package browser

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// LaunchOptions controls the headless Chromium one-shot used by stateless
// commands. Slice B will reuse the same struct for the open/close path.
type LaunchOptions struct {
	Chrome          string        // path to chrome binary; empty = LocateChrome
	Runtime         RuntimeKind   // empty = auto; Chrome makes the request external
	Headless        bool          // default true
	Timeout         time.Duration // hard deadline for the whole op (default 60s)
	UserAgent       string
	Viewport        Viewport
	ColorScheme     string // prefers-color-scheme: "" (page default), light or dark
	resolvedRuntime ResolvedRuntime
	runtimeUnlock   func()
}

// Viewport controls the initial window size for screenshots / probes.
type Viewport struct {
	Width  int
	Height int
}

// Default returns a LaunchOptions with sane stateless defaults.
func DefaultLaunch() LaunchOptions {
	return LaunchOptions{
		Headless: true,
		Timeout:  60 * time.Second,
		Viewport: Viewport{Width: 1440, Height: 900},
	}
}

func (o *LaunchOptions) normalize() (LaunchOptions, error) {
	out := *o
	out.runtimeUnlock = nil
	if err := validColorScheme(out.ColorScheme); err != nil {
		return out, err
	}
	request := RuntimeRequest{
		Kind: out.Runtime, Headful: !out.Headless, ExternalPath: out.Chrome,
	}
	unlock, err := acquireRuntimeKindLocks(runtimeLockKindsForRequest(request), 30*time.Second)
	if err != nil {
		return out, fmt.Errorf("lock browser runtime for launch: %w", err)
	}
	resolved, err := ResolveRuntime(request)
	if err != nil {
		unlock()
		return out, err
	}
	out.Chrome = resolved.Path
	out.Runtime = resolved.Kind
	out.resolvedRuntime = resolved
	out.runtimeUnlock = unlock
	if out.Timeout == 0 {
		out.Timeout = 60 * time.Second
	}
	if out.Viewport.Width == 0 {
		out.Viewport.Width = 1440
	}
	if out.Viewport.Height == 0 {
		out.Viewport.Height = 900
	}
	return out, nil
}

func newAllocCtx(parent context.Context, o LaunchOptions) (context.Context, context.CancelFunc) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(o.Chrome),
		chromedp.WindowSize(o.Viewport.Width, o.Viewport.Height),
		chromedp.Flag("force-device-scale-factor", "1"),
		// Disable the welcome flow on macOS Chrome.
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-notifications", true),
	)
	if o.Headless {
		// ExecAllocator owns creation of its initial page target. Chromium 151
		// accepts --no-startup-window but then leaves chromedp waiting on a target
		// until the operation deadline. Managed executable identity and headless
		// mode are the isolation boundary; no SnapRef launch uses that flag.
		opts = append(opts, chromedp.Headless)
	} else {
		opts = append(opts, chromedp.Flag("headless", false))
	}
	if o.UserAgent != "" {
		opts = append(opts, chromedp.UserAgent(o.UserAgent))
	}
	return chromedp.NewExecAllocator(parent, opts...)
}

// launchNavigate is the start of every stateless run: the launch's colour
// scheme (set before the page loads, so first paint already matches), then
// the navigation and the body wait.
func launchNavigate(o LaunchOptions, url string) []chromedp.Action {
	actions := []chromedp.Action{}
	if o.ColorScheme != "" {
		actions = append(actions, emulation.SetEmulatedMedia().WithFeatures(mediaFeatures(o.ColorScheme)))
	}
	return append(actions,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
	)
}

// Screenshot navigates to url and returns a full-page PNG.
func Screenshot(ctx context.Context, opts LaunchOptions, url string) ([]byte, error) {
	return ScreenshotWithCapture(ctx, opts, url, true)
}

// ScreenshotWithCapture navigates to url and returns a PNG. When
// captureBeyondViewport is false, Chrome captures only the active viewport.
func ScreenshotWithCapture(ctx context.Context, opts LaunchOptions, url string, captureBeyondViewport bool) ([]byte, error) {
	return ScreenshotWithCaptureAndWait(ctx, opts, url, captureBeyondViewport, "")
}

// ScreenshotWithCaptureAndWait is ScreenshotWithCapture plus an optional
// visible-selector gate before capture. The selector gate is for SPAs where
// document.readyState can be complete before the app has hydrated.
func ScreenshotWithCaptureAndWait(ctx context.Context, opts LaunchOptions, url string, captureBeyondViewport bool, waitSelector string) ([]byte, error) {
	o, err := opts.normalize()
	if err != nil {
		return nil, err
	}
	defer o.runtimeUnlock()
	allocCtx, cancelAlloc := newAllocCtx(ctx, o)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()
	taskCtx, cancelTimeout := context.WithTimeout(taskCtx, o.Timeout)
	defer cancelTimeout()

	var buf []byte
	actions := launchNavigate(o, url)
	if waitSelector != "" {
		actions = append(actions, chromedp.WaitVisible(waitSelector, chromedp.ByQuery))
	}
	actions = append(actions,
		chromedp.ActionFunc(func(ctx context.Context) error {
			data, err := page.CaptureScreenshot().
				WithCaptureBeyondViewport(captureBeyondViewport).
				WithFromSurface(true).
				Do(ctx)
			if err != nil {
				return err
			}
			buf = data
			return nil
		}),
	)
	if err := chromedp.Run(taskCtx, actions...); err != nil {
		return nil, fmt.Errorf("screenshot %s: %w", url, err)
	}
	return buf, nil
}

