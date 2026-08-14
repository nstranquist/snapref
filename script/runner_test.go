package browserscript

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/pageskein/browser"
)

// fakeDriver is a programmable browser.Driver for engine tests — no Chrome.
type fakeDriver struct {
	calls   []string
	navOpts []browser.NavigateOptions
	text    map[string]string
	eval    map[string]string
	fetch   string
	console []browser.ConsoleEntry
	fail    map[string]error // keyed by verb name
}

func newFake() *fakeDriver {
	return &fakeDriver{text: map[string]string{}, eval: map[string]string{}, fail: map[string]error{}}
}

func (f *fakeDriver) rec(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeDriver) List() ([]browser.SessionInfo, error) { return nil, nil }

func (f *fakeDriver) Navigate(_ context.Context, _, url string, opts ...browser.NavigateOptions) error {
	f.rec("navigate %s", url)
	if len(opts) > 0 {
		f.navOpts = append(f.navOpts, opts[0])
	} else {
		f.navOpts = append(f.navOpts, browser.NavigateOptions{})
	}
	return f.fail["navigate"]
}
func (f *fakeDriver) Click(_ context.Context, _, sel string) error {
	f.rec("click %s", sel)
	return f.fail["click"]
}
func (f *fakeDriver) ClickText(_ context.Context, _, text string) error {
	f.rec("click_text %s", text)
	return f.fail["click_text"]
}
func (f *fakeDriver) ClickXPath(_ context.Context, _, xpath string) error {
	f.rec("click_xpath %s", xpath)
	return f.fail["click_xpath"]
}
func (f *fakeDriver) Type(_ context.Context, _, sel, text string) error {
	f.rec("type %s=%s", sel, text)
	return f.fail["type"]
}
func (f *fakeDriver) Press(_ context.Context, _, key string) error {
	f.rec("press %s", key)
	return f.fail["press"]
}
func (f *fakeDriver) Viewport(_ context.Context, _ string, w, h int) error {
	f.rec("viewport %dx%d", w, h)
	return f.fail["viewport"]
}
func (f *fakeDriver) WaitVisible(_ context.Context, _, sel string) error {
	f.rec("wait %s", sel)
	return f.fail["wait"]
}
func (f *fakeDriver) WaitText(_ context.Context, _, text string) error {
	f.rec("wait_text %s", text)
	return f.fail["wait_text"]
}
func (f *fakeDriver) WaitXPath(_ context.Context, _, xpath string) error {
	f.rec("wait_xpath %s", xpath)
	return f.fail["wait_xpath"]
}
func (f *fakeDriver) Text(_ context.Context, _, sel string) (string, error) {
	f.rec("text %s", sel)
	return f.text[sel], f.fail["text"]
}
func (f *fakeDriver) Eval(_ context.Context, _, js string) (string, error) {
	f.rec("eval %s", js)
	return f.eval[js], f.fail["eval"]
}
func (f *fakeDriver) Fetch(_ context.Context, _, url string) (string, error) {
	f.rec("fetch %s", url)
	return f.fetch, f.fail["fetch"]
}
func (f *fakeDriver) Screenshot(_ context.Context, _, path string) (string, error) {
	artifact, err := f.ScreenshotWithCapture(context.Background(), "", path, true)
	return artifact.Path, err
}
func (f *fakeDriver) ScreenshotWithCapture(_ context.Context, _, path string, captureBeyondViewport bool) (browser.ScreenshotArtifact, error) {
	f.rec("screenshot full_page=%t %s", captureBeyondViewport, path)
	if path == "" {
		path = "/tmp/auto.png"
	}
	height := 1000
	if captureBeyondViewport {
		height = 2400
	}
	return browser.ScreenshotArtifact{Path: path, WidthPX: 1440, HeightPX: height}, f.fail["screenshot"]
}
func (f *fakeDriver) ResolveTarget(_ context.Context, _ string, target string) (string, error) {
	return target, nil
}
func (f *fakeDriver) Console(_ context.Context, _ string, _ browser.ConsoleCaptureOptions) ([]browser.ConsoleEntry, error) {
	f.rec("console")
	return f.console, f.fail["console"]
}
func (f *fakeDriver) Snapshot(_ context.Context, _ string, opts browser.SnapshotOptions) (*browser.PageSnapshot, error) {
	f.rec("snapshot max=%d filter=%s wait=%s settle=%s", opts.Max, opts.Filter, opts.WaitForSelector, opts.Settle)
	if err := f.fail["snapshot"]; err != nil {
		return nil, err
	}
	return &browser.PageSnapshot{
		URL: "https://example.test/", Title: "T", Revision: "sr-0123456789ab", Matched: 1, Returned: 1,
		Counts:   map[string]int{"button": 1},
		Elements: []browser.SnapshotElement{{Ref: "sr-0123456789ab:e1", Role: "button", Tag: "button", Name: "Go", Selector: "button", Visible: true}},
	}, nil
}

