package browser

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// OpenOptions configures a long-lived session launch. ExtensionPath, if set,
// loads an unpacked extension via --load-extension.
type OpenOptions struct {
	Name             string
	ChromePath       string // empty = LocateChrome
	Runtime          RuntimeKind
	Headful          bool
	HeadfulOffscreen bool // keep required headful automation from interrupting the desktop
	Viewport         Viewport
	ExtensionPath    string
	ReadyTimeout     time.Duration // default 15s
	onProcessStart   func(int)     // internal benchmark lifecycle observer
}

// Open launches a fresh Chromium under the given session name and writes
// the session file. Returns the session record.
//
// Steps:
//  1. Validate name + locate chrome.
//  2. Acquire the per-session lifecycle lock.
//  3. Spawn chrome with --remote-debugging-port=0 +
//     --user-data-dir=<isolated profile> + --load-extension if requested.
//  4. Read Chrome's atomically selected port from DevToolsActivePort, then poll
//     http://127.0.0.1:<port>/json/version until ready, capturing
//     the websocket debugger URL + browser version.
//  5. Persist the session file atomically.
func Open(o OpenOptions) (*Session, error) {
	if err := ValidateSessionName(o.Name); err != nil {
		return nil, err
	}
	unlock, err := acquireSessionLifecycleLock(o.Name, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return openLocked(o)
}

func openLocked(o OpenOptions) (*Session, error) {
	existing, existingErr := LoadSession(o.Name)
	if existingErr != nil && !errors.Is(existingErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect existing session %q: %w", o.Name, existingErr)
	}
	if existingErr == nil && existing.IsAlive() {
		owned, ownershipErr := existing.ownsLaunchedProcess()
		if ownershipErr != nil {
			return nil, fmt.Errorf("session %q has a live pid %d whose ownership could not be verified: %w", o.Name, existing.PID, ownershipErr)
		}
		if owned {
			return nil, fmt.Errorf("session %q is already running (pid %d, port %d) — close it first", o.Name, existing.PID, existing.Port)
		}
	}
	if err := RemoveSnapshotRefs(o.Name); err != nil {
		return nil, fmt.Errorf("invalidate stale snapshot refs: %w", err)
	}
	request := RuntimeRequest{
		Kind: o.Runtime, Headful: o.Headful, ExtensionPath: o.ExtensionPath, ExternalPath: o.ChromePath,
	}
	runtimeUnlock, err := acquireRuntimeKindLocks(runtimeLockKindsForRequest(request), 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock browser runtime for launch: %w", err)
	}
	defer runtimeUnlock()
	resolved, err := ResolveRuntime(request)
	if err != nil {
		return nil, err
	}
	chrome := resolved.Path
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = 15 * time.Second
	}
	if o.Viewport.Width == 0 {
		o.Viewport = Viewport{Width: 1440, Height: 900}
	}

	profileDir, err := ProfileDirFor(o.Name)
	if err != nil {
		return nil, err
	}
	profileCreated := false
	if _, err := os.Stat(profileDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect profile dir: %w", err)
		}
		profileCreated = true
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("create profile dir: %w", err)
	}
	activePortPath := filepath.Join(profileDir, "DevToolsActivePort")
	if err := os.Remove(activePortPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale DevToolsActivePort: %w", err)
	}

	args := chromeLaunchArgs(o, resolved.Kind, profileDir)

	cmd := exec.Command(chrome, args...)
	cmd.Stdout = io.Discard
	if os.Getenv("SNAPREF_DEBUG") != "" || os.Getenv("NDEV_BROWSER_DEBUG") != "" {
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = io.Discard
	}
	ConfigureDetachedProcess(cmd) // don't propagate parent signals

	processStartedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		if profileCreated {
			if removeErr := os.RemoveAll(profileDir); removeErr != nil {
				return nil, errors.Join(fmt.Errorf("start chrome: %w", err), fmt.Errorf("remove failed profile %s: %w", profileDir, removeErr))
			}
		}
		return nil, fmt.Errorf("start chrome: %w", err)
	}
	pid := cmd.Process.Pid
	if o.onProcessStart != nil {
		o.onProcessStart(pid)
	}
	readyDeadline := time.Now().Add(o.ReadyTimeout)
	remainingReadyTime := func(stage string) (time.Duration, error) {
		remaining := time.Until(readyDeadline)
		if remaining <= 0 {
			return 0, fmt.Errorf("%s: readiness deadline %s exhausted", stage, o.ReadyTimeout)
		}
		return remaining, nil
	}

	remaining, err := remainingReadyTime("wait for DevToolsActivePort")
	if err != nil {
		return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, err)
	}
	port, err := waitForDevToolsActivePort(profileDir, pid, remaining)
	if err != nil {
		return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, fmt.Errorf("chrome did not publish DevToolsActivePort: %w", err))
	}
	remaining, err = remainingReadyTime("wait for CDP")
	if err != nil {
		return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, err)
	}
	wsURL, err := waitForCDP(port, pid, remaining)
	if err != nil {
		return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, fmt.Errorf("chrome did not become reachable on port %d: %w", port, err))
	}
	remaining, err = remainingReadyTime("wait for page target")
	if err != nil {
		return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, err)
	}
	if err := waitForPageTarget(port, pid, remaining); err != nil {
		return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, fmt.Errorf("chrome did not create a page target on port %d: %w", port, err))
	}

	s := &Session{
		Name:             o.Name,
		PID:              pid,
		Port:             port,
		ProfileDir:       profileDir,
		ChromePath:       chrome,
		ProcessStartedAt: processStartedAt,
		StartedAt:        time.Now().UTC(),
		Headful:          o.Headful,
		WSURL:            wsURL,
		RuntimeKind:      resolved.Kind,
		RuntimeVersion:   resolved.Version,
		RuntimeManaged:   resolved.Managed,
		RuntimeManifest:  resolved.ManifestDigest,
		RuntimeCaps:      resolved.Capabilities,
	}
	if o.ExtensionPath != "" {
		// Match by deterministic ID derived from path so we don't pick up
		// chromium component extensions (Google Network Speech, etc.).
		expectedID := ComputeExtensionID(o.ExtensionPath)
		remaining, remainingErr := remainingReadyTime("wait for extension service worker")
		if remainingErr != nil {
			return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, remainingErr)
		}
		id, discoveryErr := discoverExtensionByID(port, expectedID, remaining)
		id, discoveryErr = requireExtensionReadiness(expectedID, id, discoveryErr)
		if discoveryErr != nil {
			return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, discoveryErr)
		}
		s.ExtensionID = id
	}
	if err := SaveSession(s); err != nil {
		return nil, cleanupFailedOpen(cmd, profileDir, profileCreated, fmt.Errorf("save session: %w", err))
	}
	// Detach only after the process is ready and its durable handoff exists. If
	// startup fails before here, cmd.Wait can still reap the direct child.
	if err := cmd.Process.Release(); err != nil {
		removeErr := RemoveSession(s.Name)
		cleanupErr := cleanupFailedOpen(cmd, profileDir, profileCreated, fmt.Errorf("detach ready chrome pid %d: %w", pid, err))
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, errors.Join(cleanupErr, fmt.Errorf("remove unusable session record: %w", removeErr))
		}
		return nil, cleanupErr
	}
	return s, nil
}

