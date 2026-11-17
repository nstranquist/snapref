//go:build !windows

package browser

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func TestWatchProcessExitFiresPromptly(t *testing.T) {
	cmd := startSleeper(t)
	exited, stop := watchProcessExit(cmd.Process.Pid)
	defer stop()
	select {
	case <-exited:
		t.Fatal("exit reported for a live process")
	case <-time.After(50 * time.Millisecond):
	}
	killed := time.Now()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	select {
	case <-exited:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("exit not reported within 1.5s")
	}
	t.Logf("exit reported %s after kill", time.Since(killed).Round(time.Millisecond))
}

func TestWatchProcessExitForGonePID(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	exited, stop := watchProcessExit(cmd.Process.Pid)
	defer stop()
	select {
	case <-exited:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("gone pid not reported as exited")
	}
}

// A normal 10-minute lease polls once a minute. Closing the browser must
// still end its watchdog within about a second (2026-11-17: 19 to 60 s).
func TestWatchIdleSessionEndsSoonAfterBrowserExit(t *testing.T) {
	setBrowserHome(t)
	cmd := startSleeper(t)
	now := time.Now().UTC()
	if err := SaveSession(&Session{
		Name: "closed-watch", PID: cmd.Process.Pid, StartedAt: now,
		LastActivityAt: now, IdleTimeout: (10 * time.Minute).String(),
	}); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result IdleExpiryResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := WatchIdleSession(context.Background(), IdleWatchOptions{
			Name: "closed-watch", ExpectedPID: cmd.Process.Pid, IdleTimeout: 10 * time.Minute,
		})
		done <- outcome{result, err}
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case got := <-done:
		t.Fatalf("watchdog ended while the browser lived: %+v", got)
	default:
	}
	closed := time.Now()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	if err := RemoveSession("closed-watch"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !got.result.Done {
			t.Fatalf("watchdog = %+v", got)
		}
		if elapsed := time.Since(closed); elapsed > 1500*time.Millisecond {
			t.Fatalf("watchdog ended %s after close, want about 1s", elapsed)
		}
		t.Logf("watchdog ended %s after close (%s)", time.Since(closed).Round(time.Millisecond), got.result.Reason)
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog still running 3s after the browser closed")
	}
}
