//go:build !windows

package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func processStoppedOrZombie(pid int) bool {
	if !processAlive(pid) {
		return true
	}
	out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "stat=").Output()
	if err != nil {
		return !processAlive(pid)
	}
	state := strings.TrimSpace(string(out))
	return state == "" || strings.HasPrefix(state, "Z")
}

func processGroupStoppedOrZombie(pgid int) bool {
	if pgid <= 0 {
		return true
	}
	if err := syscall.Kill(-pgid, syscall.Signal(0)); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		// Darwin can report EPERM for a process-group probe during Chromium's
		// final exit race. Fall through to the read-only process-table check;
		// returning "alive" here makes an already-stopped browser fail cleanup.
	}
	out, err := exec.Command("ps", "-ax", "-o", "pgid=,stat=").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		candidate, err := strconv.Atoi(fields[0])
		if err == nil && candidate == pgid && !strings.HasPrefix(fields[1], "Z") {
			return false
		}
	}
	return true
}

func sessionOwnsProcess(session *Session) (bool, error) {
	if !session.IsAlive() {
		return false, nil
	}
	if strings.TrimSpace(session.ProfileDir) == "" {
		return false, errors.New("session has no profile directory identity")
	}
	out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", session.PID), "-o", "command=").Output()
	if err != nil {
		return false, fmt.Errorf("inspect pid %d command: %w", session.PID, err)
	}
	command := string(out)
	if !strings.Contains(command, "--user-data-dir="+session.ProfileDir) {
		return false, nil
	}
	if wantBase := filepath.Base(session.ChromePath); wantBase != "" && !strings.Contains(command, wantBase) {
		return false, nil
	}
	startedAt, err := unixProcessStartedAt(session.PID)
	if err != nil {
		return false, fmt.Errorf("inspect pid %d creation time: %w", session.PID, err)
	}
	expectedStart := session.ProcessStartedAt
	startTolerance := 5 * time.Second
	if expectedStart.IsZero() {
		expectedStart = session.StartedAt
		startTolerance = 2 * time.Minute
	}
	if expectedStart.IsZero() || absDuration(startedAt.Sub(expectedStart)) > startTolerance {
		return false, nil
	}
	return true, nil
}

func unixProcessStartedAt(pid int) (time.Time, error) {
	out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "lstart=").Output()
	if err != nil {
		return time.Time{}, err
	}
	startedAt, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse ps lstart %q: %w", strings.TrimSpace(string(out)), err)
	}
	return startedAt.UTC(), nil
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func sessionSignalGracefulStop(session *Session, grace time.Duration) error {
	if !session.IsAlive() {
		return nil
	}
	owned, err := session.ownsLaunchedProcess()
	if err != nil {
		return fmt.Errorf("refusing to signal pid %d because ownership could not be verified: %w", session.PID, err)
	}
	if !owned {
		return fmt.Errorf("refusing to signal pid %d because it is not the Chromium process for profile %s", session.PID, session.ProfileDir)
	}
	target := session.PID
	stopped := func() bool { return processStoppedOrZombie(session.PID) }
	if pgid, pgidErr := syscall.Getpgid(session.PID); pgidErr == nil && pgid == session.PID {
		// PageSkein launches Chromium in a dedicated session/process group. Signal
		// the whole tree so renderer and GPU children cannot outlive the browser.
		target = -pgid
		stopped = func() bool { return processGroupStoppedOrZombie(pgid) }
	}
	if err := syscall.Kill(target, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		if stopped() {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if stopped() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if stopped() {
		return nil
	}
	if err := syscall.Kill(target, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		if stopped() {
			return nil
		}
		return err
	}
	killDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(killDeadline) {
		if stopped() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !stopped() {
		return fmt.Errorf("process tree for pid %d remained alive after SIGKILL", session.PID)
	}
	return nil
}
