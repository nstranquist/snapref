package browsermcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/pageskein/browser"
	"github.com/nstranquist/pageskein/contract"
)

type fakeDriver struct {
	navigated       []string
	navOpts         []browser.NavigateOptions
	fetched         []string
	failNavigate    bool
	consoleCalls    []browser.ConsoleCaptureOptions
	consoleReturn   []browser.ConsoleEntry
	failConsole     bool
	snapshotCalls   []browser.SnapshotOptions
	failSnapshot    bool
	screenshotPaths []string
}

func (f *fakeDriver) List() ([]SessionInfo, error) {
	return []SessionInfo{{Name: "a", Port: 9222, Alive: true}}, nil
}
func (f *fakeDriver) Navigate(_ context.Context, session, url string, opts ...browser.NavigateOptions) error {
	if f.failNavigate {
		return fmt.Errorf("nav boom")
	}
	f.navigated = append(f.navigated, session+"|"+url)
	if len(opts) > 0 {
		f.navOpts = append(f.navOpts, opts[0])
	} else {
		f.navOpts = append(f.navOpts, browser.NavigateOptions{})
	}
	return nil
}
func (f *fakeDriver) Click(_ context.Context, _, _ string) error          { return nil }
func (f *fakeDriver) ClickText(_ context.Context, _, _ string) error      { return nil }
func (f *fakeDriver) ClickXPath(_ context.Context, _, _ string) error     { return nil }
func (f *fakeDriver) Type(_ context.Context, _, _, _ string) error        { return nil }
func (f *fakeDriver) Press(_ context.Context, _, _ string) error          { return nil }
func (f *fakeDriver) WaitVisible(_ context.Context, _, _ string) error    { return nil }
func (f *fakeDriver) WaitText(_ context.Context, _, _ string) error       { return nil }
func (f *fakeDriver) WaitXPath(_ context.Context, _, _ string) error      { return nil }
func (f *fakeDriver) Text(_ context.Context, _, _ string) (string, error) { return "hello", nil }
func (f *fakeDriver) Eval(_ context.Context, _, _ string) (string, error) { return `"evaled"`, nil }
func (f *fakeDriver) Fetch(_ context.Context, _, url string) (string, error) {
	f.fetched = append(f.fetched, url)
	return "<html>" + url + "</html>", nil
}
func (f *fakeDriver) Screenshot(_ context.Context, _, path string) (string, error) {
	f.screenshotPaths = append(f.screenshotPaths, path)
	if path != "" {
		return path, nil
	}
	return "/tmp/x.png", nil
}
func (f *fakeDriver) ResolveTarget(_ context.Context, _ string, target string) (string, error) {
	return target, nil
}
func (f *fakeDriver) Console(_ context.Context, _ string, opts browser.ConsoleCaptureOptions) ([]browser.ConsoleEntry, error) {
	if f.failConsole {
		return nil, fmt.Errorf("console boom")
	}
	f.consoleCalls = append(f.consoleCalls, opts)
	return f.consoleReturn, nil
}
func (f *fakeDriver) Snapshot(_ context.Context, _ string, opts browser.SnapshotOptions) (*browser.PageSnapshot, error) {
	if f.failSnapshot {
		return nil, fmt.Errorf("snapshot boom")
	}
	f.snapshotCalls = append(f.snapshotCalls, opts)
	return &browser.PageSnapshot{
		URL: "https://example.test/app", Title: "App", Revision: "sr-0123456789ab",
		Matched: 2, Returned: 2, Counts: map[string]int{"button": 1, "link": 1},
		Elements: []browser.SnapshotElement{
			{Ref: "sr-0123456789ab:e1", Role: "button", Tag: "button", Name: "Sign in", Selector: "button[type=submit]", Visible: true},
			{Ref: "sr-0123456789ab:e2", Role: "link", Tag: "a", Name: "Home", Selector: "#home", Href: "/", Visible: true},
		},
	}, nil
}
func (f *fakeDriver) Viewport(_ context.Context, _ string, _, _ int) error { return nil }