// fixedClock returns a now() that advances 1ms per call from a fixed base, so
// run ids and durations are deterministic.
func fixedClock() func() time.Time {
	base := time.Date(2026, 6, 15, 1, 2, 3, 0, time.UTC)
	var n int
	return func() time.Time {
		t := base.Add(time.Duration(n) * time.Millisecond)
		n++
		return t
	}
}

func runPlan(t *testing.T, f *fakeDriver, plan Plan, opts RunOptions) (*RunReport, string) {
	t.Helper()
	// Runner tests exercise both successful and intentionally denied operations.
	// Keep their file telemetry out of the operator's real browser health store.
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	var sink strings.Builder
	opts.Sink = &sink
	if opts.Now == nil {
		opts.Now = fixedClock()
	}
	rep, err := NewRunner(f).Run(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep, sink.String()
}

func TestRunOrderingAndStreaming(t *testing.T) {
	f := newFake()
	f.text["h1"] = "Hello"
	plan := Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbNavigate, URL: "https://x.test"},
		{Verb: VerbWait, Selector: "h1"},
		{Verb: VerbText, Selector: "h1", Capture: "title"},
	}}
	rep, sink := runPlan(t, f, plan, RunOptions{AllowDrive: true})
	if !rep.OK {
		t.Fatalf("expected ok run, got %+v", rep)
	}
	want := []string{"navigate https://x.test", "wait h1", "text h1"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("call order = %v, want %v", f.calls, want)
	}
	// One JSONL line per step, in order.
	lines := nonEmptyLines(sink)
	if len(lines) != 3 {
		t.Fatalf("expected 3 streamed lines, got %d: %q", len(lines), sink)
	}
	var last Result
	if err := json.Unmarshal([]byte(lines[2]), &last); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if last.Captured["title"] != "Hello" {
		t.Fatalf("capture title = %v, want Hello", last.Captured["title"])
	}
}

func TestCaptureInterpolation(t *testing.T) {
	f := newFake()
	f.text[".id"] = "42"
	plan := Plan{Name: "p", Session: "s", Vars: map[string]string{"base": "https://x.test"}, Steps: []Step{
		{Verb: VerbText, Selector: ".id", Capture: "id"},
		{Verb: VerbNavigate, URL: "${base}/item/${id}"},
	}}
	_, _ = runPlan(t, f, plan, RunOptions{AllowDrive: true})
	if got := f.calls[1]; got != "navigate https://x.test/item/42" {
		t.Fatalf("interpolated navigate = %q", got)
	}
}

func TestNavigateOptionsReachDriver(t *testing.T) {
	f := newFake()
	plan := Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbNavigate, URL: "https://x.test/#view", Reload: true, BypassCache: true},
	}}
	rep, _ := runPlan(t, f, plan, RunOptions{AllowDrive: true})
	if !rep.OK {
		t.Fatalf("expected ok run, got %+v", rep.Steps[0])
	}
	if len(f.navOpts) != 1 || !f.navOpts[0].Reload || !f.navOpts[0].BypassCache {
		t.Fatalf("navigate options not passed through: %+v", f.navOpts)
	}
}

func TestAssertTextPassAndFail(t *testing.T) {
	f := newFake()
	f.text[".status"] = "Live now"
	// pass: contains
	rep, _ := runPlan(t, f, Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbAssertText, Selector: ".status", Assert: &Assertion{Contains: "Live"}},
	}}, RunOptions{})
	if !rep.OK || rep.Steps[0].Assert == nil || !rep.Steps[0].Assert.OK {
		t.Fatalf("expected passing assert, got %+v", rep.Steps[0])
	}
	// fail: equals mismatch -> run not ok, stops
	rep2, _ := runPlan(t, newFakeWithText(".status", "Live now"), Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbAssertText, Selector: ".status", Assert: &Assertion{Equals: "Down"}},
		{Verb: VerbNavigate, URL: "https://should-not-run.test"},
	}}, RunOptions{})
	if rep2.OK {
		t.Fatalf("expected failing run")
	}
	if len(rep2.Steps) != 1 {
		t.Fatalf("expected stop after failing assert, ran %d steps", len(rep2.Steps))
	}
}

