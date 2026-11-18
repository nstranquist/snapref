package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Holding a viewport between calls (WORK-1320).
//
// Chrome keeps an Emulation.* override only while the DevTools session that
// set it stays open, and closing any session that set an override clears the
// override for the whole page. Before this file every exec call re-applied the
// persisted viewport on its own connection, so between two calls the page
// went back to the window size (1440 wide; headless Chrome will not size a
// window below 500). Media-query listeners fired desktop/mobile on every call,
// which closed a phone drawer between two steps of an audit.
//
// A holder now owns the override: one long-lived process keeps one page
// connection open with the persisted emulation applied. Exec connections do
// not apply the override while a live holder serves the current spec, so
// closing them clears nothing. The holder follows the session record: a new
// spec, or a bumped EmulationRequest (after a one-shot screenshot size), is
// re-applied and acknowledged in EmulationHolder. It also checks the layout
// width and re-applies when an older client cleared the override. When the
// spec is cleared, the holder exits, and closing its connection clears the
// override. Without a launcher (a library consumer that registered none) or
// with a dead holder, connections re-apply per call as before.

// EmulationHolder is the record a live holder writes after each apply.
type EmulationHolder struct {
	PID     int       `json:"pid"`
	Token   string    `json:"token"`
	Applied string    `json:"applied"` // DeviceEmulation.String() of the applied spec
	Request int       `json:"request"` // the EmulationRequest it applied
	At      time.Time `json:"at"`
}

// EmulationHolderLauncher starts a detached holder for a session: each CLI
// runs its hidden subcommand, which calls HoldSessionEmulation. Nil leaves
// sessions on the per-call re-apply.
var EmulationHolderLauncher func(name string, browserPID int) error

const (
	holdCallIDBase   int64 = 1_700_000_000
	holdPollInterval       = 100 * time.Millisecond
	holdCheckEvery         = 5 // layout checks every 5 polls (500 ms)
)

// holderServes reports whether a live holder has applied s's current spec.
func holderServes(s *Session) bool {
	if s == nil || s.Emulation == nil || s.EmulationHolder == nil {
		return false
	}
	h := s.EmulationHolder
	return h.Applied == s.Emulation.String() && h.Request == s.EmulationRequest && processAlive(h.PID)
}

func holderAlive(s *Session) bool {
	return s != nil && s.EmulationHolder != nil && processAlive(s.EmulationHolder.PID)
}