func newTestServer(d Driver, allowDrive bool, audit *bytes.Buffer) *server {
	opts := Options{
		Driver: d, AllowDrive: allowDrive,
		Now: func() time.Time { return time.Unix(0, 0).UTC() },
	}
	// Avoid assigning a typed-nil *bytes.Buffer into the io.Writer field (it would
	// read as non-nil and panic on Write) — mirror browser_mcp.go's guard.
	if audit != nil {
		opts.Audit = audit
	}
	return &server{opts: opts}
}

func call(s *server, name string, args map[string]any) (string, bool) {
	p, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	return s.callTool(p)
}

func TestToolsListExposesReadAndDriveTools(t *testing.T) {
	s := newTestServer(&fakeDriver{}, false, nil)
	tools := s.tools()
	names := map[string]bool{}
	for _, tl := range tools {
		name, ok := tl["name"].(string)
		if !ok {
			t.Fatalf("tools/list name has type %T, want string", tl["name"])
		}
		names[name] = true
	}
	for _, want := range []string{
		"browser_list", "browser_text", "browser_screenshot", "browser_wait", "browser_snapshot",
		"browser_navigate", "browser_click", "browser_type", "browser_press",
		"browser_eval", "browser_fetch", "browser_console",
	} {
		if !names[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}
}

func TestToolsListMatchesTypedRegistry(t *testing.T) {
	s := newTestServer(&fakeDriver{}, false, nil)
	got := map[string]bool{}
	for _, tool := range s.tools() {
		name, ok := tool["name"].(string)
		if !ok {
			t.Fatalf("tools/list name has type %T, want string", tool["name"])
		}
		got[name] = true
	}
	for _, name := range browsercontract.MCPTools() {
		if !got[name] {
			t.Errorf("typed registry MCP tool %q is missing from tools/list", name)
		}
		delete(got, name)
	}
	if len(got) != 0 {
		t.Fatalf("tools/list contains unregistered MCP tools: %+v", got)
	}
}

func TestPassiveConsoleIsReadOnlyButTriggeredConsoleRequiresDrive(t *testing.T) {
	fd := &fakeDriver{}
	s := newTestServer(fd, false, nil)
	if out, isErr := call(s, "browser_console", map[string]any{"session": "a"}); isErr {
		t.Fatalf("passive console should be read-only, got %q", out)
	}
	if out, isErr := call(s, "browser_console", map[string]any{"session": "a", "reload": true}); !isErr || !strings.Contains(out, "disabled") {
		t.Fatalf("triggered console should require drive, got %q isErr=%v", out, isErr)
	}
}

func TestSelectorToolsAcceptExactlyOneSelectorOrExecutableRef(t *testing.T) {
	s := newTestServer(&fakeDriver{}, true, nil)
	if out, isErr := call(s, "browser_click", map[string]any{"session": "a", "ref": "sr-0123456789ab:e1"}); isErr {
		t.Fatalf("executable ref should be accepted, got %q", out)
	}
	out, isErr := call(s, "browser_click", map[string]any{
		"session": "a", "selector": "#x", "ref": "sr-0123456789ab:e1",
	})
	if !isErr || !strings.Contains(out, "exactly one") {
		t.Fatalf("selector+ref should be rejected, got %q isErr=%v", out, isErr)
	}
	out, isErr = call(s, "browser_click", map[string]any{"session": "a", "ref": "sr-button"})
	if !isErr || !strings.Contains(out, "malformed") {
		t.Fatalf("explicit malformed ref should be rejected, got %q isErr=%v", out, isErr)
	}
	if out, isErr = call(s, "browser_click", map[string]any{"session": "a", "selector": "sr-button"}); isErr {
		t.Fatalf("sr-prefixed CSS selector should remain compatible, got %q", out)
	}
}

func TestSnapshotIsReadOnlyAndUngated(t *testing.T) {
	// Snapshot is a read tool: it must run even with driving disabled.
	fd := &fakeDriver{}
	off := newTestServer(fd, false, nil)
	out, isErr := call(off, "browser_snapshot", map[string]any{
		"session": "a", "max": float64(50), "role": "button,link",
		"wait_for_selector": "#ready", "settle": "750ms",
	})
	if isErr {
		t.Fatalf("browser_snapshot should be ungated; got error: %s", out)
	}
	if !strings.Contains(out, `"selector": "button[type=submit]"`) || !strings.Contains(out, `"role": "button"`) {
		t.Fatalf("snapshot payload missing element fields: %s", out)
	}
	if len(fd.snapshotCalls) != 1 {
		t.Fatalf("expected 1 snapshot call, got %d", len(fd.snapshotCalls))
	}
	if got := fd.snapshotCalls[0]; got.Max != 50 || len(got.Roles) != 2 || got.WaitForSelector != "#ready" || got.Settle != 750*time.Millisecond {
		t.Fatalf("snapshot options not parsed: %+v", got)
	}
}

func TestSnapshotRejectsInvalidSettle(t *testing.T) {
	fd := &fakeDriver{}
	s := newTestServer(fd, false, nil)
	out, isErr := call(s, "browser_snapshot", map[string]any{"session": "a", "settle": "eventually"})
	if !isErr || !strings.Contains(out, "bad settle") || len(fd.snapshotCalls) != 0 {
		t.Fatalf("invalid settle = isErr:%v out:%q calls:%d", isErr, out, len(fd.snapshotCalls))
	}
}

func TestSnapshotErrorSurfaces(t *testing.T) {
	fd := &fakeDriver{failSnapshot: true}
	s := newTestServer(fd, false, nil)
	if out, isErr := call(s, "browser_snapshot", map[string]any{"session": "a"}); !isErr || !strings.Contains(out, "boom") {
		t.Fatalf("expected snapshot error surfaced, got isErr=%v out=%s", isErr, out)
	}
}

func TestScreenshotManagedTempIsUngated(t *testing.T) {
	fd := &fakeDriver{}
	s := newTestServer(fd, false, nil)
	out, isErr := call(s, "browser_screenshot", map[string]any{"session": "a"})
	if isErr || !strings.Contains(out, "/tmp/x.png") {
		t.Fatalf("managed screenshot should be ungated, got %q isErr=%v", out, isErr)
	}
	if len(fd.screenshotPaths) != 1 || fd.screenshotPaths[0] != "" {
		t.Fatalf("managed screenshot must not select a host path: %+v", fd.screenshotPaths)
	}
}

func TestScreenshotCallerPathRequiresHostWriteAuthority(t *testing.T) {
	fd := &fakeDriver{}
	var audit bytes.Buffer
	s := newTestServer(fd, false, &audit)
	out, isErr := call(s, "browser_screenshot", map[string]any{"session": "a", "path": "/tmp/caller.png"})
	if !isErr || !strings.Contains(out, "host writes") {
		t.Fatalf("caller path should be refused, got %q isErr=%v", out, isErr)
	}
	if len(fd.screenshotPaths) != 0 {
		t.Fatalf("refused host write reached driver: %+v", fd.screenshotPaths)
	}
	if !strings.Contains(audit.String(), "host write disabled") {
		t.Fatalf("refused host write was not audited: %s", audit.String())
	}
}

func TestScreenshotCallerPathRunsWithHostWriteAuthority(t *testing.T) {
	fd := &fakeDriver{}
	var audit bytes.Buffer
	s := &server{opts: Options{
		Driver: fd, AllowHostWrite: true, Audit: &audit,
		Now: func() time.Time { return time.Unix(0, 0).UTC() },
	}}
	out, isErr := call(s, "browser_screenshot", map[string]any{"session": "a", "path": "/tmp/caller.png"})
	if isErr || !strings.Contains(out, "/tmp/caller.png") {
		t.Fatalf("authorized host write failed, got %q isErr=%v", out, isErr)
	}
	if len(fd.screenshotPaths) != 1 || fd.screenshotPaths[0] != "/tmp/caller.png" {
		t.Fatalf("authorized path not passed to driver: %+v", fd.screenshotPaths)
	}
	if !strings.Contains(audit.String(), `"allowed":true`) {
		t.Fatalf("authorized host write was not audited: %s", audit.String())
	}
}

func TestConsoleIsGatedAndDrivesWhenAllowed(t *testing.T) {
	// Gated off by default.
	fd := &fakeDriver{}
	off := newTestServer(fd, false, nil)
	if out, isErr := call(off, "browser_console", map[string]any{"session": "a", "navigate": "https://x/"}); !isErr || !strings.Contains(out, "disabled") {
		t.Fatalf("console should be gated off, got %q isErr=%v", out, isErr)
	}
	if len(fd.consoleCalls) != 0 {
		t.Fatalf("driver should not have been called while gated off: %+v", fd.consoleCalls)
	}

	// With the gate on, it captures and returns the JSON payload.
	fd2 := &fakeDriver{consoleReturn: []browser.ConsoleEntry{
		{Kind: "console", Level: "log", Text: "ok"},
		{Kind: "exception", Level: "error", Text: "TypeError: boom"},
		{Kind: "log", Level: "warning", Text: "deprecated"},
	}}
	on := newTestServer(fd2, true, nil)
	out, isErr := call(on, "browser_console", map[string]any{"session": "a", "reload": true, "kind": "js", "level": "error"})
	if isErr {
		t.Fatalf("console(gate on) unexpected error: %q", out)
	}
	var got struct {
		Count    int `json:"count"`
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("console payload not JSON: %v\n%s", err, out)
	}
	if got.Errors != 1 || got.Warnings != 1 || got.Count != 3 {
		t.Fatalf("counts wrong: %+v\n%s", got, out)
	}
	if len(fd2.consoleCalls) != 1 || !fd2.consoleCalls[0].Reload || fd2.consoleCalls[0].Kind != "js" || fd2.consoleCalls[0].Level != "error" {
		t.Fatalf("driver received wrong opts: %+v", fd2.consoleCalls)
	}
}

func TestConsoleFailOnErrorSurfacesAsError(t *testing.T) {
	fd := &fakeDriver{consoleReturn: []browser.ConsoleEntry{{Kind: "exception", Level: "error", Text: "boom"}}}
	s := newTestServer(fd, true, nil)
	out, isErr := call(s, "browser_console", map[string]any{"session": "a", "navigate": "https://x/", "failOnError": true})
	if !isErr {
		t.Fatalf("failOnError with an error entry should surface isError=true, got %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("error payload should still carry the entries: %q", out)
	}
	// No errors + failOnError → not an error result.
	fd2 := &fakeDriver{consoleReturn: []browser.ConsoleEntry{{Kind: "console", Level: "log", Text: "fine"}}}
	s2 := newTestServer(fd2, true, nil)
	if _, isErr := call(s2, "browser_console", map[string]any{"session": "a", "reload": true, "failOnError": true}); isErr {
		t.Fatalf("failOnError with zero errors should NOT be an error result")
	}
}

func TestConsoleRejectsConflictingTriggersAndBadArgs(t *testing.T) {
	s := newTestServer(&fakeDriver{}, true, nil)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"navigate+reload", map[string]any{"session": "a", "navigate": "https://x/", "reload": true}, "at most one"},
		{"navigate+eval", map[string]any{"session": "a", "navigate": "https://x/", "eval": "1"}, "at most one"},
		{"bad level", map[string]any{"session": "a", "reload": true, "level": "loud"}, "level must be"},
		{"bad kind", map[string]any{"session": "a", "reload": true, "kind": "weird"}, "kind must be"},
		{"bad duration", map[string]any{"session": "a", "reload": true, "duration": "soon"}, "invalid duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, isErr := call(s, "browser_console", tc.args)
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("want error containing %q, got %q isErr=%v", tc.want, out, isErr)
			}
		})
	}
}

