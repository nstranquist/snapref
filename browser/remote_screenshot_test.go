package browser

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRemoteScreenshotCurrentDoesNotHangWhenAnimationFramesAreSuspended(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<h1>Screenshot fallback</h1>
<script>window.requestAnimationFrame = () => 0;</script>
</body></html>`)
	}))
	defer fixture.Close()

	const sessionName = "screenshot-suspended-frames-e2e"
	sess, err := Open(OpenOptions{Name: sessionName, Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close(sessionName, true)
	if err := RemoteNavigate(context.Background(), sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	started := time.Now()
	png, err := RemoteScreenshotCurrentWithTimeout(sess, false, 2*time.Second)
	if err != nil {
		t.Fatalf("screenshot with suspended animation frames: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 1500*time.Millisecond {
		t.Fatalf("screenshot with suspended animation frames took %s, want bounded timer fallback", elapsed)
	}
	if len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("screenshot is not a PNG: %d bytes", len(png))
	}
}

func TestScreenshotCaptureParamsWithoutMetricsUsesLiveRenderer(t *testing.T) {
	params := screenshotCaptureParams(false, nil, nil)
	if got := params["captureBeyondViewport"]; got != false {
		t.Fatalf("captureBeyondViewport = %#v, want false", got)
	}
	if got := params["fromSurface"]; got != false {
		t.Fatalf("fromSurface = %#v, want false", got)
	}
	if _, ok := params["clip"]; ok {
		t.Fatal("viewport capture unexpectedly has a clip")
	}
}

func TestScreenshotCaptureParamsViewportUsesCSSClip(t *testing.T) {
	params := screenshotCaptureParams(false, nil, &pageViewport{PageX: 12, PageY: 48, ClientWidth: 390.2, ClientHeight: 843.1})
	if got := params["captureBeyondViewport"]; got != false {
		t.Fatalf("captureBeyondViewport = %#v, want false", got)
	}
	if got := params["fromSurface"]; got != true {
		t.Fatalf("fromSurface = %#v, want true", got)
	}
	clip, ok := params["clip"].(map[string]any)
	if !ok {
		t.Fatalf("clip = %#v, want map", params["clip"])
	}
	if clip["x"] != float64(12) || clip["y"] != float64(48) || clip["width"] != float64(391) || clip["height"] != float64(844) {
		t.Fatalf("viewport clip = %#v", clip)
	}
}

func TestScreenshotCaptureParamsFullPageUsesContentClip(t *testing.T) {
	params := screenshotCaptureParams(true, &pageContentSize{X: 0, Y: 0, Width: 390.2, Height: 8421.4}, nil)
	if got := params["captureBeyondViewport"]; got != true {
		t.Fatalf("captureBeyondViewport = %#v, want true", got)
	}
	if got := params["fromSurface"]; got != true {
		t.Fatalf("fromSurface = %#v, want true", got)
	}
	clip, ok := params["clip"].(map[string]any)
	if !ok {
		t.Fatalf("clip = %#v, want map", params["clip"])
	}
	if got := clip["width"]; got != float64(391) {
		t.Fatalf("clip width = %#v, want 391", got)
	}
	if got := clip["height"]; got != float64(8422) {
		t.Fatalf("clip height = %#v, want 8422", got)
	}
	if got := clip["scale"]; got != 1 {
		t.Fatalf("clip scale = %#v, want 1", got)
	}
}
