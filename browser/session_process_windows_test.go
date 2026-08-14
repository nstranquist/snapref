//go:build windows

package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsProcessLifecycleNative(t *testing.T) {
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	command := exec.Command(os.Args[0], "-test.run=^TestWindowsLifecycleHelper$")
	command.Env = append(os.Environ(),
		"PAGESKEIN_WINDOWS_LIFECYCLE_HELPER=1",
		"PAGESKEIN_WINDOWS_CHILD_PID_FILE="+childPIDFile,
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start lifecycle helper: %v", err)
	}
	pid := command.Process.Pid
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for !processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !processAlive(pid) {
		t.Fatal("Windows native process probe did not observe the helper")
	}
	var childPID int
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, readErr := os.ReadFile(childPIDFile)
		if readErr == nil {
			childPID, readErr = strconv.Atoi(string(body))
			if readErr == nil && childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 0 || !processAlive(childPID) {
		t.Fatalf("Windows lifecycle helper child was not observed: pid=%d", childPID)
	}
	handle, err := windows.OpenProcess(windowsProcessQueryAccess, false, uint32(pid))
	if err != nil {
		t.Fatalf("open helper process: %v", err)
	}
	startedAt, err := windowsProcessStartedAt(handle)
	windows.CloseHandle(handle)
	if err != nil {
		t.Fatalf("read helper creation time: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	profile := t.TempDir()
	const port = 41237
	if err := os.WriteFile(filepath.Join(profile, "DevToolsActivePort"), []byte("41237\n/browser-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &Session{
		PID: pid, Port: port, ProfileDir: profile, ChromePath: executable,
		ProcessStartedAt: startedAt, StartedAt: time.Now().UTC(),
	}
	owned, err := session.OwnsProcess()
	if err != nil || !owned {
		t.Fatalf("Windows ownership = %v, %v", owned, err)
	}

	session.Port++
	if owned, err := session.OwnsProcess(); err != nil || owned {
		t.Fatalf("mismatched profile port ownership = %v, %v", owned, err)
	}
	session.Port--
	if err := session.SignalGracefulStop(0); err != nil {
		t.Fatalf("Windows bounded stop: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("Windows TerminateProcess fallback did not stop the helper")
	}
	if processAlive(childPID) {
		t.Fatal("Windows process-tree fallback left the helper child alive")
	}
	_, _ = command.Process.Wait()
	command.Process = nil
}

func TestWindowsLifecycleHelper(t *testing.T) {
	if os.Getenv("PAGESKEIN_WINDOWS_LIFECYCLE_HELPER") != "1" {
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestWindowsLifecycleChild$")
	child.Env = append(os.Environ(),
		"PAGESKEIN_WINDOWS_LIFECYCLE_HELPER=",
		"PAGESKEIN_WINDOWS_LIFECYCLE_CHILD=1",
	)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("PAGESKEIN_WINDOWS_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestWindowsLifecycleChild(t *testing.T) {
	if os.Getenv("PAGESKEIN_WINDOWS_LIFECYCLE_CHILD") != "1" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}
