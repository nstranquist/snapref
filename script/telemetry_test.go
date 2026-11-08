package browserscript

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/snapref/browser"
)

func TestScriptTypedFailuresKeepSafeCausesWithoutPersistingPageText(t *testing.T) {
	const secret = "page-exception-secret://user:password@example.test?token=hidden"
	for _, tc := range []struct {
		name   string
		step   Step
		err    error
		class  browser.ErrorClass
		reason string
	}{
		{"eval", Step{Verb: VerbEval, JS: "bad()"}, &browser.JavaScriptEvalError{Cause: errors.New(secret)}, browser.ErrorClassUsage, "javascript_exception"},
		{"select", Step{Verb: VerbSelect, Selector: "select", Option: "missing"}, &browser.SelectOptionError{Cause: errors.New(secret)}, browser.ErrorClassSelector, "select_option_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("SNAPREF_HOME", home)
			t.Setenv("SNAPREF_RUN_KIND", "negative-probe")
			f := newFake()
			f.fail[tc.name] = tc.err
			var live strings.Builder
			rep, _ := NewRunner(f).Run(context.Background(), Plan{Name: "safe-causes", Session: "s", Steps: []Step{tc.step}}, RunOptions{AllowDrive: true, Sink: &live, Telemetry: NewFileTelemetry()})
			if rep.OK || rep.Steps[0].ErrorClass != tc.class || rep.Steps[0].ReasonCode != tc.reason || !strings.Contains(live.String(), secret) {
				t.Fatalf("live result lost cause: %+v, output=%s", rep, live.String())
			}
			rows, _, err := browser.ReadCommandEventFamily()
			if err != nil || len(rows) != 3 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			for _, row := range rows[1:] {
				if row.Error != "" || row.Command.OK || row.Command.ExitCode != 1 || row.Command.ErrorClass != tc.class || row.Command.ReasonCode != tc.reason || row.Command.RunKind != "negative-probe" {
					t.Fatalf("durable row lost safe cause: %+v", row)
				}
			}
			manifest, _ := ManifestPath(rep.RunID)
			for _, path := range []string{filepath.Join(home, "events.jsonl"), manifest} {
				body, err := os.ReadFile(path)
				if err != nil || strings.Contains(string(body), secret) || !strings.Contains(string(body), tc.reason) {
					t.Fatalf("unsafe or missing durable cause in %s: %s, %v", path, body, err)
				}
			}
		})
	}
}

func TestScriptTelemetryRejectsArbitraryReasonText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SNAPREF_HOME", home)
	const secret = "untrusted-page-secret"
	r := Result{Error: secret, ReasonCode: secret, ErrorClass: browser.ErrorClassUsage}
	tel := NewFileTelemetry()
	rep := &RunReport{RunID: "untrusted-reason", Session: "s", Steps: []Result{r}}
	tel.Step(rep, r)
	tel.Finish(rep)
	if tel.LastErr != nil {
		t.Fatal(tel.LastErr)
	}
	manifest, _ := ManifestPath(rep.RunID)
	for _, path := range []string{filepath.Join(home, "events.jsonl"), manifest} {
		body, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(body), secret) || !strings.Contains(string(body), "invalid_input") {
			t.Fatalf("unsafe reason persisted in %s: %s, %v", path, body, err)
		}
	}
}

func TestFileTelemetryWritesEventsAndManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SNAPREF_HOME", home)

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
	t.Setenv("SNAPREF_HOME", home)

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

func TestFileTelemetryFailedRunSummaryRetainsCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []Result
		want  browser.ErrorClass
	}{
		{"typed policy", []Result{{OK: true}, {Skipped: true}, {ErrorClass: browser.ErrorClassPolicy}}, browser.ErrorClassPolicy},
		{"legacy cause", []Result{{Error: "selector missing"}}, browser.ErrorClassSelector},
		{"unknown cause", nil, browser.ErrorClassInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SNAPREF_HOME", t.TempDir())
			tel := NewFileTelemetry()
			tel.Finish(&RunReport{RunID: "failed-run", Session: "s", Steps: tc.steps})
			if tel.LastErr != nil {
				t.Fatal(tel.LastErr)
			}
			rows, _, err := browser.ReadCommandEventFamily()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("rows=%d", len(rows))
			}
			row := rows[0]
			if row.Command.OK || row.Command.ExitCode != 1 || row.Command.ErrorClass != tc.want || row.ErrorClass != tc.want || row.Command.ReasonCode != browser.ErrorClassReason(tc.want) {
				t.Fatalf("summary lost failed step cause: %+v", row)
			}
		})
	}
}