func TestAssertURLViaEval(t *testing.T) {
	f := newFake()
	f.eval["location.href"] = `"https://x.test/dashboard?token=secret"`
	rep, _ := runPlan(t, f, Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbAssertURL, Assert: &Assertion{Contains: "/dashboard"}},
	}}, RunOptions{})
	if !rep.OK {
		t.Fatalf("expected url assert pass, got %+v", rep.Steps[0])
	}
	if rep.Steps[0].Assert.Actual != "https://x.test/dashboard?token=secret" {
		t.Fatalf("actual url = %q", rep.Steps[0].Assert.Actual)
	}
}

func TestDriveGating(t *testing.T) {
	f := newFake()
	plan := Plan{Name: "p", Session: "s", Steps: []Step{{Verb: VerbClick, Selector: ".btn"}}}
	// Without allow-drive: refused, click never reaches the driver.
	rep, _ := runPlan(t, f, plan, RunOptions{})
	if rep.OK {
		t.Fatalf("expected drive-gated failure")
	}
	if len(f.calls) != 0 {
		t.Fatalf("mutating verb should not reach driver, calls=%v", f.calls)
	}
	if !strings.Contains(rep.Steps[0].Error, "drive-gated") {
		t.Fatalf("error = %q", rep.Steps[0].Error)
	}
	if rep.Steps[0].ErrorClass != browser.ErrorClassPolicy {
		t.Fatalf("drive gate class = %q", rep.Steps[0].ErrorClass)
	}
	if rep.SchemaVersion != browser.BrowserTelemetrySchemaVersion || rep.BuildVersion == "" {
		t.Fatalf("run telemetry contract missing: %+v", rep)
	}
	// With allow-drive: runs.
	f2 := newFake()
	rep2, _ := runPlan(t, f2, plan, RunOptions{AllowDrive: true})
	if !rep2.OK || len(f2.calls) != 1 {
		t.Fatalf("expected click to run with allow-drive, ok=%v calls=%v", rep2.OK, f2.calls)
	}
	// Per-verb allow also works.
	f3 := newFake()
	rep3, _ := runPlan(t, f3, plan, RunOptions{AllowedVerbs: map[Verb]bool{VerbClick: true}})
	if !rep3.OK || len(f3.calls) != 1 {
		t.Fatalf("expected click to run with per-verb allow")
	}
}

func TestAllActuationAndCodeExecutionIsDriveGated(t *testing.T) {
	tests := []Step{
		{Verb: VerbNavigate, URL: "https://x.test"},
		{Verb: VerbEval, JS: "document.body.remove()"},
		{Verb: VerbFetch, URL: "https://x.test"},
		{Verb: VerbViewport, Width: 390, Height: 844},
		{Verb: VerbConsole, Console: &ConsoleArgs{Reload: true}},
	}
	for _, step := range tests {
		t.Run(string(step.Verb), func(t *testing.T) {
			f := newFake()
			rep, _ := runPlan(t, f, Plan{Name: "p", Session: "s", Steps: []Step{step}}, RunOptions{})
			if rep.OK || len(f.calls) != 0 || !strings.Contains(rep.Steps[0].Error, "drive-gated") {
				t.Fatalf("step bypassed drive gate: report=%+v calls=%v", rep.Steps, f.calls)
			}
		})
	}
}

func TestScreenshotHostPathRequiresExplicitAuthority(t *testing.T) {
	step := Step{Verb: VerbScreenshot, Path: "/tmp/caller.png"}
	f := newFake()
	rep, _ := runPlan(t, f, Plan{Name: "p", Session: "s", Steps: []Step{step}}, RunOptions{})
	if rep.OK || len(f.calls) != 0 || !strings.Contains(rep.Steps[0].Error, "allow-host-write") {
		t.Fatalf("host path bypassed gate: report=%+v calls=%v", rep.Steps, f.calls)
	}
	f2 := newFake()
	rep2, _ := runPlan(t, f2, Plan{Name: "p", Session: "s", Steps: []Step{step}}, RunOptions{AllowHostWrite: true})
	if !rep2.OK || len(f2.calls) != 1 {
		t.Fatalf("authorized host path failed: report=%+v calls=%v", rep2.Steps, f2.calls)
	}
}

