package browser

import "testing"

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
