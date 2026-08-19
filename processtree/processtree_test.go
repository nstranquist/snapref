package processtree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestConfigureNilIsSafe(t *testing.T) {
	Configure(nil)
	if err := Kill(nil); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill(nil) = %v, want os.ErrProcessDone", err)
	}
}

func TestConfigurePreservesPositiveWaitDelay(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestConfigureHelper$")
	cmd.WaitDelay = 7 * time.Second
	Configure(cmd)
	if cmd.WaitDelay != 7*time.Second {
		t.Fatalf("WaitDelay = %s, want 7s", cmd.WaitDelay)
	}
}

func TestConfigureExecCommandRemainsStartable(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestConfigureHelper$")
	cmd.Env = append(os.Environ(), "PROCESSTREE_CONFIGURE_HELPER=1")
	Configure(cmd)
	if cmd.Cancel != nil {
		t.Fatal("Configure added Cancel to a command without context")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CombinedOutput: %v: %s", err, out)
	}
	if string(out) != "ready" {
		t.Fatalf("output = %q, want ready", out)
	}
}

func TestConfigureHelper(t *testing.T) {
	if os.Getenv("PROCESSTREE_CONFIGURE_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString("ready")
	os.Exit(0)
}

func TestKillCompletedCommandReturnsProcessDone(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestConfigureHelper$")
	cmd.Env = append(os.Environ(), "PROCESSTREE_CONFIGURE_HELPER=1")
	Configure(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := Kill(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill(completed command) = %v, want os.ErrProcessDone", err)
	}
}

func TestCommandContextConfiguresCancellationAndWaitDelay(t *testing.T) {
	cmd := CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	if cmd.Cancel == nil {
		t.Fatal("CommandContext did not configure cancellation")
	}
	if cmd.WaitDelay != DefaultWaitDelay {
		t.Fatalf("WaitDelay = %s, want %s", cmd.WaitDelay, DefaultWaitDelay)
	}
}

func TestPlatformCancellationScopeIsTyped(t *testing.T) {
	scope := PlatformCancellationScope()
	if scope != CancellationScopeDirectChild && scope != CancellationScopeProcessGroup && scope != CancellationScopeDescendantSnapshot {
		t.Fatalf("unexpected cancellation scope %q", scope)
	}
	if SupportsDescendantCancellation() != (scope != CancellationScopeDirectChild) {
		t.Fatalf("SupportsDescendantCancellation disagrees with scope %q", scope)
	}
}

func TestMinimalEnvironmentIsFailClosed(t *testing.T) {
	ambient := []string{
		"PATH=/usr/bin",
		"LANG=en_US.UTF-8",
		"LC_TIME=C",
		"LC_API_KEY=secret",
		"OPENAI_API_KEY=secret",
		"AWS_ACCESS_KEY_ID=secret",
		"AWS_PROFILE=production",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"HOME=/Users/operator",
		"PORT=8788",
		"malformed",
	}
	want := []string{"PATH=/usr/bin", "LANG=en_US.UTF-8", "LC_TIME=C"}
	if got := MinimalEnvironment(ambient); !reflect.DeepEqual(got, want) {
		t.Fatalf("MinimalEnvironment() = %v, want %v", got, want)
	}
}

func TestMinimalEnvironmentAllowsExplicitNamesCaseInsensitively(t *testing.T) {
	ambient := []string{"Path=/usr/bin", "PORT=8788", "SAFE_MODE=1", "TOKEN=secret"}
	want := []string{"Path=/usr/bin", "PORT=8788", "SAFE_MODE=1"}
	if got := MinimalEnvironment(ambient, "port", " SAFE_MODE ", "bad=name"); !reflect.DeepEqual(got, want) {
		t.Fatalf("MinimalEnvironment() = %v, want %v", got, want)
	}
}

func TestCredentialFreeEnvironmentUsesMinimalPolicy(t *testing.T) {
	ambient := []string{"PATH=/usr/bin", "HOME=/Users/operator", "AWS_PROFILE=production"}
	want := []string{"PATH=/usr/bin"}
	if got := CredentialFreeEnvironment(ambient); !reflect.DeepEqual(got, want) {
		t.Fatalf("CredentialFreeEnvironment() = %v, want %v", got, want)
	}
}
