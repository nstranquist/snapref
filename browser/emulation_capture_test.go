package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHolderCaptureFailsClosedWhenNoHolderTakesIt(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := holderCapture(ctx, &Session{Name: "unclaimed"}, false)
	if !errors.Is(err, errHolderCaptureUnclaimed) {
		t.Fatalf("err = %v, want errHolderCaptureUnclaimed", err)
	}
	if elapsed := time.Since(start); elapsed < holderCaptureClaimWait {
		t.Fatalf("gave up after %s, before the claim wait", elapsed)
	}
	dir, _ := holderCaptureDir("unclaimed")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("request files left behind: %d", len(entries))
	}
}

func TestHolderCaptureRoundTripThroughServe(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &backgroundScreenshotTransport{}
	stop := make(chan struct{})
	served := make(chan struct{})
	go func() {
		defer close(served)
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				serveHolderCaptures(ctx, "served", transport)
			}
		}
	}()
	png, err := holderCapture(ctx, &Session{Name: "served"}, true)
	close(stop)
	<-served
	if err != nil || string(png) != "png" {
		t.Fatalf("capture = %q, %v", png, err)
	}
	captured := false
	for _, method := range transport.methods {
		if method == "Page.captureScreenshot" {
			captured = true
		}
	}
	if !captured {
		t.Fatalf("holder connection never captured: %v", transport.methods)
	}
	dir, _ := holderCaptureDir("served")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("request files left behind: %d", len(entries))
	}
}

func TestServeHolderCapturesDropsStaleRequests(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	dir, err := holderCaptureDir("stale")
	if err != nil {
		t.Fatal(err)
	}
	req := filepath.Join(dir, "old.req.json")
	if err := writeJSONAtomic(req, holderCaptureRequest{ID: "old"}, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * holderCaptureStaleAge)
	if err := os.Chtimes(req, old, old); err != nil {
		t.Fatal(err)
	}
	transport := &backgroundScreenshotTransport{}
	serveHolderCaptures(context.Background(), "stale", transport)
	if _, err := os.Stat(req); !os.IsNotExist(err) {
		t.Fatalf("stale request kept: %v", err)
	}
	if len(transport.methods) != 0 {
		t.Fatalf("stale request was captured: %v", transport.methods)
	}
}

func TestHolderCaptureDirRejectsBadSessionNames(t *testing.T) {
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if _, err := holderCaptureDir("../escape"); err == nil {
		t.Fatal("holderCaptureDir accepted a path-escaping name")
	}
}
