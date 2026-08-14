package browserscript

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestUploadShots(t *testing.T) {
	dir := t.TempDir()
	shot := filepath.Join(dir, "s.png")
	if err := os.WriteFile(shot, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	got := map[string]string{} // path -> content-type
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = r.Header.Get("content-type")
		mu.Unlock()
		if r.Method != http.MethodPut {
			w.WriteHeader(405)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	rep := &RunReport{Plan: "demo flow", Steps: []Result{
		{Index: 0, Verb: VerbNavigate}, // no screenshot → skipped
		{Index: 1, Verb: VerbScreenshot, Label: "Home", Screenshot: shot},
		{Index: 2, Verb: VerbScreenshot, Screenshot: shot}, // no label → verb-index key
	}}
	results := UploadShots(rep, ShotsUploadOptions{BaseURL: srv.URL, Slot: "after", Viewport: "web", Client: srv.Client()})

	if len(results) != 2 {
		t.Fatalf("expected 2 uploads (1 skipped), got %d", len(results))
	}
	for _, r := range results {
		if !r.OK {
			t.Fatalf("upload failed: %+v", r)
		}
	}
	// page keys are slugified <plan>-<label>-<viewport>
	if _, ok := got["/api/shots/demo-flow-home-web/after"]; !ok {
		t.Fatalf("missing labeled shot path, got: %v", keys(got))
	}
	if _, ok := got["/api/shots/demo-flow-screenshot-2-web/after"]; !ok {
		t.Fatalf("missing verb-index shot path, got: %v", keys(got))
	}
	for p, ct := range got {
		if ct != "image/png" {
			t.Fatalf("content-type for %s = %q, want image/png", p, ct)
		}
	}
}

func TestUploadShotsBadServer(t *testing.T) {
	dir := t.TempDir()
	shot := filepath.Join(dir, "s.png")
	os.WriteFile(shot, []byte("png"), 0o600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(415)
		w.Write([]byte("unsupported-type"))
	}))
	defer srv.Close()
	rep := &RunReport{Plan: "p", Steps: []Result{{Verb: VerbScreenshot, Screenshot: shot}}}
	results := UploadShots(rep, ShotsUploadOptions{BaseURL: srv.URL, Client: srv.Client()})
	if len(results) != 1 || results[0].OK || results[0].Status != 415 || !strings.Contains(results[0].Error, "unsupported") {
		t.Fatalf("expected one failed upload with 415, got %+v", results)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