func chromeLaunchArgs(o OpenOptions, runtimeKind RuntimeKind, profileDir string) []string {
	args := []string{
		"--remote-debugging-port=0",
		fmt.Sprintf("--user-data-dir=%s", profileDir),
		fmt.Sprintf("--window-size=%d,%d", o.Viewport.Width, o.Viewport.Height),
		"--force-device-scale-factor=1",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-notifications",
		"--disable-features=Translate,InfinitePrerendering",
	}
	if o.Headful && o.HeadfulOffscreen {
		args = append(args, "--window-position=-32000,-32000")
	}
	if !o.Headful {
		if runtimeKind != RuntimeHeadlessShell {
			args = append(args, "--headless=new")
		}
	}
	if o.ExtensionPath != "" {
		args = append(args,
			fmt.Sprintf("--disable-extensions-except=%s", o.ExtensionPath),
			fmt.Sprintf("--load-extension=%s", o.ExtensionPath),
		)
	}
	args = append(args, "about:blank")
	return args
}

func requireExtensionReadiness(expectedID, discoveredID string, discoveryErr error) (string, error) {
	if discoveryErr != nil {
		return "", fmt.Errorf("extension %s did not become ready: %w", expectedID, discoveryErr)
	}
	if discoveredID == "" {
		return "", fmt.Errorf("extension %s did not become ready: service worker returned an empty extension id", expectedID)
	}
	if discoveredID != expectedID {
		return "", fmt.Errorf("extension %s did not become ready: discovered unexpected extension %s", expectedID, discoveredID)
	}
	return discoveredID, nil
}