// waitHolder polls the record until done(s) or the timeout.
func waitHolder(ctx context.Context, name string, timeout time.Duration, done func(*Session) bool) (*Session, error) {
	deadline := time.Now().Add(timeout)
	for {
		s, err := LoadSession(name)
		if err != nil {
			return nil, err
		}
		if done(s) {
			return s, nil
		}
		if time.Now().After(deadline) {
			return s, errors.New("viewport holder did not acknowledge in time")
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// holdSessionEmulation makes a holder serve s's persisted spec: it starts one
// when none is alive and waits for the acknowledgement.
func holdSessionEmulation(ctx context.Context, s *Session, timeout time.Duration) (*Session, error) {
	if !holderAlive(s) {
		if err := EmulationHolderLauncher(s.Name, s.PID); err != nil {
			return s, fmt.Errorf("start viewport holder: %w", err)
		}
	}
	return waitHolder(ctx, s.Name, timeout, holderServes)
}

// requestHolderApply asks the live holder to apply the spec again (after a
// one-shot override cleared it) and waits. It reports false when no holder
// is alive, so the caller re-applies on its own connection.
func requestHolderApply(ctx context.Context, name string, timeout time.Duration) (bool, error) {
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return false, err
	}
	s, err := LoadSession(name)
	if err != nil || s.Emulation == nil || !holderAlive(s) {
		unlock()
		return false, err
	}
	s.EmulationRequest++
	err = SaveSession(s)
	unlock()
	if err != nil {
		return false, err
	}
	_, err = waitHolder(ctx, name, timeout, holderServes)
	return true, err
}

// HoldSessionEmulation keeps the persisted viewport of one session generation
// applied until the browser exits, the session is replaced or closed, the
// viewport is cleared, another holder takes over, or ctx ends. CLIs run it
// from a detached hidden subcommand (EmulationHolderLauncher).
func HoldSessionEmulation(ctx context.Context, name string, browserPID int) error {
	if err := ValidateSessionName(name); err != nil {
		return err
	}
	tokenBytes := make([]byte, 8)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	var conn interface {
		cdpTransport
		Close() error
	}
	defer func() {
		if conn != nil {
			conn.Close()
		}
		releaseHolder(name, token)
	}()
	id := holdCallIDBase
	next := func() int64 {
		id++
		if id > holdCallIDBase+100_000_000 {
			id = holdCallIDBase + 1
		}
		return id
	}
	applied, appliedReq, baseline, polls := "", -1, -1.0, 0
	for {
		s, err := LoadSession(name)
		if err != nil || s.PID != browserPID || !processAlive(browserPID) {
			return nil
		}
		if s.Emulation == nil {
			return nil
		}
		if h := s.EmulationHolder; h != nil && h.Token != token && processAlive(h.PID) {
			return nil // another live holder owns this session
		}
		if conn == nil {
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			conn, err = dialFirstPageNoEmulation(dctx, s)
			cancel()
			if err != nil {
				conn = nil
				if sleepCtx(ctx, time.Second) != nil {
					return ctx.Err()
				}
				continue
			}
			applied, baseline = "", -1
		}
		spec := s.Emulation.String()
		check := polls%holdCheckEvery == 0
		polls++
		if !check && spec == applied && s.EmulationRequest == appliedReq {
			if sleepCtx(ctx, holdPollInterval) != nil {
				return ctx.Err()
			}
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		width, err := layoutWidth(cctx, conn, next())
		if err == nil && (spec != applied || s.EmulationRequest != appliedReq || width != baseline) {
			if err = applyEmulationOnConn(cctx, conn, s.Emulation); err == nil {
				width, err = layoutWidth(cctx, conn, next())
			}
			if err == nil {
				applied, appliedReq, baseline = spec, s.EmulationRequest, width
				err = acknowledgeHolder(name, browserPID, EmulationHolder{
					PID: os.Getpid(), Token: token, Applied: spec, Request: appliedReq, At: time.Now().UTC(),
				})
				if errors.Is(err, errHolderSuperseded) {
					cancel()
					return nil
				}
			}
		}
		cancel()
		if err != nil {
			// The page target went away or the connection broke: dial again.
			conn.Close()
			conn = nil
		}
		if sleepCtx(ctx, holdPollInterval) != nil {
			return ctx.Err()
		}
	}
}

// layoutWidth reads the CSS layout viewport width of the page.
func layoutWidth(ctx context.Context, conn cdpTransport, id int64) (float64, error) {
	raw, err := cdpCall(ctx, conn, id, "Page.getLayoutMetrics", nil)
	if err != nil {
		return 0, err
	}
	var m struct {
		CSSLayoutViewport struct {
			ClientWidth float64 `json:"clientWidth"`
		} `json:"cssLayoutViewport"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, err
	}
	return m.CSSLayoutViewport.ClientWidth, nil
}

var errHolderSuperseded = errors.New("session or holder replaced")

func acknowledgeHolder(name string, browserPID int, h EmulationHolder) error {
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := LoadSession(name)
	if err != nil {
		return err
	}
	if s.PID != browserPID {
		return errHolderSuperseded
	}
	if cur := s.EmulationHolder; cur != nil && cur.Token != h.Token && processAlive(cur.PID) {
		return errHolderSuperseded
	}
	s.EmulationHolder = &h
	return SaveSession(s)
}

func releaseHolder(name, token string) {
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return
	}
	defer unlock()
	s, err := LoadSession(name)
	if err != nil || s.EmulationHolder == nil || s.EmulationHolder.Token != token {
		return
	}
	s.EmulationHolder = nil
	_ = SaveSession(s)
}

// HoldViewportCommand is the hidden subcommand name both CLIs register.
const HoldViewportCommand = "__hold-viewport"

// DetachedHolderLauncher returns an EmulationHolderLauncher that runs
// `executable <prefix...> __hold-viewport <name> --pid <pid>` detached, with
// stderr in the sessions' watchdog.log. Each CLI routes the subcommand to
// RunHoldViewport.
func DetachedHolderLauncher(executable string, prefix ...string) func(name string, browserPID int) error {
	return func(name string, browserPID int) error {
		root, err := SessionRoot()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return err
		}
		logFile, err := os.OpenFile(filepath.Join(root, "watchdog.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer logFile.Close()
		args := append(append([]string{}, prefix...), HoldViewportCommand, name, "--pid", strconv.Itoa(browserPID))
		cmd := exec.Command(executable, args...)
		cmd.Stdout = io.Discard
		cmd.Stderr = logFile
		ConfigureDetachedProcess(cmd)
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
}

// RunHoldViewport runs the hidden subcommand: args are `<name> --pid <pid>`.
func RunHoldViewport(args []string) error {
	if len(args) != 3 || args[1] != "--pid" {
		return errors.New("usage: " + HoldViewportCommand + " <session> --pid <browser-pid>")
	}
	pid, err := strconv.Atoi(args[2])
	if err != nil || pid <= 0 {
		return errors.New("invalid browser PID")
	}
	return HoldSessionEmulation(context.Background(), args[0], pid)
}
