package browser

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRemoteTypeReplacesEditableContentAndNotifiesControlledInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<input id="controlled" value="initial">
<textarea id="notes">initial notes</textarea>
<div id="editor" contenteditable="true">initial editor</div>
<div id="plain">not editable</div>
<script>
  const controlled = document.querySelector('#controlled');
  const nativeValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value');
  window.typeState = {inputValues: [], inputEvents: 0, changeEvents: 0, instanceSetterCalls: 0};
  Object.defineProperty(controlled, 'value', {
    configurable: true,
    get() { return nativeValue.get.call(this); },
    set(value) {
      window.typeState.instanceSetterCalls += 1;
      nativeValue.set.call(this, value);
    }
  });
  controlled.addEventListener('input', () => {
    window.typeState.inputEvents += 1;
    window.typeState.inputValues.push(controlled.value);
  });
  controlled.addEventListener('change', () => { window.typeState.changeEvents += 1; });
  for (const id of ['notes', 'editor']) {
    const element = document.querySelector('#' + id);
    element.addEventListener('input', () => { element.dataset.input = 'yes'; });
    element.addEventListener('change', () => { element.dataset.change = 'yes'; });
  }
</script>
</body></html>`)
	}))
	defer fixture.Close()

	const sessionName = "type-replacement-e2e"
	sess, err := Open(OpenOptions{Name: sessionName, Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close(sessionName, true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	for _, value := range []string{"first value", "replacement", ""} {
		if err := RemoteType(ctx, sess, "#controlled", value, 10*time.Second); err != nil {
			t.Fatalf("type controlled value %q: %v", value, err)
		}
	}
	if err := RemoteType(ctx, sess, "#notes", "replacement notes", 10*time.Second); err != nil {
		t.Fatalf("type textarea: %v", err)
	}
	if err := RemoteType(ctx, sess, "#editor", "replacement editor", 10*time.Second); err != nil {
		t.Fatalf("type contenteditable: %v", err)
	}
	if err := RemoteType(ctx, sess, "#plain", "must fail", 10*time.Second); err == nil || !strings.Contains(err.Error(), "not an input, textarea, or contenteditable") {
		t.Fatalf("type non-editable error = %v", err)
	}

	var state struct {
		Controlled          string   `json:"controlled"`
		InputValues         []string `json:"inputValues"`
		InputEvents         int      `json:"inputEvents"`
		ChangeEvents        int      `json:"changeEvents"`
		InstanceSetterCalls int      `json:"instanceSetterCalls"`
		Notes               string   `json:"notes"`
		NotesInput          string   `json:"notesInput"`
		NotesChange         string   `json:"notesChange"`
		Editor              string   `json:"editor"`
		EditorInput         string   `json:"editorInput"`
		EditorChange        string   `json:"editorChange"`
	}
	expression := `(() => ({
  controlled: document.querySelector('#controlled').value,
  inputValues: window.typeState.inputValues,
  inputEvents: window.typeState.inputEvents,
  changeEvents: window.typeState.changeEvents,
  instanceSetterCalls: window.typeState.instanceSetterCalls,
  notes: document.querySelector('#notes').value,
  notesInput: document.querySelector('#notes').dataset.input || '',
  notesChange: document.querySelector('#notes').dataset.change || '',
  editor: document.querySelector('#editor').textContent,
  editorInput: document.querySelector('#editor').dataset.input || '',
  editorChange: document.querySelector('#editor').dataset.change || ''
}))()`
	if err := RemoteEval(ctx, sess, "", expression, &state, 5*time.Second); err != nil {
		t.Fatalf("inspect result: %v", err)
	}
	wantValues := []string{"first value", "replacement", ""}
	if state.Controlled != "" || !reflect.DeepEqual(state.InputValues, wantValues) || state.InputEvents != 3 || state.ChangeEvents != 3 {
		encoded, _ := json.Marshal(state)
		t.Fatalf("controlled input state = %s, want values %q and 3 input/change events", encoded, wantValues)
	}
	if state.InstanceSetterCalls != 0 {
		t.Fatalf("instance value setter calls = %d, want native prototype setter to bypass a framework tracker", state.InstanceSetterCalls)
	}
	if state.Notes != "replacement notes" || state.NotesInput != "yes" || state.NotesChange != "yes" {
		t.Fatalf("textarea state = value %q input %q change %q", state.Notes, state.NotesInput, state.NotesChange)
	}
	if state.Editor != "replacement editor" || state.EditorInput != "yes" || state.EditorChange != "yes" {
		t.Fatalf("contenteditable state = value %q input %q change %q", state.Editor, state.EditorInput, state.EditorChange)
	}
}

func TestRemoteTypeDoesNotHangWhenAnimationFramesAreSuspended(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<input id="field" value="initial">
<script>window.requestAnimationFrame = () => 0;</script>
</body></html>`)
	}))
	defer fixture.Close()

	const sessionName = "type-suspended-frames-e2e"
	sess, err := Open(OpenOptions{Name: sessionName, Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close(sessionName, true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	started := time.Now()
	if err := RemoteType(ctx, sess, "#field", "replacement", 2*time.Second); err != nil {
		t.Fatalf("type with suspended animation frames: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 1500*time.Millisecond {
		t.Fatalf("type with suspended animation frames took %s, want bounded timer fallback", elapsed)
	}
	var value string
	if err := RemoteEval(ctx, sess, "", `document.querySelector('#field').value`, &value, 5*time.Second); err != nil {
		t.Fatalf("inspect result: %v", err)
	}
	if value != "replacement" {
		t.Fatalf("field value = %q, want replacement", value)
	}
}

func TestRemoteClickScrollsExecutableRefIntoView(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body style="margin:0">
<div style="height:2400px"></div>
<button id="below-fold" onclick="this.dataset.clicked='yes'">Below fold</button>
</body></html>`)
	}))
	defer fixture.Close()

	const sessionName = "actionability-scroll-e2e"
	sess, err := Open(OpenOptions{Name: sessionName, Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close(sessionName, true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	snapshot, err := RemoteSnapshot(ctx, sess, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Elements) != 1 {
		t.Fatalf("snapshot elements = %d, want 1", len(snapshot.Elements))
	}
	if err := NewRemoteDriver().Click(ctx, sess.Name, snapshot.Elements[0].Ref); err != nil {
		t.Fatalf("click below-fold executable ref: %v", err)
	}
	var state struct {
		Clicked bool    `json:"clicked"`
		Top     float64 `json:"top"`
	}
	expression := `(() => {
  const el = document.querySelector('#below-fold');
  return {clicked: el.dataset.clicked === 'yes', top: el.getBoundingClientRect().top};
})()`
	if err := RemoteEval(ctx, sess, "", expression, &state, 5*time.Second); err != nil {
		t.Fatalf("inspect result: %v", err)
	}
	if !state.Clicked {
		t.Fatal("below-fold control was not activated")
	}
	if state.Top < 0 || state.Top >= 900 {
		t.Fatalf("target top = %.1f, want scrolled into the default viewport", state.Top)
	}
}
