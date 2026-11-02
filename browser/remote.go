package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// remoteCtx returns a chromedp context attached to the named session via
// RemoteAllocator. Caller must invoke both cancel funcs (alloc + task).
func remoteCtx(parent context.Context, s *Session) (context.Context, context.CancelFunc, context.CancelFunc, error) {
	if s.WSURL == "" {
		return nil, nil, nil, errors.New("session has no recorded ws_url; reopen it")
	}
	// IMPORTANT: parent here is typically a signal.NotifyContext that the
	// CLI cancels at exit (via `defer stop()`). chromedp 0.15.1's
	// RemoteAllocator + WithTargetID combination calls Target.closeTarget
	// on the attached page when ANY ancestor context cancels — which kills
	// the persistent session's page on every exec call. Symptom on the
	// next exec: "Failed to open new tab - no browser is open (-32000)".
	// Detach by giving chromedp a context.Background() that nothing
	// cancels. The signal context is still honored by callers' own
	// WithTimeout wrappers around chromedp.Run.
	allocCtx, _ := chromedp.NewRemoteAllocator(context.Background(), s.WSURL)
	opts := []chromedp.ContextOption{}
	if id, ok := ensurePageTargetID(parent, s.Port); ok {
		opts = append(opts, chromedp.WithTargetID(id))
	}
	taskCtx, _ := chromedp.NewContext(allocCtx, opts...)
	noop := func() {}
	return taskCtx, noop, noop, nil
}

type cdpTargetListEntry struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	DevToolsFrontendURL  string `json:"devtoolsFrontendUrl"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func fetchTargetList(port int) ([]cdpTargetListEntry, bool) {
	if port <= 0 {
		return nil, false
	}
	client := http.Client{
		Timeout: time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("CDP target list must not redirect")
		},
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json", port))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	var entries []cdpTargetListEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&entries); err != nil {
		return nil, false
	}
	return entries, true
}

func firstPageTargetID(port int) (target.ID, bool) {
	entries, ok := fetchTargetList(port)
	if !ok {
		return "", false
	}
	for _, entry := range entries {
		if entry.Type == "page" && entry.ID != "" {
			return target.ID(entry.ID), true
		}
	}
	return "", false
}

func ensurePageTargetID(ctx context.Context, port int) (target.ID, bool) {
	if port <= 0 {
		return "", false
	}
	if id, ok := firstPageTargetID(port); ok {
		return id, true
	}
	id, err := createDevToolsTarget(ctx, &Session{Port: port}, "about:blank", time.Second)
	if err != nil || id == "" {
		return firstPageTargetID(port)
	}
	return id, true
}

// firstPageWebSocketURL returns the per-page websocket debugger URL for the
// first attached `page` target. Using this URL directly avoids chromedp's
// Context lifecycle (which closes the target on cleanup) — useful for
// one-shot raw CDP calls that must preserve the page across exec invocations.
func firstPageWebSocketURL(port int) (string, bool) {
	entries, ok := fetchTargetList(port)
	if !ok {
		return "", false
	}
	for _, entry := range entries {
		if entry.Type == "page" && validateCDPWebSocketURL(entry.WebSocketDebuggerURL, port, "/devtools/page/") == nil {
			return entry.WebSocketDebuggerURL, true
		}
	}
	return "", false
}

func waitForPageWebSocketURL(ctx context.Context, port int, timeout time.Duration) (string, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if wsURL, ok := firstPageWebSocketURL(port); ok {
			return wsURL, true
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-deadline.C:
			return "", false
		case <-ticker.C:
		}
	}
}

// NewRemoteContext is the public form of remoteCtx — exposes the
// chromedp context to callers that want to keep one tab alive across
// multiple Navigate/Eval calls (e.g. skills.SessionPage). Caller must
// invoke both cancel funcs to release.
func NewRemoteContext(parent context.Context, s *Session) (context.Context, context.CancelFunc, context.CancelFunc, error) {
	return remoteCtx(parent, s)
}

// NavigateOn navigates the given chromedp tab context to url and waits
// for body. The timeout argument is honored via a goroutine + cancel so
// it doesn't cancel the underlying tab (chromedp ties tab lifetime to
// the first context it sees; wrapping taskCtx in WithTimeout would kill
// the tab when the wrapper expires).
func NavigateOn(parent context.Context, taskCtx context.Context, url string, timeout time.Duration) error {
	return runWithTimeout(parent, taskCtx, timeout, func(ctx context.Context) error {
		return chromedp.Run(ctx,
			// Disable the HTTP cache (like DevTools' "Disable cache") so navigate
			// always fetches fresh — dev servers recycle ports, and a stale cached
			// app on a reused port is a silent, confusing test failure.
			network.Enable(),
			network.SetCacheDisabled(true),
			chromedp.Navigate(url),
			chromedp.WaitReady("body", chromedp.ByQuery),
		)
	})
}

// clickGrace bounds how long ClickOn tolerates a present-but-hidden target
// before failing fast (covers a CSS-toggle/animation frame without blocking for
// the whole timeout on something that will never become clickable).
const clickGrace = 2 * time.Second

// clickableProbe reads whether selector currently matches an element (present)
// and whether that element is visible, in one cheap eval. Errors (e.g. a
// navigation in flight) are non-fatal — the caller falls through to chromedp.
func clickableProbe(ctx context.Context, selector string) (present, visible bool, err error) {
	sel, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return 0;
  const st = getComputedStyle(el);
  const vis = el.getClientRects().length > 0 && st.visibility !== 'hidden' && st.display !== 'none' && parseFloat(st.opacity || '1') > 0.01;
  return vis ? 2 : 1;
})()`, sel)
	var code int
	if e := chromedp.Run(ctx, chromedp.Evaluate(js, &code)); e != nil {
		return false, false, e
	}
	return code >= 1, code == 2, nil
}

