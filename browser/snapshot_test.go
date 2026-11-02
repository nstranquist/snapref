package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSnapshotExcludesAriaHidden drives a real headless session against a fixture
// where a link sits inside an aria-hidden="true" subtree and another inside [inert].
// Those are not real interactive targets, so the default inventory must skip them
// while --all (IncludeHidden) still surfaces them. Chromium/-short gated.
func TestSnapshotExcludesAriaHidden(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>ah</title></head><body>
  <a href="/real" data-test="real-link">Real link</a>
  <div aria-hidden="true"><a href="/ghost" data-test="ghost-link">Ghost link</a></div>
  <div inert><button data-test="inert-btn">Inert button</button></div>
</body></html>`)
	}))
	defer fixture.Close()

	sess, err := Open(OpenOptions{Name: "snap-ah-e2e", Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close("snap-ah-e2e", true)

	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	names := func(snap *PageSnapshot) map[string]bool {
		m := map[string]bool{}
		for _, e := range snap.Elements {
			m[e.Name] = true
		}
		return m
	}

	// Default: aria-hidden + inert are excluded.
	def, err := RemoteSnapshot(ctx, sess, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got := names(def)
	if !got["Real link"] {
		t.Errorf("default snapshot missing the real link; got %v", got)
	}
	if got["Ghost link"] {
		t.Errorf("default snapshot leaked an aria-hidden link")
	}
	if got["Inert button"] {
		t.Errorf("default snapshot leaked an [inert] button")
	}

	// --all: hidden elements reappear.
	all, err := RemoteSnapshot(ctx, sess, SnapshotOptions{IncludeHidden: true}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot --all: %v", err)
	}
	if a := names(all); !a["Ghost link"] || !a["Inert button"] {
		t.Errorf("IncludeHidden should surface aria-hidden/inert; got %v", a)
	}
}

func TestSnapshotAdaptivelyWaitsForHydratedInventory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><main id="app"></main></body></html>`)
	}))
	defer fixture.Close()

	sess, err := Open(OpenOptions{Name: "snapshot-hydration-e2e", Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close("snapshot-hydration-e2e", true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	// Couple hydration to the first inventory query instead of wall time since
	// navigation. On a loaded machine Chrome may take longer than a fixed page
	// timer to reattach, which used to turn this into an immediate snapshot and
	// made the adaptive-readiness assertion flaky.
	var armed bool
	err = RemoteEval(ctx, sess, fixture.URL, `(()=>{
  const original = Document.prototype.querySelectorAll;
  Document.prototype.querySelectorAll = function(selector) {
    Document.prototype.querySelectorAll = original;
    setTimeout(() => { document.querySelector('#app').innerHTML = '<button id="hydrated">Hydrated action</button>'; }, 100);
    return original.call(this, selector);
  };
  return true;
})()`, &armed, 30*time.Second)
	if err != nil || !armed {
		t.Fatalf("arm deterministic hydration: armed=%v err=%v", armed, err)
	}

	snapshot, err := RemoteSnapshot(ctx, sess, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Returned != 1 || snapshot.Elements[0].Selector != "#hydrated" {
		t.Fatalf("adaptive readiness missed hydrated control: %+v", snapshot)
	}
	if snapshot.Attempts < 2 || snapshot.ReadyBy != "non_empty" || snapshot.ReadinessWaitMS < 100 {
		t.Fatalf("readiness evidence = attempts:%d ready_by:%q wait:%dms", snapshot.Attempts, snapshot.ReadyBy, snapshot.ReadinessWaitMS)
	}
}

func TestSnapshotWaitForSelectorAndSettle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><main id="app"></main><script>
setTimeout(() => { document.querySelector('#app').innerHTML = '<button id="ready">Ready</button>'; }, 150);
</script></body></html>`)
	}))
	defer fixture.Close()

	sess, err := Open(OpenOptions{Name: "snapshot-ready-selector-e2e", Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close("snapshot-ready-selector-e2e", true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	snapshot, err := RemoteSnapshot(ctx, sess, SnapshotOptions{
		WaitForSelector: "#ready",
		Settle:          500 * time.Millisecond,
	}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Returned != 1 || snapshot.ReadyBy != "stable" || snapshot.Attempts < 2 {
		t.Fatalf("explicit readiness contract not satisfied: %+v", snapshot)
	}
}

func TestSnapshotRedactsSensitiveValuesInBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<label for="pw">Password</label><input id="pw" type="password" value="DUMMY_PASSWORD_123">
<label for="key">API key</label><input id="key" name="api-key" value="DUMMY_API_KEY_456">
<label for="note">Note</label><input id="note" value="ordinary value">
<textarea id="secret-textarea" name="client-secret">DUMMY_TEXTAREA_SECRET_789</textarea>
<select id="secret-select" name="client-secret"><option value="DUMMY_SELECT_VALUE_987">DUMMY_SELECT_LABEL_654</option></select>
</body></html>`)
	}))
	defer fixture.Close()

	sess, err := Open(OpenOptions{Name: "snap-secret-e2e", Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close("snap-secret-e2e", true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	snap, err := RemoteSnapshot(ctx, sess, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	bySelector := map[string]SnapshotElement{}
	for _, element := range snap.Elements {
		bySelector[element.Selector] = element
	}
	for _, selector := range []string{"#pw", "#key", "#secret-textarea", "#secret-select"} {
		got := bySelector[selector]
		if got.Value != "" || !got.ValueRedacted {
			t.Fatalf("%s leaked a sensitive value: %+v", selector, got)
		}
	}
	if got := bySelector["#note"]; got.Value != "ordinary value" || got.ValueRedacted {
		t.Fatalf("ordinary field was not preserved: %+v", got)
	}
	encoded, _ := json.Marshal(snap)
	for _, secret := range []string{"DUMMY_PASSWORD_123", "DUMMY_API_KEY_456", "DUMMY_TEXTAREA_SECRET_789", "DUMMY_SELECT_VALUE_987", "DUMMY_SELECT_LABEL_654"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("serialized snapshot leaked %q: %s", secret, encoded)
		}
	}
}

func TestExecutableSnapshotRefRunsAndSupersededRefIsRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<button id="target" onclick="this.dataset.clicked='yes'; this.textContent='Clicked'">Click me</button>
</body></html>`)
	}))
	defer fixture.Close()

	sess, err := Open(OpenOptions{Name: "snapshot-ref-e2e", Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close("snapshot-ref-e2e", true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	first, err := RemoteSnapshot(ctx, sess, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(first.Elements) != 1 || !executableSnapshotRefRE.MatchString(first.Elements[0].Ref) {
		t.Fatalf("missing executable ref: %+v", first)
	}
	ref := first.Elements[0].Ref
	driver := NewRemoteDriver()
	if err := driver.Click(ctx, sess.Name, ref); err != nil {
		t.Fatalf("click executable ref: %v", err)
	}
	var clicked string
	if err := RemoteEval(ctx, sess, "", `document.querySelector('#target').dataset.clicked`, &clicked, 30*time.Second); err != nil {
		t.Fatalf("read click result: %v", err)
	}
	if clicked != "yes" {
		t.Fatalf("ref click did not actuate the expected element: %q", clicked)
	}

	second, err := RemoteSnapshot(ctx, sess, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if second.Revision == first.Revision {
		t.Fatalf("changed interactive inventory kept revision %q", first.Revision)
	}
	if err := driver.Click(ctx, sess.Name, ref); !errors.Is(err, ErrStaleSnapshotRef) {
		t.Fatalf("superseded ref error = %v, want ErrStaleSnapshotRef", err)
	}
}

func TestExecutableSnapshotRefRejectsFingerprintChangeWithoutNewSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><button id="target">Original</button>`)
	}))
	defer fixture.Close()
	sess, err := Open(OpenOptions{Name: "snapshot-fingerprint-e2e", Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close("snapshot-fingerprint-e2e", true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	snapshot, err := RemoteSnapshot(ctx, sess, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ref := snapshot.Elements[0].Ref
	var ignored any
	if err := RemoteEval(ctx, sess, "", `document.querySelector('#target').textContent='Different'`, &ignored, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTarget(ctx, sess, ref, 30*time.Second); !errors.Is(err, ErrStaleSnapshotRef) || !strings.Contains(err.Error(), "transition retry") || !strings.Contains(err.Error(), "take a new snapshot") {
		t.Fatalf("fingerprint mismatch error = %v, want transition diagnostic and ErrStaleSnapshotRef", err)
	}
}

func TestSnapshotDiscoversAndActsInsideShadowRootsAndFrames(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/frame" {
			_, _ = io.WriteString(w, `<!doctype html><html><body>
<button id="frame-button" onclick="this.dataset.clicked='yes'">Frame action</button>
</body></html>`)
			return
		}
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<div id="shadow-host"></div>
<iframe id="child-frame" src="/frame"></iframe>
<script>
const root = document.querySelector('#shadow-host').attachShadow({mode: 'open'});
root.innerHTML = '<button id="shadow-button">Shadow action</button><input id="shadow-input" aria-label="Shadow input">';
root.querySelector('#shadow-button').addEventListener('click', event => event.currentTarget.dataset.clicked = 'yes');
</script>
</body></html>`)
	}))
	defer fixture.Close()

	sess, err := Open(OpenOptions{Name: "snapshot-nested-e2e", Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close("snapshot-nested-e2e", true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := RemoteWaitVisible(ctx, sess, "#child-frame", 30*time.Second); err != nil {
		t.Fatalf("wait frame: %v", err)
	}

	snapshot, err := RemoteSnapshot(ctx, sess, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.ShadowRoots != 1 || snapshot.Frames != 1 || snapshot.CrossOriginFrames != 0 {
		t.Fatalf("nested discovery counts = shadow:%d frames:%d cross-origin:%d", snapshot.ShadowRoots, snapshot.Frames, snapshot.CrossOriginFrames)
	}
	byName := make(map[string]SnapshotElement, len(snapshot.Elements))
	for _, element := range snapshot.Elements {
		byName[element.Name] = element
	}
	shadowButton := byName["Shadow action"]
	shadowInput := byName["Shadow input"]
	frameButton := byName["Frame action"]
	if len(shadowButton.Context) != 1 || shadowButton.Context[0].Kind != SnapshotRootShadow {
		t.Fatalf("shadow context = %+v", shadowButton.Context)
	}
	if len(frameButton.Context) != 1 || frameButton.Context[0].Kind != SnapshotRootFrame {
		t.Fatalf("frame context = %+v", frameButton.Context)
	}

	driver := NewRemoteDriver()
	if err := driver.Click(ctx, sess.Name, shadowButton.Ref); err != nil {
		t.Fatalf("click shadow ref: %v", err)
	}
	if err := driver.Type(ctx, sess.Name, shadowInput.Ref, "nested text"); err != nil {
		t.Fatalf("type shadow ref: %v", err)
	}
	if err := driver.Click(ctx, sess.Name, frameButton.Ref); err != nil {
		t.Fatalf("click frame ref: %v", err)
	}
	if text, err := driver.Text(ctx, sess.Name, shadowButton.Ref); err != nil || text != "Shadow action" {
		t.Fatalf("text shadow ref = %q, %v", text, err)
	}

	var state struct {
		ShadowClicked string `json:"shadow_clicked"`
		ShadowValue   string `json:"shadow_value"`
		FrameClicked  string `json:"frame_clicked"`
	}
	if err := RemoteEval(ctx, sess, "", `(() => ({
shadow_clicked: document.querySelector('#shadow-host').shadowRoot.querySelector('#shadow-button').dataset.clicked,
shadow_value: document.querySelector('#shadow-host').shadowRoot.querySelector('#shadow-input').value,
frame_clicked: document.querySelector('#child-frame').contentDocument.querySelector('#frame-button').dataset.clicked
}))()`, &state, 30*time.Second); err != nil {
		t.Fatalf("read nested state: %v", err)
	}
	if state.ShadowClicked != "yes" || state.ShadowValue != "nested text" || state.FrameClicked != "yes" {
		t.Fatalf("nested state = %+v", state)
	}
}

func TestResolveTargetPreservesSelectorsAndRejectsMalformedRef(t *testing.T) {
	session := &Session{Name: "test"}
	for _, want := range []string{`button:hover`, `sr-button`, `sr-button:enabled`} {
		selector, err := ResolveTarget(context.Background(), session, want, time.Second)
		if err != nil || selector != want {
			t.Fatalf("selector %q changed: %q err=%v", want, selector, err)
		}
	}
	if _, err := ResolveTarget(context.Background(), session, `sr-0123456789ab:bad`, time.Second); !errors.Is(err, ErrMalformedSnapshotRef) {
		t.Fatalf("malformed ref error = %v", err)
	}
}

func TestValidateExecutableSnapshotRefIsStrictForExplicitRefInputs(t *testing.T) {
	if err := ValidateExecutableSnapshotRef("sr-0123456789ab:e4"); err != nil {
		t.Fatalf("valid ref: %v", err)
	}
	for _, ref := range []string{"sr-button", "sr-bad:e1", "sr-0123456789ab:e0"} {
		if err := ValidateExecutableSnapshotRef(ref); !errors.Is(err, ErrMalformedSnapshotRef) {
			t.Errorf("ValidateExecutableSnapshotRef(%q) = %v", ref, err)
		}
	}
}

func sampleRaw() rawSnapshot {
	return rawSnapshot{
		URL:   "https://example.test/app",
		Title: "App",
		Elements: []SnapshotElement{
			{Role: "button", Tag: "button", Name: "Sign in", Selector: "button[type=submit]", Visible: true},
			{Role: "link", Tag: "a", Name: "Home", Selector: "#home", Href: "/", Visible: true},
			{Role: "textbox", Tag: "input", Name: "Email", Selector: "#email", Type: "email", Visible: true, Rect: &Rect{X: 1, Y: 2, W: 3, H: 4}},
			{Role: "button", Tag: "button", Name: "Hidden", Selector: ".hidden", Visible: false},
			{Role: "link", Tag: "a", Name: "Docs", Selector: "#docs", Href: "/docs", Visible: true},
		},
	}
}

func TestPostProcessDefaultsVisibleOnlyAndAssignsRefs(t *testing.T) {
	snap := postProcessSnapshot(sampleRaw(), SnapshotOptions{}, 12)
	if snap.Returned != 4 {
		t.Fatalf("expected 4 visible elements, got %d", snap.Returned)
	}
	if snap.Matched != 4 || snap.Truncated {
		t.Fatalf("matched=%d truncated=%v unexpected", snap.Matched, snap.Truncated)
	}
	for i, e := range snap.Elements {
		want := fmt.Sprintf("%s:e%d", snap.Revision, i+1)
		if e.Ref != want {
			t.Errorf("element %d ref = %q, want %q", i, e.Ref, want)
		}
		if !e.Visible {
			t.Errorf("element %d should be visible (hidden ones filtered)", i)
		}
	}
	if !strings.HasPrefix(snap.Revision, "sr-") {
		t.Fatalf("revision = %q, want sr-*", snap.Revision)
	}
	if snap.Counts["button"] != 1 || snap.Counts["link"] != 2 || snap.Counts["textbox"] != 1 {
		t.Fatalf("counts wrong: %+v", snap.Counts)
	}
	if snap.DurationMS != 12 {
		t.Errorf("duration not propagated: %d", snap.DurationMS)
	}
}

func TestPostProcessIncludeHidden(t *testing.T) {
	snap := postProcessSnapshot(sampleRaw(), SnapshotOptions{IncludeHidden: true}, 0)
	if snap.Returned != 5 {
		t.Fatalf("include-hidden should return all 5, got %d", snap.Returned)
	}
}

func TestPostProcessRoleFilter(t *testing.T) {
	snap := postProcessSnapshot(sampleRaw(), SnapshotOptions{Roles: []string{"link"}}, 0)
	if snap.Returned != 2 {
		t.Fatalf("role=link should return 2, got %d", snap.Returned)
	}
	for _, e := range snap.Elements {
		if e.Role != "link" {
			t.Errorf("role filter leaked %q", e.Role)
		}
	}
}

func TestPostProcessNameFilterCaseInsensitive(t *testing.T) {
	snap := postProcessSnapshot(sampleRaw(), SnapshotOptions{Filter: "sign"}, 0)
	if snap.Returned != 1 || snap.Elements[0].Name != "Sign in" {
		t.Fatalf("name filter failed: %+v", snap.Elements)
	}
}

func TestPostProcessCapTruncates(t *testing.T) {
	snap := postProcessSnapshot(sampleRaw(), SnapshotOptions{Max: 2}, 0)
	if snap.Returned != 2 || !snap.Truncated || snap.Matched != 4 {
		t.Fatalf("cap: returned=%d truncated=%v matched=%d", snap.Returned, snap.Truncated, snap.Matched)
	}
}

func TestPostProcessRectStrippedUnlessRequested(t *testing.T) {
	without := postProcessSnapshot(sampleRaw(), SnapshotOptions{IncludeHidden: true}, 0)
	for _, e := range without.Elements {
		if e.Rect != nil {
			t.Fatalf("rect should be stripped by default on %q", e.Name)
		}
	}
	with := postProcessSnapshot(sampleRaw(), SnapshotOptions{IncludeHidden: true, IncludeRect: true}, 0)
	var sawRect bool
	for _, e := range with.Elements {
		if e.Rect != nil {
			sawRect = true
		}
	}
	if !sawRect {
		t.Fatalf("expected at least one rect when IncludeRect set")
	}
}

func TestPostProcessRedactsSensitiveValues(t *testing.T) {
	raw := rawSnapshot{Elements: []SnapshotElement{
		{Role: "textbox", Tag: "input", Name: "Password", Selector: "#password", Type: "password", Value: "DUMMY_SECRET_123", Visible: true},
		{Role: "textbox", Tag: "input", Name: "API key", Selector: "#api-key", Type: "text", Value: "sk-dummy", Visible: true},
		{Role: "textbox", Tag: "textarea", Name: "Notes", Selector: "#notes", Value: "safe text", Visible: true},
		{Role: "combobox", Tag: "select", Name: "Environment", Selector: "#environment", Value: "staging", Visible: true},
	}}

	snap := postProcessSnapshot(raw, SnapshotOptions{}, 0)
	if got := snap.Elements[0]; got.Value != "" || !got.ValueRedacted {
		t.Fatalf("password was not redacted: %+v", got)
	}
	if got := snap.Elements[1]; got.Value != "" || !got.ValueRedacted {
		t.Fatalf("credential-like text input was not redacted: %+v", got)
	}
	for _, index := range []int{2, 3} {
		if got := snap.Elements[index]; got.ValueRedacted || got.Value == "" {
			t.Fatalf("ordinary field was unexpectedly redacted: %+v", got)
		}
	}
}

func TestPostProcessHonorsExtractorRedaction(t *testing.T) {
	raw := rawSnapshot{Elements: []SnapshotElement{{
		Role: "textbox", Tag: "input", Name: "Unclassified", Selector: "#field",
		Type: "text", Value: "must-not-escape", ValueRedacted: true, Visible: true,
	}}}
	snap := postProcessSnapshot(raw, SnapshotOptions{}, 0)
	if got := snap.Elements[0]; got.Value != "" || !got.ValueRedacted {
		t.Fatalf("extractor redaction marker was not enforced: %+v", got)
	}
}

func TestSnapshotJSExprShapeAndParams(t *testing.T) {
	js := snapshotJSExpr(321, true)
	if !strings.HasPrefix(js, "(() => {") || !strings.HasSuffix(js, "})()") {
		t.Fatalf("snapshot JS is not a self-invoking expression")
	}
	if !strings.Contains(js, "HARD_CAP = 321") {
		t.Fatalf("hard cap not substituted")
	}
	if !strings.Contains(js, "INCLUDE_RECT = true") {
		t.Fatalf("include-rect not substituted true")
	}
	for _, needle := range []string{"querySelectorAll", "role=button", "data-testid", "location.href", "getClientRects", "value_redacted", "current-password", "shadowRoot", "contentDocument", "cross_origin_frames"} {
		if !strings.Contains(js, needle) {
			t.Errorf("snapshot JS missing %q", needle)
		}
	}
	off := snapshotJSExpr(0, false)
	if !strings.Contains(off, fmt.Sprintf("HARD_CAP = %d", snapshotHardCap)) {
		t.Errorf("hardCap<=0 should fall back to snapshotHardCap")
	}
	if !strings.Contains(off, "INCLUDE_RECT = false") {
		t.Errorf("include-rect false not substituted")
	}
}

func makeRawN(n int) rawSnapshot {
	roles := []string{"button", "link", "textbox", "checkbox"}
	els := make([]SnapshotElement, n)
	for i := range els {
		els[i] = SnapshotElement{
			Role: roles[i%len(roles)], Tag: "div", Name: fmt.Sprintf("Element %d", i),
			Selector: fmt.Sprintf("#e%d", i), Visible: i%5 != 0,
		}
	}
	return rawSnapshot{URL: "https://x/", Title: "x", Elements: els}
}

func BenchmarkSnapshotPostProcess(b *testing.B) {
	raw := makeRawN(1000)
	opts := SnapshotOptions{Max: 200, Roles: []string{"button", "link"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = postProcessSnapshot(raw, opts, 0)
	}
}

func BenchmarkBuildSnapshotJS(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = snapshotJSExpr(snapshotHardCap, false)
	}
}
