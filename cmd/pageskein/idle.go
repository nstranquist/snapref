package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/pageskein/browser"
)

const defaultPageSkeinIdleTimeout = 10 * time.Minute

func configuredPageSkeinIdleTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("PAGESKEIN_BROWSER_IDLE_TIMEOUT"))
	if raw == "" {
		return defaultPageSkeinIdleTimeout, nil
	}
	timeout, err := parsePageSkeinDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("PAGESKEIN_BROWSER_IDLE_TIMEOUT: %w", err)
	}
	if err := browser.ValidateManagedIdleTimeout(timeout); err != nil {
		return 0, fmt.Errorf("PAGESKEIN_BROWSER_IDLE_TIMEOUT: %w", err)
	}
	return timeout, nil
}

func parsePageSkeinDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("empty duration")
	}
	unit := raw[len(raw)-1]
	if unit != 'd' && unit != 'w' {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return 0, fmt.Errorf("bad duration %q", raw)
		}
		return timeout, nil
	}
	n, err := strconv.ParseFloat(raw[:len(raw)-1], 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, fmt.Errorf("bad duration %q", raw)
	}
	hours := 24.0
	if unit == 'w' {
		hours *= 7
	}
	nanos := n * hours * float64(time.Hour)
	if nanos > float64(math.MaxInt64) {
		return 0, fmt.Errorf("duration %q is too large", raw)
	}
	timeout := time.Duration(nanos)
	if timeout <= 0 {
		return 0, fmt.Errorf("bad duration %q", raw)
	}
	return timeout, nil
}

func openManagedPageSkeinSession(opts browser.OpenOptions, idleTimeout time.Duration) (*browser.Session, error) {
	if idleTimeout > 0 {
		if err := browser.ValidateManagedIdleTimeout(idleTimeout); err != nil {
			return nil, err
		}
	}
	session, err := browser.Open(opts)
	if err != nil {
		return nil, err
	}
	if idleTimeout <= 0 {
		session, err = browser.SetSessionKeepAlive(session.Name)
		if err == nil {
			return session, nil
		}
		policyErr := fmt.Errorf("record explicit keep-alive policy: %w", err)
		if closeErr := browser.Close(session.Name, false); closeErr != nil {
			return nil, errors.Join(policyErr, fmt.Errorf("cleanup unmanaged session: %w", closeErr))
		}
		return nil, policyErr
	}
	session, err = browser.SetSessionIdlePolicy(session.Name, idleTimeout, false)
	if err != nil {
		leaseErr := fmt.Errorf("configure idle lease: %w", err)
		if closeErr := browser.Close(session.Name, false); closeErr != nil {
			return nil, errors.Join(leaseErr, fmt.Errorf("cleanup unmanaged session: %w", closeErr))
		}
		return nil, leaseErr
	}
	if err := startPageSkeinIdleWatchdog(session, idleTimeout); err != nil {
		watchdogErr := fmt.Errorf("start idle watchdog: %w", err)
		if closeErr := browser.Close(session.Name, false); closeErr != nil {
			return nil, errors.Join(watchdogErr, fmt.Errorf("cleanup unmanaged session: %w", closeErr))
		}
		return nil, watchdogErr
	}
	return session, nil
}

func startPageSkeinIdleWatchdog(session *browser.Session, idleTimeout time.Duration) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	root, err := browser.SessionRoot()
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
	cmd := exec.Command(executable,
		"__watch-session", session.Name,
		"--pid", strconv.Itoa(session.PID),
		"--idle-timeout", idleTimeout.String(),
	)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = logFile
	browser.ConfigureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func watchPageSkeinSession(args []string) error {
	if len(args) != 5 || args[1] != "--pid" || args[3] != "--idle-timeout" {
		return errors.New("invalid internal watchdog arguments")
	}
	expectedPID, err := strconv.Atoi(args[2])
	if err != nil || expectedPID <= 0 {
		return errors.New("invalid internal watchdog PID")
	}
	idleTimeout, err := parsePageSkeinDuration(args[4])
	if err != nil {
		return fmt.Errorf("invalid internal watchdog timeout: %w", err)
	}
	result, err := browser.WatchIdleSession(context.Background(), browser.IdleWatchOptions{
		Name:        args[0],
		ExpectedPID: expectedPID,
		IdleTimeout: idleTimeout,
	})
	if err != nil {
		return err
	}
	if result.Closed {
		if err := browser.AppendCommandEvent(args[0], browser.ObserverCommand{
			Command: "watchdog.close",
			OK:      true,
			Data: map[string]any{
				"idle_timeout":   idleTimeout.String(),
				"reason":         result.Reason,
				"purged_profile": false,
			},
		}); err != nil {
			return fmt.Errorf("record watchdog close telemetry: %w", err)
		}
	}
	return nil
}

func gcSessionState(args []string) error {
	olderThan := 24 * time.Hour
	apply := false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--apply":
			apply = true
		case arg == "--dry-run":
			apply = false
		case arg == "--older-than" && i+1 < len(args):
			i++
			parsed, err := parsePageSkeinDuration(args[i])
			if err != nil {
				return fmt.Errorf("gc: bad --older-than: %w", err)
			}
			olderThan = parsed
		case strings.HasPrefix(arg, "--older-than="):
			parsed, err := parsePageSkeinDuration(strings.TrimPrefix(arg, "--older-than="))
			if err != nil {
				return fmt.Errorf("gc: bad --older-than: %w", err)
			}
			olderThan = parsed
		default:
			return fmt.Errorf("gc: unknown or incomplete flag %q", arg)
		}
	}
	report, err := browser.GC(browser.GCOptions{OlderThan: olderThan, Apply: apply})
	if err != nil {
		return err
	}
	return emit(report)
}