func TestScreenshotModeDefaultsAndOverrides(t *testing.T) {
	full := newFake()
	rep, _ := runPlan(t, full, Plan{Name: "full", Session: "s", Steps: []Step{{Verb: VerbScreenshot}}}, RunOptions{})
	if !rep.OK || len(full.calls) != 1 || full.calls[0] != "screenshot full_page=true " {
		t.Fatalf("default screenshot mode = report:%+v calls:%v", rep.Steps, full.calls)
	}
	if rep.Steps[0].ScreenshotMode != ScreenshotModeFullPage || rep.Steps[0].ScreenshotWidthPX != 1440 || rep.Steps[0].ScreenshotHeightPX != 2400 {
		t.Fatalf("default screenshot receipt = %+v", rep.Steps[0])
	}

	viewport := newFake()
	rep, _ = runPlan(t, viewport, Plan{Name: "viewport", Session: "s", ScreenshotMode: ScreenshotModeViewport, Steps: []Step{{Verb: VerbScreenshot}}}, RunOptions{})
	if !rep.OK || len(viewport.calls) != 1 || viewport.calls[0] != "screenshot full_page=false " {
		t.Fatalf("plan screenshot mode = report:%+v calls:%v", rep.Steps, viewport.calls)
	}
	if rep.Steps[0].ScreenshotMode != ScreenshotModeViewport || rep.Steps[0].ScreenshotWidthPX != 1440 || rep.Steps[0].ScreenshotHeightPX != 1000 {
		t.Fatalf("viewport screenshot receipt = %+v", rep.Steps[0])
	}

	override := newFake()
	rep, _ = runPlan(t, override, Plan{Name: "override", Session: "s", ScreenshotMode: ScreenshotModeViewport, Steps: []Step{{Verb: VerbScreenshot, ScreenshotMode: ScreenshotModeFullPage}}}, RunOptions{})
	if !rep.OK || len(override.calls) != 1 || override.calls[0] != "screenshot full_page=true " {
		t.Fatalf("step screenshot override = report:%+v calls:%v", rep.Steps, override.calls)
	}
}

func TestClickTextDriveGatedAndRecorded(t *testing.T) {
	// Refused without allow-drive (mutating), no driver call.
	f := newFake()
	plan := Plan{Name: "p", Session: "s", Steps: []Step{{Verb: VerbClickText, Text: "Sign up"}}}
	rep, _ := runPlan(t, f, plan, RunOptions{})
	if rep.OK || len(f.calls) != 0 {
		t.Fatalf("click_text should be drive-gated, ok=%v calls=%v", rep.OK, f.calls)
	}
	// Runs with allow-drive; text is interpolated and passed to the driver.
	f2 := newFake()
	plan2 := Plan{Name: "p", Session: "s", Vars: map[string]string{"label": "Post"}, Steps: []Step{{Verb: VerbClickText, Text: "${label}"}}}
	rep2, _ := runPlan(t, f2, plan2, RunOptions{AllowDrive: true})
	if !rep2.OK || len(f2.calls) != 1 || f2.calls[0] != "click_text Post" {
		t.Fatalf("expected 'click_text Post', ok=%v calls=%v", rep2.OK, f2.calls)
	}
}

func TestClickTextRequiresText(t *testing.T) {
	_, err := NewRunner(newFake()).Run(context.Background(), Plan{Name: "p", Session: "s", Steps: []Step{{Verb: VerbClickText}}}, RunOptions{AllowDrive: true})
	if err == nil {
		t.Fatal("click_text with no text should fail validation")
	}
}

func TestWaitTextReadOnlyAndInterpolated(t *testing.T) {
	// wait_text is NOT mutating: it runs without --allow-drive, and interpolates vars.
	f := newFake()
	plan := Plan{Name: "p", Session: "s", Vars: map[string]string{"who": "Ada"}, Steps: []Step{
		{Verb: VerbWaitText, Text: "post by ${who}"},
	}}
	rep, _ := runPlan(t, f, plan, RunOptions{})
	if !rep.OK || len(f.calls) != 1 || f.calls[0] != "wait_text post by Ada" {
		t.Fatalf("wait_text should run read-only with interpolation, ok=%v calls=%v", rep.OK, f.calls)
	}
}

func TestWaitTextRequiresText(t *testing.T) {
	_, err := NewRunner(newFake()).Run(context.Background(), Plan{Name: "p", Session: "s", Steps: []Step{{Verb: VerbWaitText}}}, RunOptions{})
	if err == nil {
		t.Fatal("wait_text with no text should fail validation")
	}
}