// ClickOn clicks a CSS selector in an existing tab context. WaitVisible would
// otherwise block for the full timeout on a present-but-hidden target (e.g. a
// control hidden at the current viewport); we fast-fail that case with an
// actionable message while still waiting the full timeout for an element that
// simply hasn't been inserted yet.
func ClickOn(parent context.Context, taskCtx context.Context, selector string, timeout time.Duration) error {
	query, queryOption, err := targetQuery(selector)
	if err != nil {
		return err
	}
	actionTimeout := actionabilityTimeout(timeout)
	return runWithTimeout(parent, taskCtx, actionabilityRunTimeout(timeout), func(ctx context.Context) error {
		if err := waitForSelectorActionable(ctx, "click", selector, actionTimeout); err != nil {
			return err
		}
		return chromedp.Run(ctx, chromedp.Click(query, queryOption))
	})
}

func targetQuery(target string) (string, chromedp.QueryOption, error) {
	locator, nested, err := decodeTargetLocator(target)
	if err != nil {
		return "", nil, err
	}
	if !nested {
		return locator.Selector, chromedp.ByQuery, nil
	}
	expression, _, err := targetElementExpression(target)
	if err != nil {
		return "", nil, err
	}
	return expression, chromedp.ByJSPath, nil
}

// sleepCtx sleeps d or returns early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ClickSearchOn waits for and clicks the first node matching a DOM search query
// (XPath or CSS, via CDP DOM.performSearch — chromedp.BySearch), using a real
// CDP click so framework handlers fire exactly as for ClickOn.
func ClickSearchOn(parent context.Context, taskCtx context.Context, query string, timeout time.Duration) error {
	return runWithTimeout(parent, taskCtx, timeout, func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.WaitVisible(query, chromedp.BySearch),
			chromedp.Click(query, chromedp.BySearch),
		)
	})
}

// WaitSearchOn waits until the first node matching a DOM search query is visible.
func WaitSearchOn(parent context.Context, taskCtx context.Context, query string, timeout time.Duration) error {
	return runWithTimeout(parent, taskCtx, timeout, func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.WaitVisible(query, chromedp.BySearch))
	})
}

