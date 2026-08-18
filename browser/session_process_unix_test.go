//go:build !windows

package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGracefulStopTerminatesDedicatedBrowserProcessTree(t *testing.T) {
	home := setBrowserHome(t)
	profile := filepath.Join(home, "profiles", "process-tree")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestBrowserProcessTreeHelper$", "--", "--user-data-dir="+profile)
	cmd.Env = append(os.Environ(),
		"SNAPREF_TREE_PARENT=1",
		"SNAPREF_TREE_CHILD_PID_FILE="+childPIDFile,
	)
	ConfigureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(childPIDFile)
		if err == nil {
			childPID, err = strconv.Atoi(string(body))
			if err == nil && childPID > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatal("helper did not publish child PID")
	}
	startedAt, err := unixProcessStartedAt(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{
		Name: "process-tree", PID: cmd.Process.Pid, ProfileDir: profile,
		ChromePath: os.Args[0], ProcessStartedAt: startedAt, StartedAt: startedAt,
		RuntimeKind: RuntimeChromeForTesting, RuntimeVersion: "151.0.0.0", RuntimeManaged: true,
		RuntimeManifest: strings.Repeat("a", 64), RuntimeCaps: RuntimeCapabilities{Headless: true, Headful: true, Extensions: true, Screenshots: true},
	}
	if err := session.SignalGracefulStop(time.Second); err != nil {
		t.Fatal(err)
	}
	if !processStoppedOrZombie(cmd.Process.Pid) {
		t.Fatalf("browser parent pid %d survived close", cmd.Process.Pid)
	}
	if !processStoppedOrZombie(childPID) {
		t.Fatalf("browser child pid %d survived close", childPID)
	}
	_, _ = cmd.Process.Wait()
	cmd.Process = nil
}

func TestBrowserProcessTreeHelper(t *testing.T) {
	if os.Getenv("SNAPREF_TREE_PARENT") != "1" {
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestBrowserProcessTreeChild$")
	child.Env = append(os.Environ(), "SNAPREF_TREE_CHILD=1", "SNAPREF_TREE_PARENT=")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("SNAPREF_TREE_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestBrowserProcessTreeChild(t *testing.T) {
	if os.Getenv("SNAPREF_TREE_CHILD") != "1" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}