// EvalResult is the typed return of EvalJSON: any JSON-serializable value.
// Callers usually pass *map[string]any or *[]any.
//
// Eval navigates to url, waits for body, runs js (an expression — wrap
// statements in `(()=>{ ... return result; })()`), and decodes the result
// into out via JSON. A returned Promise is awaited, so an async expression
// gives its resolved value (an unresolved promise would decode as `{}`). To
// start work without waiting for it, return something else (`void p`).
func Eval(ctx context.Context, opts LaunchOptions, url, js string, out any) error {
	o, err := opts.normalize()
	if err != nil {
		return err
	}
	defer o.runtimeUnlock()
	allocCtx, cancelAlloc := newAllocCtx(ctx, o)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()
	taskCtx, cancelTimeout := context.WithTimeout(taskCtx, o.Timeout)
	defer cancelTimeout()

	actions := append(launchNavigate(o, url),
		chromedp.ActionFunc(func(ctx context.Context) error { return runEvalPromise(ctx, js, out) }),
	)
	if err := chromedp.Run(taskCtx, actions...); err != nil {
		return fmt.Errorf("eval %s: %w", url, err)
	}
	return nil
}

// EvalAwait is Eval. Eval awaits a returned Promise as well; the name stays
// for existing callers.
func EvalAwait(ctx context.Context, opts LaunchOptions, url, js string, out any) error {
	return Eval(ctx, opts, url, js, out)
}

// FetchHTML navigates to url, waits for body, and returns rendered HTML.
// Use this when WebFetch's static-HTML path is blocked by JS-heavy SPAs.
func FetchHTML(ctx context.Context, opts LaunchOptions, url string) (string, error) {
	o, err := opts.normalize()
	if err != nil {
		return "", err
	}
	defer o.runtimeUnlock()
	allocCtx, cancelAlloc := newAllocCtx(ctx, o)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()
	taskCtx, cancelTimeout := context.WithTimeout(taskCtx, o.Timeout)
	defer cancelTimeout()

	var html string
	actions := append(launchNavigate(o, url), chromedp.OuterHTML("html", &html, chromedp.ByQuery))
	if err := chromedp.Run(taskCtx, actions...); err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	return html, nil
}

// SaveScreenshot is a convenience wrapper that writes the PNG to path
// atomically (temp + rename) and returns the final path. Empty path
// auto-derives a name.
func SaveScreenshot(ctx context.Context, opts LaunchOptions, url, path string) (string, error) {
	return SaveScreenshotWithCapture(ctx, opts, url, path, true)
}

// SaveScreenshotWithCapture is SaveScreenshot with explicit full-page vs
// viewport-only capture control.
func SaveScreenshotWithCapture(ctx context.Context, opts LaunchOptions, url, path string, captureBeyondViewport bool) (string, error) {
	return SaveScreenshotWithCaptureAndWait(ctx, opts, url, path, captureBeyondViewport, "")
}

// SaveScreenshotWithCaptureAndWait is SaveScreenshotWithCapture with an
// optional visible-selector gate before capture.
func SaveScreenshotWithCaptureAndWait(ctx context.Context, opts LaunchOptions, url, path string, captureBeyondViewport bool, waitSelector string) (string, error) {
	png, err := ScreenshotWithCaptureAndWait(ctx, opts, url, captureBeyondViewport, waitSelector)
	if err != nil {
		return "", err
	}
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("snapref-%d.png", time.Now().UnixNano()))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-screenshot-*.png")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(png); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return path, nil
}

// Base64Screenshot is a convenience for callers that want to embed the
// screenshot in JSON. Returns base64-encoded PNG.
func Base64Screenshot(ctx context.Context, opts LaunchOptions, url string) (string, error) {
	png, err := Screenshot(ctx, opts, url)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(png), nil
}

// validateURL returns nil if url is one chromedp can handle. Empty / non
// http(s) URLs would otherwise fail late with a confusing CDP error.
func validateURL(url string) error {
	if url == "" {
		return errors.New("url is required")
	}
	if len(url) < 7 {
		return errors.New("url must be absolute (http:// or https://)")
	}
	scheme := url[:7]
	if scheme != "http://" && len(url) >= 8 && url[:8] != "https://" {
		return errors.New("url must start with http:// or https://")
	}
	return nil
}