// ClickTextOn clicks the first button / link / role=button / submit input whose
// visible text (or value) contains the given text. Matching is a case-sensitive
// substring on normalized text.
func ClickTextOn(parent context.Context, taskCtx context.Context, text string, timeout time.Duration) error {
	lit := xpathLiteral(text)
	xpath := "(" +
		"//button[contains(normalize-space(.), " + lit + ")] | " +
		"//a[contains(normalize-space(.), " + lit + ")] | " +
		"//*[@role='button'][contains(normalize-space(.), " + lit + ")] | " +
		"//input[(@type='submit' or @type='button') and contains(@value, " + lit + ")]" +
		")[1]"
	return ClickSearchOn(parent, taskCtx, xpath, timeout)
}

// WaitTextOn waits until an element whose direct text contains the given text is
// visible — the read-only partner of ClickTextOn, for reactive content that
// renders after an async round-trip. Uses direct text() (not subtree string) so
// it targets the leaf element, not every ancestor that also contains the text.
func WaitTextOn(parent context.Context, taskCtx context.Context, text string, timeout time.Duration) error {
	xpath := "(//*[contains(text(), " + xpathLiteral(text) + ")])[1]"
	return WaitSearchOn(parent, taskCtx, xpath, timeout)
}

// xpathLiteral renders s as an XPath string literal, handling embedded quotes
// (XPath has no escape sequence, so a string with both quote kinds must use
// concat()).
func xpathLiteral(s string) string {
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	if !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	parts := strings.Split(s, "'")
	var b strings.Builder
	b.WriteString("concat(")
	for i, p := range parts {
		if i > 0 {
			b.WriteString(`, "'", `)
		}
		b.WriteString("'" + p + "'")
	}
	b.WriteString(")")
	return b.String()
}

const fillElementJS = `(function(payload) {
  const value = payload.value;
  const element = this;
  const view = element.ownerDocument && element.ownerDocument.defaultView;
  if (!view) throw new Error("matched element has no document window");
  if (element.readOnly) throw new Error("matched element is read-only");

  try {
    element.focus({preventScroll: true});
  } catch (_) {
    element.focus();
  }

  let actual;
  if (element instanceof view.HTMLInputElement || element instanceof view.HTMLTextAreaElement) {
    const prototype = element instanceof view.HTMLInputElement
      ? view.HTMLInputElement.prototype
      : view.HTMLTextAreaElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(prototype, "value").set;
    if (typeof setter !== "function") throw new Error("matched element has no native value setter");
    // Use the native prototype setter. React installs an instance setter to
    // track controlled fields; bypassing that tracker lets the bubbling input
    // event report the replacement to React instead of being ignored.
    setter.call(element, value);
    actual = element.value;
    if (actual !== value) {
      throw new Error("browser normalized the requested value to " + JSON.stringify(actual));
    }
    if (typeof element.setSelectionRange === "function") {
      try {
        element.setSelectionRange(value.length, value.length);
      } catch (_) {
        // Some input types expose setSelectionRange but do not support it.
      }
    }
  } else if (element.isContentEditable) {
    element.textContent = value;
    actual = element.textContent;
  } else {
    throw new Error("matched element is not an input, textarea, or contenteditable element");
  }

  let inputEvent;
  if (typeof view.InputEvent === "function") {
    try {
      inputEvent = new view.InputEvent("input", {
        bubbles: true,
        composed: true,
        data: value,
        inputType: "insertReplacementText"
      });
    } catch (_) {
      // Fall back for browser contexts that expose but cannot construct InputEvent.
    }
  }
  element.dispatchEvent(inputEvent || new view.Event("input", {bubbles: true, composed: true}));
  element.dispatchEvent(new view.Event("change", {bubbles: true, composed: true}));
  return actual === value;
})`

func fillNode(ctx context.Context, node *cdp.Node, text string) error {
	object, err := dom.ResolveNode().WithNodeID(node.NodeID).Do(ctx)
	if err != nil {
		return fmt.Errorf("resolve type target: %w", err)
	}
	defer func() { _ = runtime.ReleaseObject(object.ObjectID).Do(ctx) }()

	var filled bool
	err = chromedp.CallFunctionOn(fillElementJS, &filled,
		func(params *runtime.CallFunctionOnParams) *runtime.CallFunctionOnParams {
			return params.WithObjectID(object.ObjectID)
		},
		struct {
			Value string `json:"value"`
		}{Value: text},
	).Do(ctx)
	if err != nil {
		return fmt.Errorf("fill type target: %w", err)
	}
	if !filled {
		return errors.New("fill type target: value was not replaced")
	}
	return nil
}

