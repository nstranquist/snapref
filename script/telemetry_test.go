package browserscript

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/pageskein/browser"
)

func TestFileTelemetryWritesEventsAndManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAGESKEIN_HOME", home)

	f := newFake()
	f.text["h1"] = "Hi"
	plan := Plan{Name: "smoke", Session: "s", Steps: []Step{
		{Verb: VerbNavigate, URL: "https://x.test"},
		{Verb: VerbText, Selector: "h1", Capture: "title"},
	}}
	tel := NewFileTelemetry()
	rep, err := NewRunner(f).Run(context.Background(), plan, RunOptions{
		Source: "sdk", Now: fixedClock(), Telemetry: tel, AllowDrive: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tel.LastErr != nil {
		t.Fatalf("telemetry error: %v", tel.LastErr)
	}

	// events.jsonl: script-start, two steps, script-end.
	evPath := filepath.Join(home, "events.jsonl")
	b, err := os.ReadFile(evPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	var kinds []string
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		var ev browser.ObserverEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			kinds = append(kinds, ev.Kind)
		}
	}
	want := []string{browser.KindScript, browser.KindStep, browser.KindStep, browser.KindScript}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}

	// manifest exists and round-trips.
	mp, err := ManifestPath(rep.RunID)
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	mb, err := os.ReadFile(mp)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var got RunReport
	if err := json.Unmarshal(mb, &got); err != nil {
		t.Fatalf("manifest json: %v", err)
	}
	if got.RunID != rep.RunID || got.Plan != "smoke" || len(got.Steps) != 2 || !got.OK {
		t.Fatalf("manifest mismatch: %+v", got)
	}

	// ListRuns sees it.
	runs, err := ListRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %d runs, err %v", len(runs), err)
	}
}

func TestManifestRedactsCapturedAndAssertValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAGESKEIN_HOME", home)

	f := newFake()
	f.text["#tok"] = "supersecret-abc123"
	f.eval["location.href"] = `"https://x.test/p?token=leak#frag"`
	plan := Plan{Name: "redact", Session: "s", Steps: []Step{
		{Verb: VerbText, Selector: "#tok", Capture: "api_token"}, // secret-named capture
		{Verb: VerbAssertURL, Assert: &Assertion{Contains: "x.test"}},
	}}
	rep, err := NewRunner(f).Run(context.Background(), plan, RunOptions{Now: fixedClock(), Telemetry: NewFileTelemetry()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// In-memory report (agent-facing stream) keeps the full value.
	if rep.Steps[0].Captured["api_token"] != "supersecret-abc123" {
		t.Fatalf("in-memory capture should be full: %v", rep.Steps[0].Captured)
	}
	// On-disk manifest must NOT contain the secret or the URL query/fragment.
	mp, _ := ManifestPath(rep.RunID)
	mb, err := os.ReadFile(mp)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	s := string(mb)
	if strings.Contains(s, "supersecret-abc123") {
		t.Fatalf("manifest leaked secret-named captured value:\n%s", s)
	}
	if strings.Contains(s, "token=leak") || strings.Contains(s, "frag") {
		t.Fatalf("manifest leaked URL query/fragment in assert actual:\n%s", s)
	}
}