func TestConsoleNavigateRespectsAllowlistAndAudits(t *testing.T) {
	fd := &fakeDriver{consoleReturn: []browser.ConsoleEntry{{Kind: "console", Level: "error", Text: "x"}}}
	var audit bytes.Buffer
	s := &server{opts: Options{
		Driver: fd, AllowDrive: true, AllowOrigins: []string{"example.com"},
		Audit: &audit, Now: func() time.Time { return time.Unix(0, 0).UTC() },
	}}
	// Blocked origin: driver not called, refusal audited.
	out, isErr := call(s, "browser_console", map[string]any{"session": "a", "navigate": "https://evil.test/x"})
	if !isErr || !strings.Contains(out, "not in the navigation allowlist") {
		t.Fatalf("evil.test should be blocked, got %q isErr=%v", out, isErr)
	}
	if len(fd.consoleCalls) != 0 {
		t.Fatalf("blocked navigate should not reach the driver: %+v", fd.consoleCalls)
	}
	if !strings.Contains(audit.String(), "origin not allowlisted") {
		t.Fatalf("blocked origin not audited: %s", audit.String())
	}
	// Allowed origin: captures and audits success with the error count.
	audit.Reset()
	if _, isErr := call(s, "browser_console", map[string]any{"session": "a", "navigate": "https://www.example.com/app"}); isErr {
		t.Fatalf("example.com should be allowed")
	}
	if !strings.Contains(audit.String(), `"allowed":true`) || !strings.Contains(audit.String(), "errors=1") {
		t.Fatalf("success not audited with detail: %s", audit.String())
	}
}

