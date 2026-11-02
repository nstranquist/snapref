package browser

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteSelectUsesVisibleLabelsAndSupportsExecutableShadowRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<label for="plan">Plan</label>
<select id="plan">
  <option value="hobby">Hobby plan</option>
  <option value="team">Team   plan</option>
</select>
<label for="duplicate">Duplicate</label>
<select id="duplicate"><option value="a">Same</option><option value="b">Same</option></select>
<label for="disabled-option">Disabled option</label>
<select id="disabled-option"><option>Allowed</option><option disabled>Blocked</option></select>
<label for="mixed-enabled">Mixed enabled</label>
<select id="mixed-enabled"><option value="disabled" disabled>Chosen</option><option value="enabled">Chosen</option></select>
<div id="plain">Not a select</div>
<div id="shadow-host"></div>
<script>
  window.selectState = {inputEvents: 0, changeEvents: 0};
  const plan = document.querySelector('#plan');
  plan.addEventListener('input', () => window.selectState.inputEvents++);
  plan.addEventListener('change', () => window.selectState.changeEvents++);
  const root = document.querySelector('#shadow-host').attachShadow({mode: 'open'});
  root.innerHTML = '<label for="region">Region</label><select id="region"><option value="us">United States</option><option value="eu">Europe</option></select>';
  root.querySelector('#region').addEventListener('change', event => event.currentTarget.dataset.changed = 'yes');
</script>
</body></html>`)
	}))
	defer fixture.Close()

	const sessionName = "select-visible-label-e2e"
	session, err := Open(OpenOptions{Name: sessionName, Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close(sessionName, true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, session, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	snapshot, err := RemoteSnapshot(ctx, session, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	refs := map[string]string{}
	for _, element := range snapshot.Elements {
		if element.Tag == "select" {
			refs[element.Name] = element.Ref
		}
	}
	if refs["Plan"] == "" || refs["Region"] == "" {
		t.Fatalf("snapshot missing select refs: %+v", refs)
	}
	driver := NewRemoteDriver()
	if err := driver.Select(ctx, session.Name, refs["Plan"], "  Team\nplan  "); err != nil {
		t.Fatalf("select top-level executable ref: %v", err)
	}
	if err := driver.Select(ctx, session.Name, refs["Region"], "Europe"); err != nil {
		t.Fatalf("select shadow executable ref: %v", err)
	}

	if err := RemoteSelect(ctx, session, "#plan", "team", 5*time.Second); err == nil || !strings.Contains(err.Error(), "no option") {
		t.Fatalf("opaque option value must not select: %v", err)
	}
	if err := RemoteSelect(ctx, session, "#duplicate", "Same", 5*time.Second); err == nil || !strings.Contains(err.Error(), "more than one enabled option") {
		t.Fatalf("duplicate visible label error = %v", err)
	}
	if err := RemoteSelect(ctx, session, "#disabled-option", "Blocked", 5*time.Second); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled option error = %v", err)
	}
	if err := RemoteSelect(ctx, session, "#mixed-enabled", "Chosen", 5*time.Second); err != nil {
		t.Fatalf("one enabled match among duplicate labels: %v", err)
	}
	if err := RemoteSelect(ctx, session, "#plain", "Anything", 5*time.Second); err == nil || !strings.Contains(err.Error(), "not a select element") {
		t.Fatalf("non-select error = %v", err)
	}

	var state struct {
		Plan          string `json:"plan"`
		InputEvents   int    `json:"input_events"`
		ChangeEvents  int    `json:"change_events"`
		Region        string `json:"region"`
		RegionChanged string `json:"region_changed"`
		Mixed         string `json:"mixed"`
	}
	expression := `(() => {
  const region = document.querySelector('#shadow-host').shadowRoot.querySelector('#region');
  return {
    plan: document.querySelector('#plan').value,
    input_events: window.selectState.inputEvents,
    change_events: window.selectState.changeEvents,
    region: region.value,
    region_changed: region.dataset.changed || '',
    mixed: document.querySelector('#mixed-enabled').value
  };
})()`
	if err := RemoteEval(ctx, session, "", expression, &state, 5*time.Second); err != nil {
		t.Fatalf("inspect select state: %v", err)
	}
	if state.Plan != "team" || state.InputEvents != 1 || state.ChangeEvents != 1 || state.Region != "eu" || state.RegionChanged != "yes" || state.Mixed != "enabled" {
		t.Fatalf("select state = %+v", state)
	}
}

func TestRemoteSelectRequiresNonEmptyVisibleLabel(t *testing.T) {
	err := SelectOn(context.Background(), context.Background(), "#plan", " \n ", time.Second)
	if err == nil || !strings.Contains(err.Error(), "label is required") {
		t.Fatalf("empty option label error = %v", err)
	}
}