// TypeOn replaces the content of an editable selector in an existing tab
// context and emits bubbling input and change events.
func TypeOn(parent context.Context, taskCtx context.Context, selector, text string, timeout time.Duration) error {
	query, queryOption, err := targetQuery(selector)
	if err != nil {
		return err
	}
	actionTimeout := actionabilityTimeout(timeout)
	return runWithTimeout(parent, taskCtx, actionabilityRunTimeout(timeout), func(ctx context.Context) error {
		if err := waitForSelectorActionable(ctx, "type", selector, actionTimeout); err != nil {
			return err
		}
		return chromedp.Run(ctx, chromedp.QueryAfter(query,
			func(ctx context.Context, _ runtime.ExecutionContextID, nodes ...*cdp.Node) error {
				if len(nodes) == 0 {
					return fmt.Errorf("selector %q did not return any nodes", selector)
				}
				return fillNode(ctx, nodes[0], text)
			},
			queryOption,
		))
	})
}

// namedKeys maps the key names agents naturally pass to `press` to the chromedp
// keyboard runes that synthesize a REAL key event. Without it,
// chromedp.KeyEvent("Escape") types the literal text "Escape" rather than
// pressing Esc — a footgun surfaced dogfooding the snapshot→act loop (e.g.
// closing a modal in a smoke). Names are matched case-insensitively; anything
// not here (single chars, arbitrary text) falls through to literal KeyEvent, so
// `press a` still types "a".
// namedKeys values are chromedp/kb constants — untyped string runes like
// "\r" (Enter) / "\x1b" (Escape) that synthesize a real key event.
var namedKeys = map[string]string{
	"enter": kb.Enter, "return": kb.Enter,
	"tab":    kb.Tab,
	"escape": kb.Escape, "esc": kb.Escape,
	"backspace": kb.Backspace,
	"delete":    kb.Delete, "del": kb.Delete,
	"space": " ", "spacebar": " ",
	"arrowup": kb.ArrowUp, "up": kb.ArrowUp,
	"arrowdown": kb.ArrowDown, "down": kb.ArrowDown,
	"arrowleft": kb.ArrowLeft, "left": kb.ArrowLeft,
	"arrowright": kb.ArrowRight, "right": kb.ArrowRight,
	"home": kb.Home, "end": kb.End,
	"pageup": kb.PageUp, "pagedown": kb.PageDown,
	"insert": kb.Insert,
}

// keyEventArg resolves a `press` argument to the string chromedp.KeyEvent should
// send: a named key (Enter/Tab/Escape/ArrowDown/…) becomes its real key rune;
// everything else passes through literally.
func keyEventArg(key string) string {
	if s, ok := namedKeys[strings.ToLower(strings.TrimSpace(key))]; ok {
		return s
	}
	return key
}

// PressOn sends a keyboard event to the focused element in an existing tab.
func PressOn(parent context.Context, taskCtx context.Context, key string, timeout time.Duration) error {
	actionTimeout := actionabilityTimeout(timeout)
	return runWithTimeout(parent, taskCtx, actionabilityRunTimeout(timeout), func(ctx context.Context) error {
		if err := waitForFocusedActionable(ctx, "press", actionTimeout); err != nil {
			return err
		}
		return chromedp.Run(ctx, chromedp.KeyEvent(keyEventArg(key)))
	})
}

// WaitVisibleOn waits for a CSS selector to become visible.
func WaitVisibleOn(parent context.Context, taskCtx context.Context, selector string, timeout time.Duration) error {
	query, queryOption, err := targetQuery(selector)
	if err != nil {
		return err
	}
	return runWithTimeout(parent, taskCtx, timeout, func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.WaitVisible(query, queryOption))
	})
}