func TestConsoleDriverErrorIsAudited(t *testing.T) {
	fd := &fakeDriver{failConsole: true}
	var audit bytes.Buffer
	s := newTestServer(fd, true, &audit)
	out, isErr := call(s, "browser_console", map[string]any{"session": "a", "reload": true})
	if !isErr || !strings.Contains(out, "console boom") {
		t.Fatalf("driver error should surface, got %q isErr=%v", out, isErr)
	}
	if !strings.Contains(audit.String(), `"ok":false`) {
		t.Fatalf("driver failure not audited: %s", audit.String())
	}
}

func TestWaitIsReadOnlyAndEvalIsGated(t *testing.T) {
	// browser_wait runs without the gate (observation).
	s := newTestServer(&fakeDriver{}, false, nil)
	if out, isErr := call(s, "browser_wait", map[string]any{"session": "a", "selector": "#x"}); isErr {
		t.Fatalf("wait should be read-only, got error %q", out)
	}
	// browser_eval is gated off by default.
	if out, isErr := call(s, "browser_eval", map[string]any{"session": "a", "js": "1+1"}); !isErr || !strings.Contains(out, "disabled") {
		t.Fatalf("eval should be gated off, got %q isErr=%v", out, isErr)
	}
	// With the gate on, eval returns its result.
	son := newTestServer(&fakeDriver{}, true, nil)
	if out, isErr := call(son, "browser_eval", map[string]any{"session": "a", "js": "1+1"}); isErr || out != `"evaled"` {
		t.Fatalf("eval(gate on) = %q isErr=%v", out, isErr)
	}
}

