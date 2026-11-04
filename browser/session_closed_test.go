package browser

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// The idle watchdog closes a session after its lease. Before 11-04 the next
// exec said only "no such file or directory", and the agent could not tell an
// idle close from a typo.
func TestMissingSessionErrorNamesTheIdleClose(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(AppendCommandEvent("gone", ObserverCommand{Command: "open", OK: true}))
	must(AppendCommandEvent("other", ObserverCommand{Command: "close", OK: true}))
	must(AppendCommandEvent("gone", ObserverCommand{Command: "watchdog.close", OK: true, Data: map[string]any{"idle_timeout": "10m0s", "reason": string(IdleExpiryLeaseExpired)}}))
	must(AppendCommandEvent("gone", ObserverCommand{Command: "console", OK: false, ExitCode: 1}))

	c, ok := LastSessionClose("gone")
	if !ok || !c.IdleExpired() || c.IdleTimeout != "10m0s" || c.Reason != "idle_lease_expired" || c.At.IsZero() {
		t.Fatalf("close = %+v ok=%v", c, ok)
	}
	missing := &os.PathError{Op: "open", Path: "gone.json", Err: os.ErrNotExist}
	err := MissingSessionError("gone", "host-cli browser open", missing)
	for _, want := range []string{`no such session "gone": it closed at `, "after 10m0s idle (idle lease expired)", "Reopen: host-cli browser open gone", "--keep-alive"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	if ClassifyError(err) != ErrorClassUsage {
		t.Fatalf("class = %q, want usage", ClassifyError(err))
	}

	// A caller's close, a reopen, and no history at all each read differently.
	if err := MissingSessionError("other", "snapref open", missing); !strings.Contains(err.Error(), `"other": it was closed at `) {
		t.Fatalf("manual close: %v", err)
	}
	must(AppendCommandEvent("gone", ObserverCommand{Command: "open", OK: true}))
	if _, ok := LastSessionClose("gone"); ok {
		t.Fatal("a close followed by an open must not explain a missing session")
	}
	if err := MissingSessionError("typo", "snapref open", missing); err.Error() != `no such session "typo". Open it: snapref open typo` {
		t.Fatalf("no history: %v", err)
	}
	if err := MissingSessionError("typo", "snapref open", nil); !strings.Contains(err.Error(), "its browser is not running. Reopen: snapref open typo") {
		t.Fatalf("dead process: %v", err)
	}
	other := errors.New("permission denied")
	if err := MissingSessionError("typo", "snapref open", other); !errors.Is(err, other) || ClassifyError(err) != ErrorClassUsage {
		t.Fatalf("other cause must stay wrapped: %v", err)
	}
}

func TestRemoteDriverMissingSessionUsesTheHostOpenCommand(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	restore := SessionOpenCommand
	SessionOpenCommand = "host-cli browser open"
	t.Cleanup(func() { SessionOpenCommand = restore })
	_, err := remoteDriver{}.session("nope")
	if err == nil || err.Error() != `no such session "nope". Open it: host-cli browser open nope` {
		t.Fatalf("err = %v", err)
	}
}
