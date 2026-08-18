package skills

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nstranquist/snapref/browser"
)

// TestBatchAcrossDomainsViaSession proves the slice C → D handoff: one
// persistent SessionPage drives multiple fixture pages, each routed to
// the right skill, with all outputs assembled. This is the unit-level
// equivalent of the `snapref drift-batch` CLI smoke.
func TestBatchAcrossDomainsViaSession(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; -short")
	}
	if _, err := browser.ResolveRuntime(browser.RuntimeRequest{Kind: browser.RuntimeHeadlessShell}); err != nil {
		t.Skipf("no managed Headless Shell: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	receipt, err := RunRuntimeConsumerSmoke(ctx, func(opts browser.OpenOptions) (*browser.Session, error) {
		session, err := browser.Open(opts)
		if err != nil {
			return nil, err
		}
		managed, err := browser.SetSessionKeepAlive(session.Name)
		if err != nil {
			_ = browser.Close(session.Name, true)
		}
		return managed, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.DomainsChecked != 2 || receipt.YouTubeViews != 9876 {
		t.Fatalf("unexpected smoke receipt: %+v", receipt)
	}
}

func TestSessionPageRenewsManagedIdleLease(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	firstActivity := time.Now().UTC().Add(-time.Minute)
	sess := &browser.Session{
		Name:           "leased-batch",
		PID:            os.Getpid(),
		StartedAt:      firstActivity,
		LastActivityAt: firstActivity,
		IdleTimeout:    "30m0s",
	}
	if err := browser.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
	page := NewSessionPage(sess)
	if err := page.renewIdleLease(); err != nil {
		t.Fatal(err)
	}
	got, err := browser.LoadSession(sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.After(firstActivity) {
		t.Fatalf("last activity = %s, want after %s", got.LastActivityAt, firstActivity)
	}
}