func TestPerVerbAllowToolsGate(t *testing.T) {
	fd := &fakeDriver{}
	// Only navigate is individually enabled; full AllowDrive is off.
	s := &server{opts: Options{
		Driver:       fd,
		AllowedTools: map[string]bool{"browser_navigate": true},
		Now:          func() time.Time { return time.Unix(0, 0).UTC() },
	}}
	if _, isErr := call(s, "browser_navigate", map[string]any{"session": "a", "url": "https://x/"}); isErr {
		t.Errorf("navigate should be allowed via --allow-tools")
	}
	if out, isErr := call(s, "browser_click", map[string]any{"session": "a", "selector": "#x"}); !isErr || !strings.Contains(out, "disabled") {
		t.Errorf("click should be refused (not in allow-tools), got %q isErr=%v", out, isErr)
	}
	if _, isErr := call(s, "browser_list", nil); isErr {
		t.Errorf("read tools must always work regardless of the gate")
	}
}

func TestNavigationAllowlist(t *testing.T) {
	fd := &fakeDriver{}
	var audit bytes.Buffer
	s := &server{opts: Options{
		Driver: fd, AllowDrive: true, AllowOrigins: []string{"example.com", "https://localhost:7777"},
		Audit: &audit, Now: func() time.Time { return time.Unix(0, 0).UTC() },
	}}
	// Allowed host (and subdomain) pass.
	if _, isErr := call(s, "browser_navigate", map[string]any{"session": "a", "url": "https://www.example.com/page"}); isErr {
		t.Errorf("www.example.com should be allowed")
	}
	// Disallowed host is blocked and audited; the driver is not called for it.
	out, isErr := call(s, "browser_navigate", map[string]any{"session": "a", "url": "https://evil.test/x"})
	if !isErr || !strings.Contains(out, "not in the navigation allowlist") {
		t.Fatalf("evil.test should be blocked, got %q isErr=%v", out, isErr)
	}
	for _, n := range fd.navigated {
		if strings.Contains(n, "evil.test") {
			t.Errorf("driver navigated to a blocked origin: %v", fd.navigated)
		}
	}
	if !strings.Contains(audit.String(), "origin not allowlisted") {
		t.Errorf("blocked origin not audited: %s", audit.String())
	}
}

