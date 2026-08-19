package browserscript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/snapref/contract"
)

func TestExampleOpenAndSnapshotLoads(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "examples", "open-and-snapshot.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadYAML(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Name != "open-and-snapshot" || len(p.Steps) != 4 {
		t.Fatalf("plan = %+v", p)
	}
}

func TestLoadYAMLEqualsJSON(t *testing.T) {
	yamlSrc := []byte(`
name: capture
session: leads
vars:
  base: https://x.test
steps:
  - verb: navigate
    url: ${base}/p/1
  - verb: wait
    selector: h1
  - verb: text
    selector: h1.name
    capture: name
  - verb: assert_text
    selector: .status
    assert:
      contains: Live
  - verb: type
    selector: "#q"
    text: hi
    timeout: 5s
    on_error: continue
`)
	p, err := LoadYAML(yamlSrc)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if p.Name != "capture" || p.Session != "leads" || len(p.Steps) != 5 {
		t.Fatalf("plan shape wrong: %+v", p)
	}
	if p.Steps[0].URL != "${base}/p/1" {
		t.Fatalf("navigate url = %q", p.Steps[0].URL)
	}
	if p.Steps[2].Capture != "name" {
		t.Fatalf("capture not parsed: %+v", p.Steps[2])
	}
	if p.Steps[3].Assert == nil || p.Steps[3].Assert.Contains != "Live" {
		t.Fatalf("assert not parsed: %+v", p.Steps[3])
	}
	if p.Steps[4].Timeout.D().String() != "5s" {
		t.Fatalf("duration not parsed: %v", p.Steps[4].Timeout.D())
	}
	if p.Steps[4].OnError != OnErrorContinue {
		t.Fatalf("on_error not parsed: %q", p.Steps[4].OnError)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestLoadYAMLScreenshotModes(t *testing.T) {
	p, err := LoadYAML([]byte(`
name: screenshots
session: s
screenshot_mode: viewport
steps:
  - verb: screenshot
    path: fold.png
  - verb: screenshot
    path: archive.png
    screenshot_mode: full-page
`))
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if p.ScreenshotMode != ScreenshotModeViewport || p.Steps[1].ScreenshotMode != ScreenshotModeFullPage {
		t.Fatalf("screenshot modes not parsed: plan=%q step=%q", p.ScreenshotMode, p.Steps[1].ScreenshotMode)
	}
}

func TestLoadYAMLTextTargetingVerbs(t *testing.T) {
	// click_text / wait_text round-trip through the YAML loader (text field).
	p, err := LoadYAML([]byte(`
name: text-verbs
session: s
steps:
  - verb: wait_text
    text: "Welcome back"
  - verb: click_text
    text: "Sign up"
`))
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if p.Steps[0].Verb != VerbWaitText || p.Steps[0].Text != "Welcome back" {
		t.Fatalf("wait_text not parsed: %+v", p.Steps[0])
	}
	if p.Steps[1].Verb != VerbClickText || p.Steps[1].Text != "Sign up" {
		t.Fatalf("click_text not parsed: %+v", p.Steps[1])
	}
	if !IsMutating(VerbClickText) || IsMutating(VerbWaitText) {
		t.Fatalf("click_text must be drive-gated, wait_text must not be")
	}
}

func TestAllVerbsComeFromTypedContract(t *testing.T) {
	operations := browsercontract.ScriptOperations()
	if len(AllVerbs) != len(operations) {
		t.Fatalf("AllVerbs=%d registry=%d", len(AllVerbs), len(operations))
	}
	for i, operation := range operations {
		if AllVerbs[i] != Verb(operation.ScriptVerb) {
			t.Fatalf("verb %d = %q, registry %q", i, AllVerbs[i], operation.ScriptVerb)
		}
	}
}

func TestLoadYAMLNavigationAndCountAssertions(t *testing.T) {
	p, err := LoadYAML([]byte(`
name: explicit-asserts
session: s
steps:
  - verb: navigate
    url: https://x.test/#search
    reload: true
    bypass_cache: true
  - verb: assert_visible
    selector: '[data-test="view-search"]'
  - verb: assert_count
    selector: '[data-test="command-palette"]'
    count:
      equals: 0
`))
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !p.Steps[0].Reload || !p.Steps[0].BypassCache {
		t.Fatalf("navigate options not parsed: %+v", p.Steps[0])
	}
	if p.Steps[2].Count == nil || p.Steps[2].Count.Equals == nil || *p.Steps[2].Count.Equals != 0 {
		t.Fatalf("count assertion not parsed: %+v", p.Steps[2])
	}
}

func TestXPathVerbs(t *testing.T) {
	f := newFake()
	plan := Plan{Name: "x", Session: "s", Steps: []Step{
		{Verb: VerbWaitXPath, Selector: "//article[contains(.,'X')]"},
		{Verb: VerbClickXPath, Selector: "//button[@id='go']"},
	}}
	rep, _ := runPlan(t, f, plan, RunOptions{AllowDrive: true})
	if !rep.OK || len(f.calls) != 2 {
		t.Fatalf("xpath verbs should run, ok=%v calls=%v", rep.OK, f.calls)
	}
	if f.calls[0] != "wait_xpath //article[contains(.,'X')]" || f.calls[1] != "click_xpath //button[@id='go']" {
		t.Fatalf("xpath verbs mis-dispatched: %v", f.calls)
	}
	// click_xpath is mutating (gated); wait_xpath is read-only.
	if !IsMutating(VerbClickXPath) || IsMutating(VerbWaitXPath) {
		t.Fatalf("click_xpath must be gated, wait_xpath must not be")
	}
	f2 := newFake()
	rep2, _ := runPlan(t, f2, Plan{Name: "x", Session: "s", Steps: []Step{{Verb: VerbClickXPath, Selector: "//b"}}}, RunOptions{})
	if rep2.OK || len(f2.calls) != 0 {
		t.Fatalf("click_xpath should be drive-gated without --allow-drive")
	}
}

func TestLoadJSONRejectsUnknownField(t *testing.T) {
	_, err := LoadJSON([]byte(`{"name":"p","session":"s","steps":[{"verb":"wait","selctor":"h1"}]}`))
	if err == nil || !strings.Contains(err.Error(), "selctor") {
		t.Fatalf("expected unknown-field error mentioning the typo, got %v", err)
	}
}

func TestLoadFileMergesVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, []byte("name: p\nsession: s\nvars:\n  a: '1'\nsteps:\n  - verb: navigate\n    url: ${a}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFile(path, map[string]string{"a": "override", "b": "2"})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if p.Vars["a"] != "override" || p.Vars["b"] != "2" {
		t.Fatalf("vars merge wrong: %v", p.Vars)
	}
}

func TestStarlarkEagerBranchingAndEmit(t *testing.T) {
	f := newFake()
	f.text["h1.name"] = "Ada"
	f.eval["location.href"] = `"https://x.test/dashboard"`
	src := []byte(`
nav(params["base"] + "/p/1")
wait("h1")
name = text("h1.name")
if name == "Ada":
    emit({"who": name, "n": 1})
assert_url(contains="/dashboard")
`)
	var sink strings.Builder
	rep, err := RunStarlark(context.Background(), f, src, "flow.star", RunOptions{
		Session: "s", Vars: map[string]string{"base": "https://x.test"}, Sink: &sink, Now: fixedClock(), AllowDrive: true,
	})
	if err != nil {
		t.Fatalf("RunStarlark: %v", err)
	}
	if !rep.OK {
		t.Fatalf("expected ok run, got %+v", rep)
	}
	wantCalls := []string{"navigate https://x.test/p/1", "wait h1", "text h1.name", "eval location.href"}
	if strings.Join(f.calls, "|") != strings.Join(wantCalls, "|") {
		t.Fatalf("calls = %v, want %v", f.calls, wantCalls)
	}
	if len(rep.Outputs) != 1 || rep.Outputs[0]["who"] != "Ada" {
		t.Fatalf("emit output wrong: %+v", rep.Outputs)
	}
	if rep.Source != SourceStar {
		t.Fatalf("source = %q", rep.Source)
	}
}

func TestStarlarkAssertCountAndNavigationOptions(t *testing.T) {
	f := newFake()
	f.eval[selectorCountJS(".modal")] = "0"
	src := []byte(`
nav("https://x.test/#modal", reload=True, bypass_cache=True)
assert_visible("main")
assert_count(".modal", equals=0)
`)
	rep, err := RunStarlark(context.Background(), f, src, "flow.star", RunOptions{Session: "s", Now: fixedClock(), AllowDrive: true})
	if err != nil {
		t.Fatalf("RunStarlark: %v", err)
	}
	if !rep.OK {
		t.Fatalf("expected ok run, got %+v", rep.Steps)
	}
	if len(f.navOpts) != 1 || !f.navOpts[0].Reload || !f.navOpts[0].BypassCache {
		t.Fatalf("nav opts not passed through: %+v", f.navOpts)
	}
	if got := strings.Join(f.calls, "|"); !strings.Contains(got, "wait main") || !strings.Contains(got, selectorCountJS(".modal")) {
		t.Fatalf("calls missing assertions: %v", f.calls)
	}
}

func TestStarlarkDriveGatingHalts(t *testing.T) {
	f := newFake()
	src := []byte(`
click(".danger")
nav("https://should-not-run.test")
`)
	rep, err := RunStarlark(context.Background(), f, src, "flow.star", RunOptions{Session: "s", Now: fixedClock()})
	if err == nil {
		t.Fatal("expected drive-gated error to halt the script")
	}
	if rep.OK {
		t.Fatal("report should be not-ok")
	}
	if len(f.calls) != 0 {
		t.Fatalf("no driver calls expected (click gated, nav never reached), got %v", f.calls)
	}
}

func TestStarlarkRequiresSession(t *testing.T) {
	_, err := RunStarlark(context.Background(), newFake(), []byte(`nav("x")`), "f.star", RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("expected session-required error, got %v", err)
	}
}

func TestStarlarkValidatesSteps(t *testing.T) {
	// assert_text with no condition is a no-op in raw evalAssertion; the eager
	// path must reject it (parity with Plan.Validate) instead of vacuously passing.
	f := newFake()
	f.text["h1"] = "x"
	rep, err := RunStarlark(context.Background(), f, []byte(`assert_text("h1")`), "f.star", RunOptions{Session: "s", Now: fixedClock()})
	if err == nil {
		t.Fatal("expected validation error for assert_text with no condition")
	}
	if rep.OK {
		t.Fatal("run should be not-ok")
	}
	// empty navigate url is also rejected before hitting the driver.
	f2 := newFake()
	_, err = RunStarlark(context.Background(), f2, []byte(`nav("")`), "f.star", RunOptions{Session: "s", Now: fixedClock()})
	if err == nil || len(f2.calls) != 0 {
		t.Fatalf("expected nav(\"\") rejected before driver, err=%v calls=%v", err, f2.calls)
	}
}

func TestParseStarlarkSyntax(t *testing.T) {
	if err := ParseStarlark([]byte("nav(\"x\")\n"), "ok.star"); err != nil {
		t.Fatalf("valid script should parse: %v", err)
	}
	if err := ParseStarlark([]byte("def (:\n"), "bad.star"); err == nil {
		t.Fatal("invalid script should fail to parse")
	}
}
