package browser

import (
	"context"
	"fmt"
	"time"
)

// CaptureInterval is the screenshot-loop period used by RunScreencast.
const CaptureInterval = 120 * time.Millisecond

// CurrentViewport returns the page's CSS viewport (window.innerWidth/Height).
func CurrentViewport(ctx context.Context, s *Session, timeout time.Duration) (Viewport, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	var box struct {
		W int `json:"w"`
		H int `json:"h"`
	}
	if err := RemoteEval(ctx, s, "", `({w: window.innerWidth, h: window.innerHeight})`, &box, timeout); err != nil {
		return Viewport{}, err
	}
	if box.W <= 0 || box.H <= 0 {
		return Viewport{}, fmt.Errorf("page reported empty viewport %dx%d", box.W, box.H)
	}
	return Viewport{Width: box.W, Height: box.H}, nil
}

// ScreenshotAtViewport captures the current page at vp as a one-shot override,
// then returns the page to the session's persisted emulation (or none).
func ScreenshotAtViewport(ctx context.Context, s *Session, vp Viewport, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return RemoteScreenshotCurrentWithOptions(ctx, s, ScreenshotCurrentOptions{
		Timeout:   timeout,
		Emulation: &DeviceEmulation{Width: vp.Width, Height: vp.Height},
	})
}