func cleanupFailedOpen(cmd *exec.Cmd, profileDir string, removeProfile bool, cause error) error {
	errList := []error{cause}
	if cmd != nil && cmd.Process != nil {
		if err := killStartedProcessTree(cmd); err != nil && !errors.Is(err, os.ErrProcessDone) {
			errList = append(errList, fmt.Errorf("kill failed chrome pid %d: %w", cmd.Process.Pid, err))
		}
		// An ExitError is the expected result after Kill. Other wait failures
		// matter because they mean the direct child may not have been reaped.
		if err := cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) && !errors.Is(err, os.ErrProcessDone) {
				errList = append(errList, fmt.Errorf("reap failed chrome pid %d: %w", cmd.Process.Pid, err))
			}
		}
	}
	if removeProfile {
		if err := os.RemoveAll(profileDir); err != nil {
			errList = append(errList, fmt.Errorf("remove failed profile %s: %w", profileDir, err))
		}
	}
	return errors.Join(errList...)
}

// Close stops the session: graceful SIGTERM with a 5s window, then
// SIGKILL, then removes the session file. ProfileDir is preserved by
// default (lets the user reopen with the same cookies); pass purgeProfile
// to wipe it.
func Close(name string, purgeProfile bool) error {
	if err := ValidateSessionName(name); err != nil {
		return err
	}
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	return closeLocked(name, purgeProfile)
}

func closeLocked(name string, purgeProfile bool) error {
	s, err := LoadSession(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if refErr := RemoveSnapshotRefs(name); refErr != nil {
				return refErr
			}
			if !purgeProfile {
				return nil
			}
			profileDir, profileErr := ProfileDirFor(name)
			if profileErr != nil {
				return profileErr
			}
			return os.RemoveAll(profileDir)
		}
		return err
	}
	if err := s.SignalGracefulStop(5 * time.Second); err != nil {
		return fmt.Errorf("stop pid %d: %w", s.PID, err)
	}
	if err := RemoveSession(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := RemoveSnapshotRefs(name); err != nil {
		return fmt.Errorf("remove snapshot refs: %w", err)
	}
	if purgeProfile {
		profileDir, err := ProfileDirFor(name)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(profileDir); err != nil {
			return fmt.Errorf("remove profile %s: %w", profileDir, err)
		}
	}
	return nil
}

// Status returns a SessionStatus for one session: alive bit + CDP
// reachability + http URL for the devtools front-end.
func Status(name string) (*SessionStatus, error) {
	s, err := LoadSession(name)
	if err != nil {
		return nil, err
	}
	st := &SessionStatus{Session: *s, Alive: s.IsAlive()}
	st.HTTPURL = fmt.Sprintf("http://127.0.0.1:%d", s.Port)
	if !st.Alive {
		st.Error = "process is not running"
		return st, nil
	}
	_, err = readCDPVersion(s.Port, 1*time.Second)
	if err != nil {
		st.Error = err.Error()
		return st, nil
	}
	st.Reachable = true
	return st, nil
}

