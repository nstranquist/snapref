//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package processtree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandContextPreservesOutput(t *testing.T) {
	cmd := CommandContext(context.Background(), "sh", "-c", "printf ready")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CombinedOutput: %v", err)
	}
	if string(out) != "ready" {
		t.Fatalf("output = %q, want ready", out)
	}
}

func TestCommandContextTimeoutDoesNotWaitOnDescendantOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	cmd := CommandContext(ctx, "sh", "-c", "sleep 30 & wait")
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "killed") && ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout waited on descendant-owned output for %s", elapsed)
	}
}

func TestPOSIXCancellationScopeIsProcessGroup(t *testing.T) {
	if got := PlatformCancellationScope(); got != CancellationScopeProcessGroup {
		t.Fatalf("PlatformCancellationScope() = %q, want %q", got, CancellationScopeProcessGroup)
	}
}

func TestKillCompletedCommandCannotTargetReusedProcessGroup(t *testing.T) {
	completed := exec.Command("sh", "-c", "exit 0")
	Configure(completed)
	if err := completed.Run(); err != nil {
		t.Fatal(err)
	}

	victim := exec.Command("sh", "-c", "exec sleep 30")
	victim.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
	})

	// Model the completed command's numeric PID being reused by another
	// process-group leader. The os.Process identity still refers to the
	// completed command and must prevent any numeric group signal.
	completed.Process.Pid = victim.Process.Pid
	if err := Kill(completed); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill(completed command with reused PID) = %v, want os.ErrProcessDone", err)
	}
	if err := syscall.Kill(victim.Process.Pid, 0); err != nil {
		t.Fatalf("unrelated process group was signaled: %v", err)
	}
}