func TestAssertVisibleAndCountAreExplicitReadOnlyAssertions(t *testing.T) {
	f := newFake()
	f.eval[selectorCountJS(`[data-test="modal"]`)] = "0"
	zero := 0
	plan := Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbAssertVisible, Selector: `[data-test="ready"]`},
		{Verb: VerbAssertCount, Selector: `[data-test="modal"]`, Count: &CountAssertion{Equals: &zero}},
	}}
	rep, _ := runPlan(t, f, plan, RunOptions{})
	if !rep.OK {
		t.Fatalf("expected assertions to pass: %+v", rep.Steps)
	}
	want := []string{`wait [data-test="ready"]`, `eval document.querySelectorAll("[data-test=\"modal\"]").length`}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	if rep.Steps[1].Assert == nil || !rep.Steps[1].Assert.OK || rep.Steps[1].Assert.Actual != "0" {
		t.Fatalf("count assert outcome = %+v", rep.Steps[1].Assert)
	}
}

func TestViewportVerbDrivesDeviceMetrics(t *testing.T) {
	f := newFake()
	rep, _ := runPlan(t, f, Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbViewport, Width: 390, Height: 844},
	}}, RunOptions{AllowDrive: true})
	if !rep.OK {
		t.Fatalf("viewport step should pass: %+v", rep.Steps)
	}
	if len(f.calls) != 1 || f.calls[0] != "viewport 390x844" {
		t.Fatalf("calls = %v, want [viewport 390x844]", f.calls)
	}
	if !IsMutating(VerbViewport) {
		t.Fatal("viewport must be drive-gated because it changes page device metrics")
	}
	// validation rejects non-positive dims.
	if (Step{Verb: VerbViewport, Width: 0, Height: 844}).validate() == nil {
		t.Fatal("viewport with width 0 should fail validation")
	}
}

func TestAssertCountFailureStopsRun(t *testing.T) {
	f := newFake()
	f.eval[selectorCountJS(".toast")] = "2"
	zero := 0
	rep, _ := runPlan(t, f, Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbAssertCount, Selector: ".toast", Count: &CountAssertion{Equals: &zero}},
		{Verb: VerbNavigate, URL: "https://should-not-run.test"},
	}}, RunOptions{})
	if rep.OK || len(rep.Steps) != 1 {
		t.Fatalf("expected count assertion to fail and stop, ok=%v steps=%d", rep.OK, len(rep.Steps))
	}
	if rep.Steps[0].Assert == nil || rep.Steps[0].Assert.OK || !strings.Contains(rep.Steps[0].Error, "== 0") {
		t.Fatalf("bad failed assertion: %+v", rep.Steps[0])
	}
}

func TestOnErrorContinue(t *testing.T) {
	f := newFake()
	f.fail["click"] = fmt.Errorf("boom")
	plan := Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbClick, Selector: ".a", OnError: OnErrorContinue},
		{Verb: VerbNavigate, URL: "https://after.test"},
	}}
	rep, _ := runPlan(t, f, plan, RunOptions{AllowDrive: true})
	if rep.OK {
		t.Fatalf("run should be marked not-ok after a failed step")
	}
	if len(rep.Steps) != 2 {
		t.Fatalf("on_error:continue should run both steps, ran %d", len(rep.Steps))
	}
	if rep.Steps[1].OK != true {
		t.Fatalf("second step should have run successfully")
	}
}

func TestSnapshotVerbReadOnlyCapturesInventory(t *testing.T) {
	f := newFake()
	plan := Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbSnapshot, Snapshot: &SnapshotArgs{Max: 25, Filter: "Go", WaitForSelector: "#ready", Settle: Duration(time.Second)}, Capture: "ui"},
	}}
	// No AllowDrive: snapshot is read-only and must still run.
	rep, _ := runPlan(t, f, plan, RunOptions{})
	if !rep.OK {
		t.Fatalf("snapshot should run without --allow-drive: %+v", rep)
	}
	if want := "snapshot max=25 filter=Go wait=#ready settle=1s"; f.calls[len(f.calls)-1] != want {
		t.Fatalf("snapshot call = %q, want %q", f.calls[len(f.calls)-1], want)
	}
	last := rep.Steps[len(rep.Steps)-1]
	captured, _ := last.Captured["ui"].(string)
	if !strings.Contains(captured, `"selector":"button"`) || !strings.Contains(captured, `"role":"button"`) {
		t.Fatalf("captured snapshot missing element fields: %s", captured)
	}
}