// waitForDevToolsActivePort waits for Chrome to publish the kernel-selected
// debugging port in the session profile. Open removes any stale file before
// launch, so a successfully parsed value belongs to this launch.
func waitForDevToolsActivePort(profileDir string, pid int, timeout time.Duration) (int, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	path := filepath.Join(profileDir, "DevToolsActivePort")
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
			port, parseErr := strconv.Atoi(strings.TrimSpace(line))
			if parseErr == nil && port > 0 && port <= 65535 {
				return port, nil
			}
			last = fmt.Errorf("invalid port %q", line)
		} else if !errors.Is(err, os.ErrNotExist) {
			last = err
		}
		if pid > 0 && !processAlive(pid) {
			return 0, fmt.Errorf("chrome pid %d exited before publishing port", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("file %s was not created", path)
	}
	return 0, fmt.Errorf("timeout after %s: %w", timeout, last)
}

// waitForCDP polls the devtools `/json/version` endpoint until the
// browser is reachable, returning the websocket debugger URL.
func waitForCDP(port, pid int, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		return "", errors.New("CDP readiness timeout must be positive")
	}
	deadline := time.Now().Add(timeout)
	last := error(nil)
	for time.Now().Before(deadline) {
		if pid > 0 && !processAlive(pid) {
			return "", fmt.Errorf("chrome pid %d exited before CDP became ready", pid)
		}
		requestTimeout := time.Until(deadline)
		if requestTimeout > 500*time.Millisecond {
			requestTimeout = 500 * time.Millisecond
		}
		v, err := readCDPVersion(port, requestTimeout)
		if err == nil {
			if pid > 0 && !processAlive(pid) {
				return "", fmt.Errorf("chrome pid %d exited while CDP became ready", pid)
			}
			return v.WebSocketDebuggerURL, nil
		}
		last = err
		time.Sleep(150 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout after %s", timeout)
	}
	return "", last
}

