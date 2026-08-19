package browser

import (
	"testing"
	"time"
)

func TestHotJSONLPrefersSnapRefEnv(t *testing.T) {
	t.Setenv("SNAPREF_JSONL_MAX_BYTES", "12")
	t.Setenv("NDEV_JSONL_MAX_BYTES", "1048576")
	if got := hotJSONLMaxBytes(); got != 12 {
		t.Fatalf("hotJSONLMaxBytes = %d, want 12", got)
	}
}

func TestAppendEventFamilySurvivesRotate(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	t.Setenv("SNAPREF_JSONL_MAX_BYTES", "80")
	for i := 0; i < 4; i++ {
		if err := AppendEvent(ObserverEvent{
			Kind:    KindCommand,
			Session: "s",
			Command: &ObserverCommand{Command: "exec.snapshot", OK: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalEvents != 4 {
		t.Fatalf("stats family events = %d, want 4", st.TotalEvents)
	}
	end, err := CommandEventLogSize()
	if err != nil {
		t.Fatal(err)
	}
	// Observe starts at live end after rotate: no archive replay.
	tail, next, err := ReadCommandEventsAfter(end)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 0 {
		t.Fatalf("live tail after rotate = %d, want 0", len(tail))
	}
	if next != end {
		t.Fatalf("live cursor = %d, want %d", next, end)
	}
	family, cursor, err := ReadCommandEventFamily()
	if err != nil {
		t.Fatal(err)
	}
	if len(family) != 4 {
		t.Fatalf("family replay = %d, want 4", len(family))
	}
	again, _, err := ReadCommandEventsAfter(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("tail after family cursor re-emitted %d", len(again))
	}
	// Give the clock a tick so a same-second dest is still unique if needed.
	time.Sleep(time.Millisecond)
}

func TestObserveStartAtEndAfterRotateDoesNotReplayFamily(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	t.Setenv("SNAPREF_JSONL_MAX_BYTES", "80")
	for i := 0; i < 4; i++ {
		if err := AppendEvent(ObserverEvent{
			Kind:    KindCommand,
			Session: "s",
			Command: &ObserverCommand{Command: "exec.snapshot", OK: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	end, err := CommandEventLogSize()
	if err != nil {
		t.Fatal(err)
	}
	events, next, err := ObserveSnapshot("", end)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Kind == KindCommand {
			t.Fatalf("observe start-at-end after rotate replayed command %+v", ev)
		}
	}
	if next != end {
		t.Fatalf("observe cursor = %d, want live end %d", next, end)
	}
	t.Setenv("SNAPREF_JSONL_MAX_BYTES", "1048576")
	if err := AppendEvent(ObserverEvent{
		Kind:    KindCommand,
		Session: "s",
		Command: &ObserverCommand{Command: "exec.click", OK: true},
	}); err != nil {
		t.Fatal(err)
	}
	delta, _, err := ReadCommandEventsAfter(end)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta) != 1 || delta[0].Command == nil || delta[0].Command.Command != "exec.click" {
		t.Fatalf("post-rotate live tail = %+v, want 1 exec.click", delta)
	}
}

func TestSnapshotStatsFamilySurvivesRotate(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	t.Setenv("SNAPREF_JSONL_MAX_BYTES", "80")
	for i := 0; i < 4; i++ {
		if err := AppendSnapshotEvent("atlas", &PageSnapshot{URL: "x", Matched: 2, Returned: 2, DurationMS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Snapshots.Count != 4 {
		t.Fatalf("snapshot family count = %d, want 4", st.Snapshots.Count)
	}
}
