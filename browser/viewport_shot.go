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

// ScreenshotAtViewport sets device metrics, captures the current page, then
// restores the previous CSS viewport. Restore is attempted even when capture
// fails; a restore error is returned only if capture succeeded.
func ScreenshotAtViewport(ctx context.Context, s *Session, vp Viewport, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	prev, err := CurrentViewport(ctx, s, timeout)
	if err != nil {
		return nil, err
	}
	if err := RemoteSetViewport(ctx, s, vp, timeout); err != nil {
		return nil, fmt.Errorf("set viewport %dx%d: %w", vp.Width, vp.Height, err)
	}
	png, capErr := RemoteScreenshotCurrentWithTimeout(s, false, timeout)
	if restErr := RemoteSetViewport(ctx, s, prev, timeout); restErr != nil {
		if capErr != nil {
			return nil, capErr
		}
		return png, fmt.Errorf("restore viewport %dx%d: %w", prev.Width, prev.Height, restErr)
	}
	if capErr != nil {
		return nil, capErr
	}
	return png, nil
}