// TextOn reads visible text from a CSS selector.
func TextOn(parent context.Context, taskCtx context.Context, selector string, timeout time.Duration) (string, error) {
	query, queryOption, err := targetQuery(selector)
	if err != nil {
		return "", err
	}
	var text string
	err = runWithTimeout(parent, taskCtx, timeout, func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Text(query, &text, queryOption))
	})
	return text, err
}

// EvaluateOn evaluates js in the given chromedp tab context. See the
// NavigateOn docs for why we don't WithTimeout the tab context.
func EvaluateOn(parent context.Context, taskCtx context.Context, js string, out any, timeout time.Duration) error {
	return runWithTimeout(parent, taskCtx, timeout, func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Evaluate(js, out))
	})
}

// runWithTimeout invokes fn(taskCtx) and returns its error or
// context.DeadlineExceeded if the timeout fires. It does NOT wrap
// taskCtx, so the chromedp tab is preserved across calls.
func runWithTimeout(parent context.Context, taskCtx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	done := make(chan error, 1)
	go func() { done <- fn(taskCtx) }()
	select {
	case err := <-done:
		return err
	case <-parent.Done():
		return parent.Err()
	case <-time.After(timeout):
		return fmt.Errorf("timeout after %s", timeout)
	}
}

func cancelRemoteTarget(_ context.Context, cancel context.CancelFunc) context.CancelFunc {
	return cancel
}

// RemoteScreenshot navigates the session to url and returns a full-page PNG.
func RemoteScreenshot(ctx context.Context, s *Session, url string, timeout time.Duration) ([]byte, error) {
	return RemoteScreenshotWithCapture(ctx, s, url, timeout, true)
}

// RemoteScreenshotWithCapture navigates the session to url and returns a PNG.
// When captureBeyondViewport is false, Chrome captures only the active viewport.
func RemoteScreenshotWithCapture(ctx context.Context, s *Session, url string, timeout time.Duration, captureBeyondViewport bool) ([]byte, error) {
	return RemoteScreenshotWithCaptureAndWait(ctx, s, url, timeout, captureBeyondViewport, "")
}

// RemoteScreenshotWithCaptureAndWait is RemoteScreenshotWithCapture plus an
// optional visible-selector gate before capture.
func RemoteScreenshotWithCaptureAndWait(ctx context.Context, s *Session, url string, timeout time.Duration, captureBeyondViewport bool, waitSelector string) ([]byte, error) {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return nil, err
	}
	defer cancelAlloc()
	defer cancelTask()
	tctx, tcancel := context.WithTimeout(taskCtx, timeout)
	defer tcancel()

	// Empty url means "capture current page" — needed for multi-step flows
	// in a persistent session where intermediate state must be preserved
	// across screenshots. Mirrors the same convention used by RemoteEval.
	actions := []chromedp.Action{}
	if url != "" {
		actions = append(actions, chromedp.Navigate(url))
	}
	actions = append(actions, chromedp.WaitReady("body", chromedp.ByQuery))
	if waitSelector != "" {
		actions = append(actions, chromedp.WaitVisible(waitSelector, chromedp.ByQuery))
	}
	var buf []byte
	actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
		data, err := page.CaptureScreenshot().
			WithCaptureBeyondViewport(captureBeyondViewport).
			WithFromSurface(true).
			Do(ctx)
		if err != nil {
			return err
		}
		buf = data
		return nil
	}))
	if err := chromedp.Run(tctx, actions...); err != nil {
		return nil, err
	}
	return buf, nil
}

// RemoteEval evaluates a JS expression in the session's first tab,
// navigating first if url is non-empty.
func RemoteEval(ctx context.Context, s *Session, url, js string, out any, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	// Raw CDP (not chromedp) so the session's page target survives this call —
	// see remote_raw.go. The chromedp path intermittently closed the tab on exit.
	return rawEvalInto(ctx, s, url, js, false, out, timeout)
}

// RemoteNavigate navigates the session's first tab and waits for body. Raw CDP
// (see remote_raw.go) so the page target is preserved across exec calls.
func RemoteNavigate(ctx context.Context, s *Session, url string, timeout time.Duration, opts ...NavigateOptions) error {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return rawNavigate(ctx, s, url, timeout, opts...)
}

