package browser

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
