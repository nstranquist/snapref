//go:build windows

package processtree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestWindowsCommandContextKillsObservedDescendants(t *testing.T) {
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsCancellationHelper$")
	cmd.Env = append(os.Environ(),
		"PROCESSTREE_WINDOWS_HELPER=1",
		"PROCESSTREE_WINDOWS_CHILD_PID_FILE="+childPIDFile,
	)
	_, err := cmd.CombinedOutput()
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("CombinedOutput error = %v, context error = %v", err, ctx.Err())
	}
	body, err := os.ReadFile(childPIDFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for windowsProcessAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if windowsProcessAlive(childPID) {
		t.Fatalf("descendant pid %d remained alive", childPID)
	}
}

func TestWindowsCancellationHelper(t *testing.T) {
	if os.Getenv("PROCESSTREE_WINDOWS_HELPER") != "1" {
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestWindowsCancellationChild$")
	child.Env = append(os.Environ(),
		"PROCESSTREE_WINDOWS_HELPER=",
		"PROCESSTREE_WINDOWS_CHILD=1",
	)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("PROCESSTREE_WINDOWS_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestWindowsCancellationChild(t *testing.T) {
	if os.Getenv("PROCESSTREE_WINDOWS_CHILD") != "1" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func windowsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION|syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	state, err := syscall.WaitForSingleObject(handle, 0)
	return err == nil && state == uint32(syscall.WAIT_TIMEOUT)
}

func TestWindowsCancellationScopeIsDescendantSnapshot(t *testing.T) {
	if got := PlatformCancellationScope(); got != CancellationScopeDescendantSnapshot {
		t.Fatalf("PlatformCancellationScope() = %q, want %q", got, CancellationScopeDescendantSnapshot)
	}
}

func TestWindowsDescendantValidationDiscardsChangedPIDs(t *testing.T) {
	processes := []windowsProcessHandle{{pid: 20}, {pid: 30}}
	retained, discarded := retainValidatedWindowsProcesses(processes, []uint32{30, 40})
	if len(retained) != 1 || retained[0].pid != 30 {
		t.Fatalf("retained = %+v, want only pid 30", retained)
	}
	if len(discarded) != 1 || discarded[0].pid != 20 {
		t.Fatalf("discarded = %+v, want only pid 20", discarded)
	}
}

func TestKillCompletedCommandCannotTargetReusedWindowsPID(t *testing.T) {
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	victim := exec.Command(os.Args[0], "-test.run=^TestWindowsCancellationHelper$")
	victim.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	victim.Env = append(os.Environ(),
		"PROCESSTREE_WINDOWS_HELPER=1",
		"PROCESSTREE_WINDOWS_CHILD_PID_FILE="+childPIDFile,
	)
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := waitForWindowsChildPID(t, childPIDFile)
	t.Cleanup(func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
		if child, err := openWindowsProcess(uint32(childPID)); err == nil {
			_ = terminateWindowsProcess(child)
			_ = syscall.CloseHandle(child.handle)
		}
	})

	completed := exec.Command(os.Args[0], "-test.run=^TestConfigureHelper$")
	completed.Env = append(os.Environ(), "PROCESSTREE_CONFIGURE_HELPER=1")
	Configure(completed)
	if err := completed.Run(); err != nil {
		t.Fatal(err)
	}

	// Model reuse of the completed command's numeric PID by the victim tree.
	// Its retained os.Process handle still identifies the completed command.
	completed.Process.Pid = victim.Process.Pid
	if err := Kill(completed); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill(completed command with reused PID) = %v, want os.ErrProcessDone", err)
	}
	if !windowsProcessAlive(childPID) {
		t.Fatalf("unrelated descendant pid %d was terminated", childPID)
	}
}

func TestKillExitedUnwaitedCommandCannotTargetReusedWindowsPID(t *testing.T) {
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	victim := exec.Command(os.Args[0], "-test.run=^TestWindowsCancellationHelper$")
	victim.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	victim.Env = append(os.Environ(),
		"PROCESSTREE_WINDOWS_HELPER=1",
		"PROCESSTREE_WINDOWS_CHILD_PID_FILE="+childPIDFile,
	)
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := waitForWindowsChildPID(t, childPIDFile)
	t.Cleanup(func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
		if child, err := openWindowsProcess(uint32(childPID)); err == nil {
			_ = terminateWindowsProcess(child)
			_ = syscall.CloseHandle(child.handle)
		}
	})

	exited := exec.Command(os.Args[0], "-test.run=^TestConfigureHelper$")
	exited.Env = append(os.Environ(), "PROCESSTREE_CONFIGURE_HELPER=1")
	Configure(exited)
	if err := exited.Start(); err != nil {
		t.Fatal(err)
	}
	waitForWindowsProcessExit(t, exited.Process)
	t.Cleanup(func() { _ = exited.Wait() })

	// Model the exited root's numeric PID being reused before its owner calls
	// Wait. The retained handle is already signaled, so no numeric snapshot may
	// be taken for the replacement tree.
	exited.Process.Pid = victim.Process.Pid
	if err := Kill(exited); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill(exited unwaited command with reused PID) = %v, want os.ErrProcessDone", err)
	}
	if !windowsProcessAlive(childPID) {
		t.Fatalf("unrelated descendant pid %d was terminated", childPID)
	}
}

func waitForWindowsChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(body))
			if parseErr != nil {
				t.Fatalf("parse child pid: %v", parseErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child pid file %s was not created", path)
	return 0
}

func waitForWindowsProcessExit(t *testing.T, process *os.Process) {
	t.Helper()
	var state uint32
	var waitErr error
	if err := process.WithHandle(func(handle uintptr) {
		state, waitErr = syscall.WaitForSingleObject(syscall.Handle(handle), syscall.INFINITE)
	}); err != nil {
		t.Fatalf("retain process handle while waiting for exit: %v", err)
	}
	if waitErr != nil {
		t.Fatalf("wait for process exit: %v", waitErr)
	}
	if state != uint32(syscall.WAIT_OBJECT_0) {
		t.Fatalf("wait for process exit returned state %d", state)
	}
}