func TestReadToolsRunWithoutGate(t *testing.T) {
	s := newTestServer(&fakeDriver{}, false, nil)
	out, isErr := call(s, "browser_list", nil)
	if isErr || !strings.Contains(out, "9222") {
		t.Fatalf("browser_list = %q isErr=%v", out, isErr)
	}
	if out, isErr := call(s, "browser_text", map[string]any{"session": "a", "selector": "h1"}); isErr || out != "hello" {
		t.Fatalf("browser_text = %q isErr=%v", out, isErr)
	}
}

func TestDriveRefusedWhenGateOff(t *testing.T) {
	fd := &fakeDriver{}
	var audit bytes.Buffer
	s := newTestServer(fd, false, &audit)
	out, isErr := call(s, "browser_navigate", map[string]any{"session": "a", "url": "https://x"})
	if !isErr || !strings.Contains(out, "disabled") {
		t.Fatalf("expected refusal, got %q isErr=%v", out, isErr)
	}
	if len(fd.navigated) != 0 {
		t.Fatalf("driver should NOT have been called: %v", fd.navigated)
	}
	if !strings.Contains(audit.String(), `"allowed":false`) {
		t.Errorf("refusal not audited: %s", audit.String())
	}
}

func TestDriveRunsAndAuditsWhenGateOn(t *testing.T) {
	fd := &fakeDriver{}
	var audit bytes.Buffer
	s := newTestServer(fd, true, &audit)
	out, isErr := call(s, "browser_navigate", map[string]any{"session": "a", "url": "https://example.com/", "reload": true, "bypass_cache": true})
	if isErr || !strings.Contains(out, "ok:") {
		t.Fatalf("navigate = %q isErr=%v", out, isErr)
	}
	if len(fd.navigated) != 1 || fd.navigated[0] != "a|https://example.com/" {
		t.Fatalf("driver navigate calls = %v", fd.navigated)
	}
	if len(fd.navOpts) != 1 || !fd.navOpts[0].Reload || !fd.navOpts[0].BypassCache {
		t.Fatalf("driver navigate opts = %+v", fd.navOpts)
	}
	if !strings.Contains(audit.String(), `"allowed":true`) || !strings.Contains(audit.String(), `"ok":true`) || !strings.Contains(audit.String(), "bypass_cache=true") {
		t.Errorf("success not audited: %s", audit.String())
	}
}