func TestSnapshotVerbIsNotMutating(t *testing.T) {
	if IsMutating(VerbSnapshot) {
		t.Fatalf("snapshot must be read-only (not drive-gated)")
	}
}

func TestConsoleFailOnError(t *testing.T) {
	f := newFake()
	f.console = []browser.ConsoleEntry{{Kind: "exception", Level: "error", Text: "ReferenceError: x"}}
	rep, _ := runPlan(t, f, Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbConsole, Console: &ConsoleArgs{Reload: true, FailOnError: true}},
	}}, RunOptions{AllowDrive: true})
	if rep.OK {
		t.Fatalf("expected console fail-on-error to fail the run")
	}
	if !strings.Contains(rep.Steps[0].Error, "exception") {
		t.Fatalf("error = %q", rep.Steps[0].Error)
	}
}

func TestMaxSteps(t *testing.T) {
	f := newFake()
	plan := Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbNavigate, URL: "https://1.test"},
		{Verb: VerbNavigate, URL: "https://2.test"},
		{Verb: VerbNavigate, URL: "https://3.test"},
	}}
	rep, _ := runPlan(t, f, plan, RunOptions{AllowDrive: true, MaxSteps: 2})
	if len(rep.Steps) != 2 || len(f.calls) != 2 {
		t.Fatalf("MaxSteps=2 should run 2 steps, got %d/%d", len(rep.Steps), len(f.calls))
	}
}

func TestRunIDDeterministic(t *testing.T) {
	plan := Plan{Name: "p", Session: "s", Steps: []Step{{Verb: VerbNavigate, URL: "https://x.test"}}}
	r1, _ := runPlan(t, newFake(), plan, RunOptions{Now: fixedClock()})
	r2, _ := runPlan(t, newFake(), plan, RunOptions{Now: fixedClock()})
	if r1.RunID != r2.RunID {
		t.Fatalf("run ids should be deterministic under a fixed clock: %q vs %q", r1.RunID, r2.RunID)
	}
	if r1.PlanHash != r2.PlanHash || !strings.HasPrefix(r1.PlanHash, "sha256:") {
		t.Fatalf("plan hash unstable: %q vs %q", r1.PlanHash, r2.PlanHash)
	}
}

func TestVarsRedactedInReport(t *testing.T) {
	f := newFake()
	plan := Plan{Name: "p", Session: "s", Vars: map[string]string{
		"api_token": "supersecret",
		"page":      "https://x.test/p?token=leak#frag",
	}, Steps: []Step{{Verb: VerbNavigate, URL: "${page}"}}}
	rep, _ := runPlan(t, f, plan, RunOptions{})
	if rep.Vars["api_token"] != redactedMark {
		t.Fatalf("secret var not redacted: %q", rep.Vars["api_token"])
	}
	if strings.Contains(rep.Vars["page"], "token=leak") || strings.Contains(rep.Vars["page"], "frag") {
		t.Fatalf("url query/fragment not stripped: %q", rep.Vars["page"])
	}
}

func TestTelemetryHookFires(t *testing.T) {
	f := newFake()
	tel := &captureTelemetry{}
	plan := Plan{Name: "p", Session: "s", Steps: []Step{
		{Verb: VerbNavigate, URL: "https://x.test"},
		{Verb: VerbScreenshot, Path: "shot.png"},
	}}
	_, _ = runPlan(t, f, plan, RunOptions{AllowDrive: true, AllowHostWrite: true, Telemetry: tel})
	if tel.begins != 1 || tel.steps != 2 || tel.finishes != 1 {
		t.Fatalf("telemetry begins=%d steps=%d finishes=%d, want 1/2/1", tel.begins, tel.steps, tel.finishes)
	}
}

func TestValidateRejectsBadPlan(t *testing.T) {
	_, err := NewRunner(newFake()).Run(context.Background(), Plan{Name: "p", Session: "", Steps: nil}, RunOptions{})
	if err == nil {
		t.Fatal("expected validation error for empty session/steps")
	}
}

// helpers

type captureTelemetry struct{ begins, steps, finishes int }

func (c *captureTelemetry) Begin(_ *RunReport)          { c.begins++ }
func (c *captureTelemetry) Step(_ *RunReport, _ Result) { c.steps++ }
func (c *captureTelemetry) Finish(_ *RunReport)         { c.finishes++ }

func newFakeWithText(sel, val string) *fakeDriver {
	f := newFake()
	f.text[sel] = val
	return f
}

func nonEmptyLines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			out = append(out, sc.Text())
		}
	}
	return out
}
