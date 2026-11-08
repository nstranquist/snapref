package browser

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadStatsRejectsUnreadableSinks(t *testing.T) {
	for _, name := range []string{"events.jsonl", "snapshot-events.jsonl", "findings.jsonl"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("SNAPREF_HOME", home)
			if err := os.Mkdir(filepath.Join(home, name), 0700); err != nil {
				t.Fatal(err)
			}
			if st, err := ReadStats(StatsOptions{}); err == nil {
				t.Fatalf("unreadable %s reported healthy: %+v", name, st)
			}
		})
	}
}

func TestReadStatsAccountsForEveryFailureClass(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	for _, class := range []ErrorClass{ErrorClassAssertion, ErrorClassTransport, ""} {
		ev := cmdEvent("review", "exec.click", false, time.Now())
		ev.Command.ErrorClass = class
		if err := AppendEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	st, err := ReadStats(StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.NonOK != 3 || st.FailuresByClass[ErrorClassAssertion] != 1 || st.FailuresByClass[ErrorClassTransport] != 1 || st.UnclassifiedFailures != 1 {
		t.Fatalf("failure accounting: %+v", st)
	}
}