// RemoteSetViewport applies a viewport override to the persistent session's
// current page. It does not resize the OS-level Chrome window; it controls the
// page metrics used by layout and screenshots.
func RemoteSetViewport(ctx context.Context, s *Session, vp Viewport, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return rawSetViewport(ctx, s, vp, timeout)
}

// RemoteClick clicks a CSS selector in the session's first tab.
func RemoteClick(ctx context.Context, s *Session, selector string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return ClickOn(ctx, taskCtx, selector, timeout)
}

// RemoteClickText clicks the first clickable element whose visible text contains
// text, in the session's first tab.
func RemoteClickText(ctx context.Context, s *Session, text string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return ClickTextOn(ctx, taskCtx, text, timeout)
}

// RemoteWaitText waits until an element whose direct text contains text is
// visible, in the session's first tab.
func RemoteWaitText(ctx context.Context, s *Session, text string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return WaitTextOn(ctx, taskCtx, text, timeout)
}

// RemoteClickXPath clicks the first element matching a raw XPath, in the
// session's first tab.
func RemoteClickXPath(ctx context.Context, s *Session, xpath string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return ClickSearchOn(ctx, taskCtx, xpath, timeout)
}

// RemoteWaitXPath waits until the first element matching a raw XPath is visible.
func RemoteWaitXPath(ctx context.Context, s *Session, xpath string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return WaitSearchOn(ctx, taskCtx, xpath, timeout)
}

// RemoteType sends text to a CSS selector in the session's first tab.
func RemoteType(ctx context.Context, s *Session, selector, text string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return TypeOn(ctx, taskCtx, selector, text, timeout)
}

// RemotePress sends a keyboard event to the focused element in the session's first tab.
func RemotePress(ctx context.Context, s *Session, key string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return PressOn(ctx, taskCtx, key, timeout)
}

// RemoteWaitVisible waits for a selector in the session's first tab.
func RemoteWaitVisible(ctx context.Context, s *Session, selector string, timeout time.Duration) error {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return err
	}
	defer cancelAlloc()
	defer cancelTask()
	return WaitVisibleOn(ctx, taskCtx, selector, timeout)
}

// RemoteText returns visible text for a selector in the session's first tab.
func RemoteText(ctx context.Context, s *Session, selector string, timeout time.Duration) (string, error) {
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return "", err
	}
	defer cancelAlloc()
	defer cancelTask()
	return TextOn(ctx, taskCtx, selector, timeout)
}

// RemoteFetchHTML returns the rendered HTML at url for the session.
func RemoteFetchHTML(ctx context.Context, s *Session, url string, timeout time.Duration) (string, error) {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	taskCtx, cancelAlloc, cancelTask, err := remoteCtx(ctx, s)
	if err != nil {
		return "", err
	}
	defer cancelAlloc()
	defer cancelTask()
	tctx, tcancel := context.WithTimeout(taskCtx, timeout)
	defer tcancel()

	var html string
	if err := chromedp.Run(tctx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	); err != nil {
		return "", err
	}
	return html, nil
}

// RemoteSaveScreenshot is the file-writing convenience for sessions.
func RemoteSaveScreenshot(ctx context.Context, s *Session, url, path string) (string, error) {
	return RemoteSaveScreenshotWithCapture(ctx, s, url, path, true)
}

// RemoteSaveScreenshotWithCapture is RemoteSaveScreenshot with explicit
// full-page vs viewport-only capture control.
func RemoteSaveScreenshotWithCapture(ctx context.Context, s *Session, url, path string, captureBeyondViewport bool) (string, error) {
	return RemoteSaveScreenshotWithCaptureAndWait(ctx, s, url, path, captureBeyondViewport, "")
}

// RemoteSaveScreenshotWithCaptureAndWait is RemoteSaveScreenshotWithCapture
// plus an optional visible-selector gate before capture.
func RemoteSaveScreenshotWithCaptureAndWait(ctx context.Context, s *Session, url, path string, captureBeyondViewport bool, waitSelector string) (string, error) {
	png, err := RemoteScreenshotWithCaptureAndWait(ctx, s, url, 0, captureBeyondViewport, waitSelector)
	if err != nil {
		return "", err
	}
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("snapref-%s-%d.png", s.Name, time.Now().UnixNano()))
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
	return path, os.Rename(tmpName, path)
}