func TestDriveErrorIsAudited(t *testing.T) {
	fd := &fakeDriver{failNavigate: true}
	var audit bytes.Buffer
	s := newTestServer(fd, true, &audit)
	out, isErr := call(s, "browser_navigate", map[string]any{"session": "a", "url": "https://x"})
	if !isErr || !strings.Contains(out, "boom") {
		t.Fatalf("expected error surfaced, got %q isErr=%v", out, isErr)
	}
	if !strings.Contains(audit.String(), `"ok":false`) {
		t.Errorf("failure not audited: %s", audit.String())
	}
}

func writePlaybook(t *testing.T, body string) string {
	t.Helper()
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "flow.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScriptRunRequiresHostReadAndCannotBypassDriveGate(t *testing.T) {
	path := writePlaybook(t, "name: p\nsession: a\nsteps:\n  - verb: navigate\n    url: https://example.com/\n")
	fd := &fakeDriver{}
	var audit bytes.Buffer
	s := newTestServer(fd, false, &audit)
	out, isErr := call(s, "browser_script_run", map[string]any{"session": "a", "file": path})
	if !isErr || !strings.Contains(out, "host file reads") {
		t.Fatalf("script host read should be refused, got %q isErr=%v", out, isErr)
	}
	if !strings.Contains(audit.String(), "host read disabled") {
		t.Fatalf("refused host read was not audited: %s", audit.String())
	}

	s.opts.AllowHostRead = true
	out, isErr = call(s, "browser_script_run", map[string]any{"session": "a", "file": path})
	if !isErr || !strings.Contains(out, "drive-gated") {
		t.Fatalf("script navigate should remain drive-gated, got %q isErr=%v", out, isErr)
	}
	if len(fd.navigated) != 0 {
		t.Fatalf("drive-gated script reached driver: %+v", fd.navigated)
	}
}

func TestScriptRunRespectsPerToolGateAndOriginAllowlist(t *testing.T) {
	path := writePlaybook(t, "name: p\nsession: a\nsteps:\n  - verb: navigate\n    url: https://evil.test/\n")
	fd := &fakeDriver{}
	s := &server{opts: Options{
		Driver: fd, AllowHostRead: true,
		AllowedTools: map[string]bool{"browser_navigate": true},
		AllowOrigins: []string{"example.com"},
		Now:          func() time.Time { return time.Unix(0, 0).UTC() },
	}}
	out, isErr := call(s, "browser_script_run", map[string]any{"session": "a", "file": path})
	if !isErr || !strings.Contains(out, "not in the navigation allowlist") {
		t.Fatalf("script origin should be blocked, got %q isErr=%v", out, isErr)
	}
	if len(fd.navigated) != 0 {
		t.Fatalf("origin-blocked script reached driver: %+v", fd.navigated)
	}
}

func TestInitializeAndUnknownMethod(t *testing.T) {
	s := newTestServer(&fakeDriver{}, false, nil)
	resp, respond := s.handleRPC(rpcRequest{ID: json.RawMessage("1"), Method: "initialize"})
	if !respond || resp.Result == nil {
		t.Fatalf("initialize should respond with a result")
	}
	resp, _ = s.handleRPC(rpcRequest{ID: json.RawMessage("2"), Method: "bogus"})
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("unknown method should error -32601, got %+v", resp.Error)
	}
	// Notifications (no id) get no response.
	if _, respond := s.handleRPC(rpcRequest{Method: "initialized"}); respond {
		t.Error("notification should not be answered")
	}
}