// waitForPageTarget makes Open fail closed until the session has a driveable
// page target. Chromium normally creates the about:blank target from argv, but
// dogfooding found a startup race where /json/version was reachable while /json
// still had zero page targets; the next navigate then failed with "no page
// target on session". We recover by creating an about:blank target before the
// session file is handed to callers.
func waitForPageTarget(port, pid int, timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("page-target readiness timeout must be positive")
	}
	deadline := time.Now().Add(timeout)
	last := error(nil)
	for time.Now().Before(deadline) {
		if pid > 0 && !processAlive(pid) {
			return fmt.Errorf("chrome pid %d exited before a page target became ready", pid)
		}
		if _, ok := firstPageTargetID(port); ok {
			return nil
		}
		attemptTimeout := time.Until(deadline)
		if attemptTimeout > time.Second {
			attemptTimeout = time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		_, ok := ensurePageTargetID(ctx, port)
		cancel()
		if ok {
			return nil
		}
		last = fmt.Errorf("no page target in /json")
		time.Sleep(150 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout after %s", timeout)
	}
	return last
}

type cdpVersion struct {
	Browser              string `json:"Browser"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func readCDPVersion(port int, timeout time.Duration) (*cdpVersion, error) {
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("CDP readiness endpoint must not redirect")
		},
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 1<<20 {
		return nil, errors.New("CDP readiness response exceeds 1 MiB")
	}
	var v cdpVersion
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if strings.TrimSpace(v.Browser) == "" {
		return nil, errors.New("CDP readiness response has no browser identity")
	}
	if err := validateCDPWebSocketURL(v.WebSocketDebuggerURL, port, "/devtools/browser/"); err != nil {
		return nil, err
	}
	return &v, nil
}

func validateCDPWebSocketURL(raw string, port int, pathPrefix string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid CDP websocket URL: %w", err)
	}
	if parsed.Scheme != "ws" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("unsafe CDP websocket URL %q", raw)
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || !address.IsLoopback() {
		return fmt.Errorf("CDP websocket host %q is not a literal loopback address", parsed.Hostname())
	}
	wsPort, err := strconv.Atoi(parsed.Port())
	if err != nil || wsPort != port {
		return fmt.Errorf("CDP websocket port %q does not match expected %d", parsed.Port(), port)
	}
	suffix := strings.TrimPrefix(parsed.EscapedPath(), pathPrefix)
	if !strings.HasPrefix(parsed.EscapedPath(), pathPrefix) || suffix == "" ||
		strings.ContainsAny(suffix, "/\\%") || suffix == "." || suffix == ".." {
		return fmt.Errorf("CDP websocket path %q is not a %s endpoint", parsed.EscapedPath(), strings.Trim(pathPrefix, "/"))
	}
	return nil
}

// discoverExtensionID finds the unpacked extension's ID. Chromium derives
// the ID deterministically from the SHA256 of the extension's absolute
// path: first 16 bytes mapped from hex 0-f → letters a-p. We compute the
// expected ID and then poll /json/list until a target with that ID
// appears (proving the SW finished registering).
//
// Component extensions (Google Network Speech, etc.) also expose SW
// targets at chrome-extension:// URLs, so we cannot just pick the first
// SW we see — we have to filter by our exact ID.
func discoverExtensionID(port int, timeout time.Duration) (string, error) {
	// We don't have the path here; the caller must compute the expected
	// ID and match. Instead, we fall back to: pick the SW whose URL
	// references our extension (caller supplies expected ID via the
	// public ComputeExtensionID + DiscoverExtensionByID helpers).
	return discoverExtensionByID(port, "", timeout)
}

// discoverExtensionByID polls until a SW or other target with the given
// extension ID appears. If wantID is empty, returns the first non-component
// extension SW (best-effort fallback for the legacy code path).
func discoverExtensionByID(port int, wantID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	requestTimeout := timeout
	if requestTimeout > time.Second {
		requestTimeout = time.Second
	}
	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("extension readiness endpoint must not redirect")
		},
	}
	const prefix = "chrome-extension://"
	var lastErr error
	for time.Now().Before(deadline) {
		targets, err := readExtensionTargets(client, port)
		if err != nil {
			lastErr = err
		} else {
			for _, target := range targets {
				if target.Type != "service_worker" || !strings.HasPrefix(target.URL, prefix) {
					continue
				}
				rest := target.URL[len(prefix):]
				slash := strings.IndexByte(rest, '/')
				id := rest
				if slash >= 0 {
					id = rest[:slash]
				}
				if wantID == "" {
					return id, nil
				}
				if id == wantID {
					return id, nil
				}
			}
		}
		pollDelay := time.Until(deadline)
		if pollDelay > 200*time.Millisecond {
			pollDelay = 200 * time.Millisecond
		}
		if pollDelay > 0 {
			time.Sleep(pollDelay)
		}
	}
	if wantID != "" {
		if lastErr != nil {
			return "", fmt.Errorf("no service_worker registered for extension id %s within %s: %w", wantID, timeout, lastErr)
		}
		return "", fmt.Errorf("no service_worker registered for extension id %s within %s", wantID, timeout)
	}
	if lastErr != nil {
		return "", fmt.Errorf("no extension service worker registered within timeout: %w", lastErr)
	}
	return "", errors.New("no extension service worker registered within timeout")
}

func readExtensionTargets(client *http.Client, port int) ([]cdpTargetListEntry, error) {
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("extension readiness endpoint returned http %d", resp.StatusCode)
	}
	const maxBodyBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read extension readiness response: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, errors.New("extension readiness response exceeds 1 MiB")
	}
	var targets []cdpTargetListEntry
	if err := json.Unmarshal(body, &targets); err != nil {
		return nil, fmt.Errorf("parse extension readiness response: %w", err)
	}
	return targets, nil
}

// ComputeExtensionID returns the deterministic extension ID chromium
// derives from an unpacked extension's absolute directory path:
// SHA256(absolute_path); take the first 16 bytes; map each nibble
// '0'-'f' → 'a'-'p'.
func ComputeExtensionID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	id := make([]byte, 32)
	for i := 0; i < 16; i++ {
		id[i*2] = mapNibble(sum[i] >> 4)
		id[i*2+1] = mapNibble(sum[i] & 0x0f)
	}
	return string(id)
}

func mapNibble(n byte) byte {
	if n > 0x0f {
		n &= 0x0f
	}
	return 'a' + n
}