// RemoteEvalBase64 returns a base64-encoded PNG (used by callers that
// want to embed in JSON).
func RemoteEvalBase64(ctx context.Context, s *Session, url string) (string, error) {
	png, err := RemoteScreenshot(ctx, s, url, 0)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(png), nil
}

// RemoteScreenshotCurrent captures the current page state via a one-shot
// raw CDP call, bypassing chromedp's Context lifecycle entirely. The
// chromedp Context + RemoteAllocator combination calls Target.closeTarget
// on the attached page during process-exit cleanup, which kills the
// persistent session's page. Dialing the page's per-target websocket URL
// directly and sending Page.captureScreenshot avoids that lifecycle —
// the connection closes when the process exits but no detach/close
// commands fire. Use this verb (`exec <name> screenshot-current`) for
// multi-step flows where prior state must be preserved.
func RemoteScreenshotCurrent(s *Session, captureBeyondViewport bool) ([]byte, error) {
	return RemoteScreenshotCurrentWithTimeout(s, captureBeyondViewport, 60*time.Second)
}

const screenshotPresentedFrameJS = `new Promise(resolve => {
  let complete = false;
  let timer;
  const finish = () => {
    if (complete) return;
    complete = true;
    clearTimeout(timer);
    resolve(1);
  };
  timer = setTimeout(finish, 250);
  if (typeof requestAnimationFrame === "function") {
    requestAnimationFrame(() => requestAnimationFrame(finish));
  }
})`

