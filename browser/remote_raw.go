package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"
)

// Raw-CDP session ops. chromedp's RemoteAllocator + WithTargetID combination
// intermittently closes the session's page target when an exec process exits —
// the "no page target on session" failure, which also yields blank/black
// screenshots (the next op recreates an empty about:blank, or a stale capture is
// taken). Dialing the page's own websocket and speaking CDP directly never
// touches that lifecycle, so the tab survives across exec calls. This is the
// pattern RemoteScreenshotCurrent already used; eval + navigate now share it, so
// a navigate→eval→eval→screenshot flow keeps one stable, paint-fresh tab.

// cdpTransport is the subset of chromedp's dialed connection we use.
type cdpTransport interface {
	Write(context.Context, *cdproto.Message) error
	Read(context.Context, *cdproto.Message) error
}

// dialFirstPage opens a raw CDP connection to the session's first page target.
func dialFirstPage(ctx context.Context, s *Session) (interface {
	cdpTransport
	Close() error
}, error) {
	wsURL, ok := firstPageWebSocketURL(s.Port)
	if !ok {
		if _, ensured := ensurePageTargetID(ctx, s.Port); ensured {
			wsURL, ok = waitForPageWebSocketURL(ctx, s.Port, time.Second)
		}
		if !ok {
			return nil, errors.New("no page target on session — open or navigate first")
		}
	}
	conn, err := chromedp.DialContext(ctx, wsURL)
	if err != nil {
		return nil, fmt.Errorf("dial page websocket: %w", err)
	}
	return conn, nil
}

// cdpCall sends one CDP command and returns its result, draining interleaved
// events/other responses until the matching id arrives.
func cdpCall(ctx context.Context, t cdpTransport, id int64, method string, params map[string]any) (json.RawMessage, error) {
	var pb []byte
	if params != nil {
		pb, _ = json.Marshal(params)
	}
	msg := &cdproto.Message{ID: id, Method: cdproto.MethodType(method), Params: jsontext.Value(pb)}
	if err := t.Write(ctx, msg); err != nil {
		return nil, fmt.Errorf("send %s: %w", method, err)
	}
	for {
		in := new(cdproto.Message)
		if err := t.Read(ctx, in); err != nil {
			return nil, fmt.Errorf("read %s response: %w", method, err)
		}
		if in.ID != id {
			continue
		}
		if in.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, in.Error.Message)
		}
		return json.RawMessage(in.Result), nil
	}
}

