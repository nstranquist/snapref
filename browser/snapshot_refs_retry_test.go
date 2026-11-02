package browser

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecutableSnapshotRefRetriesSameFingerprintDuringTransition(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><button id="target" onclick="this.dataset.clicked='yes'">Stable action</button>`)
	}))
	defer fixture.Close()

	const sessionName = "snapshot-transition-retry-e2e"
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
	if len(snapshot.Elements) != 1 {
		t.Fatalf("snapshot elements = %d, want 1", len(snapshot.Elements))
	}
	ref := snapshot.Elements[0].Ref

	var scheduled bool
	transition := `(() => {
  const current = document.querySelector('#target');
  const clone = current.cloneNode(true);
  const parent = current.parentElement;
  current.remove();
  setTimeout(() => parent.appendChild(clone), 200);
  return true;
})()`
	if err := RemoteEval(ctx, session, "", transition, &scheduled, 5*time.Second); err != nil || !scheduled {
		t.Fatalf("schedule transition: scheduled=%v err=%v", scheduled, err)
	}
	started := time.Now()
	if err := NewRemoteDriver().Click(ctx, session.Name, ref); err != nil {
		t.Fatalf("click ref through transition: %v", err)
	}
	elapsed := time.Since(started)
	if elapsed < 100*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("transition-aware click took %s, want a bounded retry", elapsed)
	}
	var clicked bool
	if err := RemoteEval(ctx, session, "", `document.querySelector('#target').dataset.clicked === 'yes'`, &clicked, 5*time.Second); err != nil {
		t.Fatalf("inspect click: %v", err)
	}
	if !clicked {
		t.Fatal("recovered target was not clicked")
	}
}

func TestExecutableSnapshotRefFailsClosedAfterNavigationToIdenticalControl(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><button id="target" onclick="this.dataset.clicked='yes'">Stable action</button>`)
	}))
	defer fixture.Close()

	const sessionName = "snapshot-navigation-stale-e2e"
	session, err := Open(OpenOptions{Name: sessionName, Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close(sessionName, true)
	ctx := context.Background()
	firstURL := fixture.URL + "/first?token=first-secret"
	secondURL := fixture.URL + "/second?token=second-secret"
	if err := RemoteNavigate(ctx, session, firstURL, 30*time.Second); err != nil {
		t.Fatalf("navigate first page: %v", err)
	}
	snapshot, err := RemoteSnapshot(ctx, session, SnapshotOptions{}, 30*time.Second)
	if err != nil {
		t.Fatalf("snapshot first page: %v", err)
	}
	if len(snapshot.Elements) != 1 {
		t.Fatalf("snapshot elements = %d, want 1", len(snapshot.Elements))
	}
	if err := RemoteNavigate(ctx, session, secondURL, 30*time.Second); err != nil {
		t.Fatalf("navigate second page: %v", err)
	}

	err = NewRemoteDriver().Click(ctx, session.Name, snapshot.Elements[0].Ref)
	if !errors.Is(err, ErrStaleSnapshotRef) {
		t.Fatalf("click stale ref error = %v, want ErrStaleSnapshotRef", err)
	}
	if !strings.Contains(err.Error(), "URL changed") {
		t.Fatalf("click stale ref error = %v, want URL-change guidance", err)
	}
	if strings.Contains(err.Error(), "first-secret") || strings.Contains(err.Error(), "second-secret") {
		t.Fatalf("stale-ref error leaked URL query data: %v", err)
	}
	var clicked bool
	if err := RemoteEval(ctx, session, "", `document.querySelector('#target').dataset.clicked === 'yes'`, &clicked, 5*time.Second); err != nil {
		t.Fatalf("inspect second page: %v", err)
	}
	if clicked {
		t.Fatal("stale ref clicked the identical control on the new page")
	}
}