// RemoteScreenshotCurrentWithTimeout captures the current page state with a
// hard timeout around the raw CDP screenshot call. Some Chrome builds can leave
// Page.captureScreenshot waiting indefinitely; on timeout we close the raw
// transport asynchronously and return control to the caller.
func RemoteScreenshotCurrentWithTimeout(s *Session, captureBeyondViewport bool, timeout time.Duration) ([]byte, error) {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	wsURL, ok := firstPageWebSocketURL(s.Port)
	if !ok {
		return nil, errors.New("no page target on session — open or navigate first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := chromedp.DialContext(ctx, wsURL)
	if err != nil {
		return nil, fmt.Errorf("dial page websocket: %w", err)
	}
	defer closeCDPTransportAsync(conn)

	var id int64
	next := func() int64 { id++; return id }

	// Activate the target and force a fresh composited frame before capturing.
	// fromSurface captures the last *presented* compositor surface, which on an
	// idle headless tab can predate JS-mutated DOM (a menu opened after load) —
	// the classic "black/stale screenshot". bringToFront + two rAFs guarantee the
	// renderer presents the current DOM. Errors are non-fatal (best effort).
	_, _ = cdpCall(ctx, conn, next(), "Page.bringToFront", nil)
	_, _ = cdpCall(ctx, conn, next(), "Runtime.evaluate", map[string]any{
		"expression":   screenshotPresentedFrameJS,
		"awaitPromise": true,
	})

	// Both capture modes use explicit CSS-pixel clips when layout metrics are
	// available. This avoids device-scale-factor drift for viewport evidence and
	// makes full-page capture include the entire content height. Chrome ignores
	// captureBeyondViewport with fromSurface:false in some builds; bringToFront +
	// two rAFs above keep the compositor surface fresh. The live-renderer fallback
	// remains for older/failed metrics calls.
	params := screenshotCaptureParams(captureBeyondViewport, nil, nil)
	if metricsRaw, metricsErr := cdpCall(ctx, conn, next(), "Page.getLayoutMetrics", nil); metricsErr == nil {
		var metrics struct {
			CSSContentSize    *pageContentSize `json:"cssContentSize"`
			ContentSize       *pageContentSize `json:"contentSize"`
			CSSVisualViewport *pageViewport    `json:"cssVisualViewport"`
			CSSLayoutViewport *pageViewport    `json:"cssLayoutViewport"`
		}
		if json.Unmarshal(metricsRaw, &metrics) == nil {
			size := metrics.CSSContentSize
			if size == nil {
				size = metrics.ContentSize
			}
			viewport := metrics.CSSVisualViewport
			if viewport == nil {
				viewport = metrics.CSSLayoutViewport
			}
			params = screenshotCaptureParams(captureBeyondViewport, size, viewport)
		}
	}

	type captureResult struct {
		res json.RawMessage
		err error
	}
	done := make(chan captureResult, 1)
	go func() {
		res, err := cdpCall(ctx, conn, next(), "Page.captureScreenshot", params)
		done <- captureResult{res: res, err: err}
	}()

	var res json.RawMessage
	select {
	case got := <-done:
		if got.err != nil {
			return nil, got.err
		}
		res = got.res
	case <-ctx.Done():
		closeCDPTransportAsync(conn)
		return nil, fmt.Errorf("capture screenshot timeout after %s: %w", timeout, ctx.Err())
	}
	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &result); err != nil {
		return nil, fmt.Errorf("unmarshal captureScreenshot result: %w", err)
	}
	return base64.StdEncoding.DecodeString(result.Data)
}

type pageContentSize struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type pageViewport struct {
	PageX        float64 `json:"pageX"`
	PageY        float64 `json:"pageY"`
	ClientWidth  float64 `json:"clientWidth"`
	ClientHeight float64 `json:"clientHeight"`
}

func screenshotCaptureParams(captureBeyondViewport bool, size *pageContentSize, viewport *pageViewport) map[string]any {
	params := map[string]any{
		"captureBeyondViewport": captureBeyondViewport,
		"fromSurface":           false,
	}
	var x, y, width, height float64
	if captureBeyondViewport && size != nil {
		x, y, width, height = size.X, size.Y, size.Width, size.Height
	} else if !captureBeyondViewport && viewport != nil {
		x, y, width, height = viewport.PageX, viewport.PageY, viewport.ClientWidth, viewport.ClientHeight
	}
	if width <= 0 || height <= 0 {
		return params
	}
	params["fromSurface"] = true
	params["clip"] = map[string]any{
		"x":      x,
		"y":      y,
		"width":  math.Ceil(width),
		"height": math.Ceil(height),
		"scale":  1,
	}
	return params
}

func closeCDPTransportAsync(conn interface{ Close() error }) {
	done := make(chan struct{})
	go func() {
		_ = conn.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// RemoteSaveScreenshotCurrent is the file-writing convenience for
// RemoteScreenshotCurrent — same temp+rename atomicity as
// RemoteSaveScreenshot, just sourced from the no-close path.
func RemoteSaveScreenshotCurrent(s *Session, path string) (string, error) {
	return RemoteSaveScreenshotCurrentWithCapture(s, path, true)
}

// RemoteSaveScreenshotCurrentWithCapture is RemoteSaveScreenshotCurrent with
// explicit full-page vs viewport-only capture control.
func RemoteSaveScreenshotCurrentWithCapture(s *Session, path string, captureBeyondViewport bool) (string, error) {
	return RemoteSaveScreenshotCurrentWithCaptureTimeout(s, path, captureBeyondViewport, 60*time.Second)
}

// RemoteSaveScreenshotCurrentWithCaptureTimeout is
// RemoteSaveScreenshotCurrentWithCapture with caller-controlled timeout.
func RemoteSaveScreenshotCurrentWithCaptureTimeout(s *Session, path string, captureBeyondViewport bool, timeout time.Duration) (string, error) {
	png, err := RemoteScreenshotCurrentWithTimeout(s, captureBeyondViewport, timeout)
	if err != nil {
		return "", err
	}
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("snapref-%s-%d.png", s.Name, time.Now().UnixNano()))
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
	return path, os.Rename(tmpName, path)
}

// EvalInTarget evaluates js inside a specific CDP target (e.g. an
// extension service worker). Used by Slice D's web-companion e2e to
// drive the extension without going through a popup.
func EvalInTarget(ctx context.Context, s *Session, targetURLPrefix string, js string, out any, timeout time.Duration) error {
	return rawEvalIntoTarget(ctx, s, "", targetURLPrefix, js, false, out, timeout)
}