// rawEvalValue runs a JS expression in the session's page (navigating first when
// url is non-empty) and returns the JSON value of the result. await resolves a
// returned Promise. Mirrors the chromedp Evaluate/EvalPromise contract.
func rawEvalValue(ctx context.Context, s *Session, url, js string, await bool, timeout time.Duration) (json.RawMessage, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialFirstPage(cctx, s)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var id int64
	next := func() int64 { id++; return id }

	if url != "" {
		if err := rawNavigateOnConn(cctx, conn, next, url, NavigateOptions{}, timeout); err != nil {
			return nil, err
		}
	}

	res, err := cdpCall(cctx, conn, next(), "Runtime.evaluate", map[string]any{
		"expression":    js,
		"returnByValue": true,
		"awaitPromise":  await,
		"userGesture":   true,
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, fmt.Errorf("decode eval result: %w", err)
	}
	if r.ExceptionDetails != nil {
		msg := r.ExceptionDetails.Text
		if r.ExceptionDetails.Exception != nil && r.ExceptionDetails.Exception.Description != "" {
			msg = r.ExceptionDetails.Exception.Description
		}
		return nil, fmt.Errorf("eval threw: %s", msg)
	}
	return r.Result.Value, nil
}

// rawEvalInto runs rawEvalValue and decodes the JSON value into out.
func rawEvalInto(ctx context.Context, s *Session, url, js string, await bool, out any, timeout time.Duration) error {
	val, err := rawEvalValue(ctx, s, url, js, await, timeout)
	if err != nil {
		return err
	}
	if out == nil || len(val) == 0 {
		return nil // no value (e.g. a Promise without --await) — leave out at zero
	}
	return json.Unmarshal(val, out)
}

// rawEvalIntoTarget evaluates against an existing target selected by type and
// URL prefix without giving chromedp ownership of that target. A chromedp
// context created with WithTargetID closes the attached target on cancellation;
// direct websocket CDP preserves extension pages and service workers across
// multiple acceptance steps.
func rawEvalIntoTarget(ctx context.Context, s *Session, targetType, targetURLPrefix, js string, await bool, out any, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	entries, ok := fetchTargetList(s.Port)
	if !ok {
		return fmt.Errorf("list CDP targets on port %d", s.Port)
	}
	wsURL := ""
	for _, entry := range entries {
		if targetType != "" && entry.Type != targetType {
			continue
		}
		if strings.HasPrefix(entry.URL, targetURLPrefix) && entry.WebSocketDebuggerURL != "" {
			wsURL = entry.WebSocketDebuggerURL
			break
		}
	}
	if wsURL == "" {
		return fmt.Errorf("no %s target with URL prefix %q", targetType, targetURLPrefix)
	}
	conn, err := chromedp.DialContext(cctx, wsURL)
	if err != nil {
		return fmt.Errorf("dial target websocket: %w", err)
	}
	defer conn.Close()

	res, err := cdpCall(cctx, conn, 1, "Runtime.evaluate", map[string]any{
		"expression": js, "returnByValue": true, "awaitPromise": await, "userGesture": true,
	})
	if err != nil {
		return err
	}
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &result); err != nil {
		return fmt.Errorf("decode target eval result: %w", err)
	}
	if result.ExceptionDetails != nil {
		msg := result.ExceptionDetails.Text
		if result.ExceptionDetails.Exception != nil && result.ExceptionDetails.Exception.Description != "" {
			msg = result.ExceptionDetails.Exception.Description
		}
		return fmt.Errorf("target eval threw: %s", msg)
	}
	if out == nil || len(result.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(result.Result.Value, out)
}

// rawNavigate navigates the session's page and waits for body, via raw CDP.
func rawNavigate(ctx context.Context, s *Session, url string, timeout time.Duration, opts ...NavigateOptions) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	var navOpts NavigateOptions
	if len(opts) > 0 {
		navOpts = opts[0]
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialFirstPage(cctx, s)
	if err != nil {
		return err
	}
	defer conn.Close()
	var id int64
	next := func() int64 { id++; return id }
	return rawNavigateOnConn(cctx, conn, next, url, navOpts, timeout)
}

func rawNavigateOnConn(ctx context.Context, conn cdpTransport, next func() int64, url string, opts NavigateOptions, timeout time.Duration) error {
	if opts.BypassCache {
		if _, err := cdpCall(ctx, conn, next(), "Network.enable", nil); err != nil {
			return err
		}
		if _, err := cdpCall(ctx, conn, next(), "Network.setCacheDisabled", map[string]any{"cacheDisabled": true}); err != nil {
			return err
		}
		defer func() {
			_, _ = cdpCall(ctx, conn, next(), "Network.setCacheDisabled", map[string]any{"cacheDisabled": false})
		}()
	}
	if url != "" {
		if _, err := cdpCall(ctx, conn, next(), "Page.navigate", map[string]any{"url": url}); err != nil {
			return err
		}
	}
	if err := waitForDocumentReady(ctx, conn, next, timeout); err != nil {
		return err
	}
	if opts.Reload {
		if _, err := cdpCall(ctx, conn, next(), "Page.reload", map[string]any{"ignoreCache": opts.BypassCache}); err != nil {
			return err
		}
		if err := waitForDocumentReady(ctx, conn, next, timeout); err != nil {
			return err
		}
	}
	return nil
}

func waitForDocumentReady(ctx context.Context, conn cdpTransport, next func() int64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := error(nil)
	for time.Now().Before(deadline) {
		res, err := cdpCall(ctx, conn, next(), "Runtime.evaluate", map[string]any{
			"expression": "document.readyState", "returnByValue": true,
		})
		if err == nil {
			var r struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			}
			if json.Unmarshal(res, &r) == nil && (r.Result.Value == "interactive" || r.Result.Value == "complete") {
				return nil
			}
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if last != nil {
		return fmt.Errorf("document not ready within %s: %w", timeout, last)
	}
	return fmt.Errorf("document not ready within %s", timeout)
}

// rawSetViewport applies a page-level viewport override without involving
// chromedp's target lifecycle. It is intentionally paired with the raw CDP
// navigate/screenshot path used by persistent sessions.
func rawSetViewport(ctx context.Context, s *Session, vp Viewport, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if vp.Width <= 0 || vp.Height <= 0 {
		return fmt.Errorf("viewport dimensions must be positive")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialFirstPage(cctx, s)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = cdpCall(cctx, conn, 1, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             vp.Width,
		"height":            vp.Height,
		"deviceScaleFactor": 1,
		"mobile":            false,
		"scale":             1,
		"screenWidth":       vp.Width,
		"screenHeight":      vp.Height,
	})
	return err
}
