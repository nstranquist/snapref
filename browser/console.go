package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/go-json-experiment/json/jsontext"
)

// Console capture — native CDP console/exception/log capture for a persistent
// session, so a verification harness can assert on client-side errors directly
// instead of injecting a window.onerror shim into the page (the workaround the
// garrid-console UI harness had to use before this existed).
//
// Like RemoteScreenshotCurrent / rawEvalValue, this dials the page's own
// websocket and speaks CDP directly rather than going through chromedp's
// Context lifecycle, so the session's tab survives the call. The difference
// from those one-shot request/response helpers is that console events arrive
// asynchronously: we enable the Runtime + Log domains, optionally fire a
// trigger (navigate / reload / eval), then read every frame for a settle
// window, collecting the events instead of draining them.
//
// CDP does NOT retain Runtime.consoleAPICalled / Runtime.exceptionThrown from
// before a client connected, so capture is only reliable for events that fire
// while we are listening. That is why the primary, deterministic mode is
// "trigger + capture" in a single call (navigate fresh, or eval an interaction,
// and capture the console output it produces). The two compose: with --eval
// after --navigate or --reload, the capture loads the page, waits for its load
// event, then evaluates the interaction, so one call covers "load, then act".
//
// A capture must never be falsely clean. The trigger responses are read, not
// drained: a navigation Chrome could not complete (Page.navigate errorText)
// returns *ConsoleNavigateError, an --eval that threw or rejected returns
// *ConsoleEvalError, and a load that never fired before a queued --eval
// returns *ConsoleLoadTimeoutError. Each comes back with the entries collected.
//
// One subtlety: Runtime.enable replays the *currently loaded* page's buffered
// console, and we enable before the navigation commits — so a --navigate /
// --reload would otherwise report the outgoing page's history as the new page's
// errors. Runtime.executionContextsCleared fires when navigation tears down the
// old contexts; we reset the collected set on it, so a per-load capture reflects
// only the page it loaded. In the passive (no-trigger) and --eval modes no
// navigation occurs, so the replay surfaces the page's current console state
// (plus the Log domain's buffered network / CSP / deprecation entries).

// ConsoleEntry is one normalized console / exception / log / csp record.
type ConsoleEntry struct {
	Kind   string  `json:"kind"`             // "console" | "exception" | "log" | "csp"
	Level  string  `json:"level"`            // "log" | "debug" | "info" | "warning" | "error"
	Text   string  `json:"text"`             // joined args / exception description / log text
	URL    string  `json:"url,omitempty"`    // source URL when known
	Line   int64   `json:"line,omitempty"`   // 1-based line in source when known
	Source string  `json:"source,omitempty"` // Log domain source (network/javascript/…); empty for console/exception
	TS     float64 `json:"ts,omitempty"`     // CDP monotonic timestamp
	// CSP carries the typed violation for Kind == "csp" (Audits domain).
	CSP *CSPViolation `json:"csp,omitempty"`
}

// CSPViolation is one Content-Security-Policy issue as Chrome's Audits domain
// reports it (Audits.issueAdded, code ContentSecurityPolicyIssue). Unlike the
// free-text Log entry ("Applying inline style violates ..."), it is typed, and
// it reports report-only violations with ReportOnly set, so a
// Content-Security-Policy-Report-Only soak can be gated deterministically
// without waiting for the browser's batched Reporting API upload.
type CSPViolation struct {
	Directive     string `json:"directive"`             // violated directive, e.g. "style-src-elem"
	ViolationType string `json:"violation_type"`        // "inline" | "eval" | "url" | "trusted-types-sink" | ...
	BlockedURL    string `json:"blocked_url,omitempty"` // for URL violations
	ReportOnly    bool   `json:"report_only"`           // true under Content-Security-Policy-Report-Only
	SourceURL     string `json:"source_url,omitempty"`  // script that caused it, when known
	Line          int64  `json:"line,omitempty"`        // 1-based
	Column        int64  `json:"column,omitempty"`      // 1-based
}

// ConsoleKinds is the closed --kind vocabulary shared by the CLI, MCP, and
// browserscript surfaces.
var ConsoleKinds = []string{"all", "js", "log", "csp"}

// ValidConsoleKind reports whether k is "" (default all) or one of ConsoleKinds.
func ValidConsoleKind(k string) bool {
	if k == "" {
		return true
	}
	for _, v := range ConsoleKinds {
		if k == v {
			return true
		}
	}
	return false
}

// ConsoleCaptureOptions controls a single capture call.
type ConsoleCaptureOptions struct {
	Navigate string // if set, Page.navigate here before capturing (captures full load)
	Reload   bool   // if set, Page.reload before capturing (captures load of current URL)
	// Eval, if set, is Runtime.evaluate'd to drive an interaction. Alone it runs
	// at once; after Navigate or Reload it runs when the new page's load event
	// fires (load, then interact).
	Eval string
	// Window is how long to listen after the trigger (default 1.5s). With an
	// Eval it starts when the eval settles, so output after the eval's awaits
	// and a late rejection are both part of the capture.
	Window time.Duration
	Level  string // "all" | "warning" | "error" — minimum level to keep (default "all")
	Kind   string // "all" (default) | "js" (console+exception) | "log" (Log-domain only) | "csp" (typed CSP issues)
}

// Validate checks one capture request. Navigate and Reload both load the page
// and are exclusive; Eval may stand alone or follow either one. The CLI, MCP,
// and script surfaces all call this, so the rules cannot drift between them.
func (o ConsoleCaptureOptions) Validate() error {
	if o.Navigate != "" && o.Reload {
		return errors.New("navigate and reload are exclusive (eval may follow either)")
	}
	switch o.Level {
	case "", "all", "warning", "warn", "error":
	default:
		return fmt.Errorf("level must be all|warning|error (got %q)", o.Level)
	}
	if !ValidConsoleKind(o.Kind) {
		return fmt.Errorf("kind must be %s (got %q)", strings.Join(ConsoleKinds, "|"), o.Kind)
	}
	if o.Window < 0 {
		return fmt.Errorf("duration must not be negative (got %s)", o.Window)
	}
	return nil
}

// ConsoleEvalError reports that the Eval expression threw, or that the promise
// it returned rejected. The interaction it was meant to drive did not happen,
// so the entries returned with it are not evidence of a clean page.
type ConsoleEvalError struct {
	Description string
}

func (e *ConsoleEvalError) Error() string { return "console --eval threw: " + e.Description }

// ConsoleNavigateError reports a navigation Chrome could not complete, such as
// net::ERR_CONNECTION_REFUSED. Chrome then shows its own error page, so a
// capture of it would report an unreachable target as clean.
type ConsoleNavigateError struct {
	URL       string
	ErrorText string
}

func (e *ConsoleNavigateError) Error() string {
	return fmt.Sprintf("console --navigate %s failed: %s", e.URL, e.ErrorText)
}

// ConsoleLoadTimeoutError reports that the page load never finished, so an
// Eval queued behind it never ran.
type ConsoleLoadTimeoutError struct {
	Timeout time.Duration
}

func (e *ConsoleLoadTimeoutError) Error() string {
	return fmt.Sprintf("console: the page load did not finish within %s, so --eval did not run", e.Timeout)
}

// ConsoleEvalTimeoutError reports an Eval whose promise did not settle in
// time. The interaction's outcome is unknown, so the capture is not clean.
type ConsoleEvalTimeoutError struct {
	Timeout time.Duration
}

func (e *ConsoleEvalTimeoutError) Error() string {
	return fmt.Sprintf("console --eval did not settle within %s", e.Timeout)
}

// IsConsoleCaptureFailure reports whether err is a capture that ran but must
// not read as clean: a failed navigation, a thrown eval, a load timeout, or an
// eval that never settled. RemoteConsoleCapture returns the entries it
// collected with these errors, so a caller should show both and fail.
func IsConsoleCaptureFailure(err error) bool {
	var evalErr *ConsoleEvalError
	var navErr *ConsoleNavigateError
	var loadErr *ConsoleLoadTimeoutError
	var evalTimeoutErr *ConsoleEvalTimeoutError
	return errors.As(err, &evalErr) || errors.As(err, &navErr) || errors.As(err, &loadErr) || errors.As(err, &evalTimeoutErr)
}

// ConsoleEvalState says what became of the Eval expression.
type ConsoleEvalState string

const (
	// ConsoleEvalSettled: the expression returned, or its promise resolved.
	ConsoleEvalSettled ConsoleEvalState = "settled"
	// ConsoleEvalNavigated: the page left before the eval settled, usually
	// through a navigation the eval started (a sign-in redirect, a form
	// submit). Code after the eval's pending await did not run. This is not a
	// throw: the capture goes on with the new document.
	ConsoleEvalNavigated ConsoleEvalState = "navigated"
)

// ConsoleCapture is one capture: the entries, and what became of the Eval.
type ConsoleCapture struct {
	Entries []ConsoleEntry
	// Eval is empty when no Eval was given or when it did not finish; the
	// returned error then says why.
	Eval ConsoleEvalState
	// NavigatedTo is the top frame's URL after the last cross-document
	// navigation that started once the eval was sent. Empty if the page
	// stayed on its document.
	NavigatedTo string
}

// ConsoleCapturer is a Driver that reports the eval outcome with the entries.
type ConsoleCapturer interface {
	ConsoleCapture(ctx context.Context, session string, opts ConsoleCaptureOptions) (ConsoleCapture, error)
}

// CaptureConsole runs one capture through d. A driver without
// ConsoleCapturer gives only the entries (the Eval state stays empty).
func CaptureConsole(ctx context.Context, d Driver, session string, opts ConsoleCaptureOptions) (ConsoleCapture, error) {
	if c, ok := d.(ConsoleCapturer); ok {
		return c.ConsoleCapture(ctx, session, opts)
	}
	entries, err := d.Console(ctx, session, opts)
	return ConsoleCapture{Entries: entries}, err
}

// consoleLoadTimeout bounds the wait for the load event before a queued Eval,
// and consoleEvalTimeout bounds the wait for the Eval to settle. Tests
// shorten them.
var (
	consoleLoadTimeout = 30 * time.Second
	consoleEvalTimeout = 30 * time.Second
)

// keepKind reports whether an entry of kind k passes the Kind filter. "js"
// keeps console + exception (drops Log-domain network/resource/CSP noise — the
// clean "did the page's own JS crash" gate); "log" keeps only Log-domain
// entries; anything else keeps all.
func keepKind(filter, k string) bool {
	switch filter {
	case "js":
		return k == "console" || k == "exception"
	case "log":
		return k == "log"
	case "csp":
		return k == "csp"
	default:
		return true
	}
}

// consoleLevelRank orders levels so a minimum-level filter is a simple compare.
func consoleLevelRank(level string) int {
	switch level {
	case "error":
		return 4
	case "warning", "warn":
		return 3
	case "info":
		return 2
	case "log", "debug", "verbose":
		return 1
	default:
		return 0
	}
}

// normalizeConsoleType maps a Runtime.consoleAPICalled `type` to our level set.
func normalizeConsoleType(t string) string {
	switch t {
	case "error", "assert":
		return "error"
	case "warning":
		return "warning"
	case "info":
		return "info"
	case "debug":
		return "debug"
	default:
		return "log"
	}
}

// RemoteConsoleCapture is RemoteConsoleCaptureResult without the Eval state.
func RemoteConsoleCapture(ctx context.Context, s *Session, opts ConsoleCaptureOptions) ([]ConsoleEntry, error) {
	res, err := RemoteConsoleCaptureResult(ctx, s, opts)
	return res.Entries, err
}

// RemoteConsoleCaptureResult enables the Runtime + Log domains on the
// session's page, optionally fires a trigger, then collects console /
// exception / log events for opts.Window. With an Eval the window starts when
// the eval settles; the eval may await for up to consoleEvalTimeout. Entries
// below opts.Level are dropped. Entries are in arrival order with exact
// (kind,level,text) duplicates removed.
func RemoteConsoleCaptureResult(ctx context.Context, s *Session, opts ConsoleCaptureOptions) (ConsoleCapture, error) {
	var result ConsoleCapture
	if err := opts.Validate(); err != nil {
		return result, err
	}
	window := opts.Window
	if window <= 0 {
		window = 1500 * time.Millisecond
	}
	minRank := 0
	if r := consoleLevelRank(opts.Level); r > 0 {
		minRank = r
	}
	// loadThenEval: load the page, wait for its load event, then evaluate.
	loadThenEval := opts.Eval != "" && (opts.Navigate != "" || opts.Reload)
	loadBudget := time.Duration(0)
	if loadThenEval {
		loadBudget = consoleLoadTimeout
	}
	evalBudget := time.Duration(0)
	if opts.Eval != "" {
		evalBudget = consoleEvalTimeout
	}

	// Operation budget: the dial + enable + trigger writes, the load wait for
	// a queued eval, the eval's own wait, and the listen window, with headroom
	// so a slow navigation doesn't eat the window.
	opCtx, cancel := context.WithTimeout(ctx, window+loadBudget+evalBudget+30*time.Second)
	defer cancel()

	conn, err := dialFirstPage(opCtx, s)
	if err != nil {
		return result, err
	}
	defer conn.Close()

	var id int64
	next := func() int64 { id++; return id }
	write := func(method string, params map[string]any) (int64, error) {
		var pb []byte
		if params != nil {
			pb, _ = json.Marshal(params)
		}
		msgID := next()
		return msgID, conn.Write(opCtx, &cdproto.Message{
			ID:     msgID,
			Method: cdproto.MethodType(method),
			Params: jsontext.Value(pb),
		})
	}

	// Enable the event sources before any trigger so load-time output is caught.
	// Commands are processed FIFO on the connection, so the enables land before
	// the trigger even though we do not wait for their responses here.
	if _, err := write("Runtime.enable", nil); err != nil {
		return result, fmt.Errorf("Runtime.enable: %w", err)
	}
	if _, err := write("Log.enable", nil); err != nil {
		return result, fmt.Errorf("Log.enable: %w", err)
	}
	// Typed CSP issues come from the Audits domain. Enable it only for
	// --kind csp so the default "all" output keeps its existing shape (the
	// same violation also arrives as a free-text Log entry).
	auditsEnabled := opts.Kind == "csp"
	if auditsEnabled {
		if _, err := write("Audits.enable", nil); err != nil {
			return result, fmt.Errorf("Audits.enable: %w", err)
		}
	}
	// With an eval, Page events gate it (loadEventFired after a navigate or
	// reload) and name where it took the page (frameNavigated).
	if opts.Eval != "" {
		if _, err := write("Page.enable", nil); err != nil {
			return result, fmt.Errorf("Page.enable: %w", err)
		}
	}

	// Bound the capture by closing the connection when a timer elapses.
	// chromedp's Conn.Read ignores its context argument and blocks on the
	// socket, so a read deadline cannot end it — closing the conn makes the
	// blocked Read return an error, which ends the read loop. The load timer
	// bounds the wait for a queued eval to start, the eval timer bounds its
	// wait to settle, and the listen window starts only after it settles.
	var windowTimer, loadTimer, evalTimer *time.Timer
	var loadTimedOut, evalTimedOut atomic.Bool
	armWindow := func() { windowTimer = time.AfterFunc(window, func() { _ = conn.Close() }) }
	defer func() {
		for _, t := range []*time.Timer{windowTimer, loadTimer, evalTimer} {
			if t != nil {
				t.Stop()
			}
		}
	}()

	var navigateID, evalID int64
	sendEval := func() error {
		msgID, err := write("Runtime.evaluate", map[string]any{
			"expression":   opts.Eval,
			"awaitPromise": true,
			"userGesture":  true,
		})
		if err != nil {
			return fmt.Errorf("Runtime.evaluate: %w", err)
		}
		evalID = msgID
		evalTimer = time.AfterFunc(evalBudget, func() {
			evalTimedOut.Store(true)
			_ = conn.Close()
		})
		return nil
	}
	switch {
	case opts.Navigate != "":
		msgID, err := write("Page.navigate", map[string]any{"url": opts.Navigate})
		if err != nil {
			return result, fmt.Errorf("Page.navigate: %w", err)
		}
		navigateID = msgID
	case opts.Reload:
		if _, err := write("Page.reload", map[string]any{"ignoreCache": false}); err != nil {
			return result, fmt.Errorf("Page.reload: %w", err)
		}
	}
	switch {
	case loadThenEval:
		loadTimer = time.AfterFunc(loadBudget, func() {
			loadTimedOut.Store(true)
			_ = conn.Close()
		})
	case opts.Eval != "":
		if err := sendEval(); err != nil {
			return result, err
		}
	default:
		armWindow()
	}
	// evalAfterLoad fires the queued eval once.
	evalAfterLoad := func() error {
		if !loadThenEval || evalID != 0 {
			return nil
		}
		loadTimer.Stop()
		return sendEval()
	}
	var captureErr error
	evalSettled := false
	committed := false // the new document replaced the old one (contexts cleared)

	var entries []ConsoleEntry
	seen := map[string]struct{}{}
	add := func(e ConsoleEntry) {
		if e.Level == "" {
			e.Level = "log"
		}
		if consoleLevelRank(e.Level) < minRank {
			return
		}
		if !keepKind(opts.Kind, e.Kind) {
			return
		}
		key := e.Kind + "\x00" + e.Level + "\x00" + e.Text
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		entries = append(entries, e)
	}

read:
	for {
		in := new(cdproto.Message)
		if err := conn.Read(opCtx, in); err != nil {
			// The window timer closing the conn ends capture cleanly; any other
			// read error is also non-fatal — return what we collected rather than
			// discarding a real capture.
			break
		}
		if in.ID != 0 {
			switch in.ID {
			case navigateID:
				if navErr := navigateFailure(opts.Navigate, in); navErr != nil {
					// Chrome is on its own error page; nothing after this is
					// evidence about the target.
					captureErr = navErr
					break read
				}
				if loadThenEval && !navigateCrossesDocument(in) {
					// A same-document navigation (fragment change) fires no load
					// event; act at once.
					if err := evalAfterLoad(); err != nil {
						captureErr = err
						break read
					}
				}
			case evalID:
				evalTimer.Stop()
				evalSettled = true
				desc, threw := evalException(in)
				switch {
				case evalNavigatedAway(in):
					result.Eval = ConsoleEvalNavigated
				case threw:
					captureErr = &ConsoleEvalError{Description: desc}
				default:
					result.Eval = ConsoleEvalSettled
				}
				// The listen window counts from the moment the eval settled.
				armWindow()
			}
			continue // other command responses
		}
		if in.Method == "" {
			continue // empty frame
		}
		switch string(in.Method) {
		case "Page.frameNavigated":
			if evalID != 0 {
				if u, ok := topFrameNavigatedURL(in.Params); ok {
					result.NavigatedTo = u
				}
			}
		case "Page.loadEventFired":
			// Only the new document's load counts: an outgoing page that was
			// still loading can fire its own load before the navigation commits.
			if committed {
				if err := evalAfterLoad(); err != nil {
					captureErr = err
					break read
				}
			}
		case "Runtime.executionContextsCleared":
			// Navigation tore down the old page's execution contexts. Runtime.enable
			// replays the *outgoing* page's buffered console before the navigation
			// commits, which would otherwise be reported as the new page's errors.
			// The first clear after our own navigate / reload is that boundary —
			// discard everything collected so far so the capture reflects only the
			// page it loaded. A later clear (the page or the eval navigating on) is
			// part of what this capture observed, so its evidence is kept.
			if (opts.Navigate != "" || opts.Reload) && !committed {
				entries = entries[:0]
				seen = map[string]struct{}{}
			}
			committed = true
		case "Runtime.consoleAPICalled":
			if e, ok := parseConsoleAPICalled(in.Params); ok {
				add(e)
			}
		case "Runtime.exceptionThrown":
			if e, ok := parseExceptionThrown(in.Params); ok {
				add(e)
			}
		case "Log.entryAdded":
			if e, ok := parseLogEntryAdded(in.Params); ok {
				add(e)
			}
		case "Audits.issueAdded":
			if !auditsEnabled {
				continue
			}
			if e, ok := parseCSPIssueAdded(in.Params); ok {
				add(e)
			}
		}
	}
	if captureErr == nil {
		switch {
		case loadThenEval && evalID == 0 && loadTimedOut.Load():
			captureErr = &ConsoleLoadTimeoutError{Timeout: loadBudget}
		case evalID != 0 && !evalSettled && evalTimedOut.Load():
			captureErr = &ConsoleEvalTimeoutError{Timeout: evalBudget}
		case evalID != 0 && !evalSettled:
			// Chrome answers every eval, even one the page navigated away
			// from; a missing answer means the target itself went away.
			captureErr = errors.New("console --eval: the page's CDP connection closed before the eval settled")
		}
	}
	result.Entries = entries
	return result, captureErr
}

// evalNavigatedAway reports a Runtime.evaluate that Chrome abandoned because
// the page left under it, usually through a navigation the eval started.
// Chrome answers such an eval with a protocol error, not exceptionDetails.
func evalNavigatedAway(in *cdproto.Message) bool {
	if in.Error == nil {
		return false
	}
	return strings.Contains(in.Error.Message, "Inspected target navigated or closed") ||
		strings.Contains(in.Error.Message, "Execution context was destroyed")
}

// topFrameNavigatedURL returns the URL of a Page.frameNavigated event for the
// top frame (no parentId). Child frames (ads, embeds) do not move the page.
func topFrameNavigatedURL(params jsontext.Value) (string, bool) {
	var ev struct {
		Frame struct {
			ParentID string `json:"parentId"`
			URL      string `json:"url"`
		} `json:"frame"`
	}
	if json.Unmarshal([]byte(params), &ev) != nil || ev.Frame.ParentID != "" || ev.Frame.URL == "" {
		return "", false
	}
	return ev.Frame.URL, true
}

// navigateFailure returns the typed error for a Page.navigate response that
// failed: a protocol error (an invalid URL) or Chrome's errorText (the target
// could not be reached). Nil means the navigation started.
func navigateFailure(url string, in *cdproto.Message) error {
	if in.Error != nil {
		return &ConsoleNavigateError{URL: url, ErrorText: in.Error.Message}
	}
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if json.Unmarshal([]byte(in.Result), &res) == nil && res.ErrorText != "" {
		return &ConsoleNavigateError{URL: url, ErrorText: res.ErrorText}
	}
	return nil
}

// navigateCrossesDocument reports whether a Page.navigate response started a
// new document. Chrome omits loaderId for a same-document navigation.
func navigateCrossesDocument(in *cdproto.Message) bool {
	var res struct {
		LoaderID string `json:"loaderId"`
	}
	return json.Unmarshal([]byte(in.Result), &res) == nil && res.LoaderID != ""
}

// evalException reports whether a Runtime.evaluate response carries a thrown
// exception or a rejected awaited promise, and describes it in one line.
func evalException(in *cdproto.Message) (string, bool) {
	if in.Error != nil {
		return in.Error.Message, true
	}
	var res struct {
		ExceptionDetails *struct {
			Text      string           `json:"text"`
			Exception *cdpRemoteObject `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal([]byte(in.Result), &res) != nil || res.ExceptionDetails == nil {
		return "", false
	}
	d := res.ExceptionDetails
	text := d.Text
	if d.Exception != nil {
		if desc := renderRemoteObject(*d.Exception); desc != "" {
			text = desc
		}
	}
	text, _, _ = strings.Cut(strings.TrimSpace(text), "\n") // the first line; the stack follows
	if text == "" {
		text = "exception"
	}
	return text, true
}

// cdpRemoteObject is the subset of Runtime.RemoteObject we render.
type cdpRemoteObject struct {
	Type                string          `json:"type"`
	Value               json.RawMessage `json:"value"`
	Description         string          `json:"description"`
	UnserializableValue string          `json:"unserializableValue"`
}

func renderRemoteObject(o cdpRemoteObject) string {
	if len(o.Value) > 0 {
		// Strings come through as JSON strings; unquote for readability.
		var s string
		if json.Unmarshal(o.Value, &s) == nil {
			return s
		}
		return strings.TrimSpace(string(o.Value))
	}
	if o.Description != "" {
		return o.Description
	}
	if o.UnserializableValue != "" {
		return o.UnserializableValue
	}
	if o.Type != "" {
		return o.Type
	}
	return ""
}

func firstStackFrameURLLine(raw json.RawMessage) (string, int64) {
	if len(raw) == 0 {
		return "", 0
	}
	var st struct {
		CallFrames []struct {
			URL        string `json:"url"`
			LineNumber int64  `json:"lineNumber"`
		} `json:"callFrames"`
	}
	if json.Unmarshal(raw, &st) != nil || len(st.CallFrames) == 0 {
		return "", 0
	}
	f := st.CallFrames[0]
	// CDP line numbers are 0-based.
	return f.URL, f.LineNumber + 1
}

func parseConsoleAPICalled(params jsontext.Value) (ConsoleEntry, bool) {
	var p struct {
		Type       string            `json:"type"`
		Args       []cdpRemoteObject `json:"args"`
		Timestamp  float64           `json:"timestamp"`
		StackTrace json.RawMessage   `json:"stackTrace"`
	}
	if json.Unmarshal([]byte(params), &p) != nil {
		return ConsoleEntry{}, false
	}
	parts := make([]string, 0, len(p.Args))
	for _, a := range p.Args {
		parts = append(parts, renderRemoteObject(a))
	}
	url, line := firstStackFrameURLLine(p.StackTrace)
	return ConsoleEntry{
		Kind:  "console",
		Level: normalizeConsoleType(p.Type),
		Text:  strings.TrimSpace(strings.Join(parts, " ")),
		URL:   url,
		Line:  line,
		TS:    p.Timestamp,
	}, true
}

func parseExceptionThrown(params jsontext.Value) (ConsoleEntry, bool) {
	var p struct {
		Timestamp        float64 `json:"timestamp"`
		ExceptionDetails struct {
			Text       string           `json:"text"`
			URL        string           `json:"url"`
			LineNumber int64            `json:"lineNumber"`
			StackTrace json.RawMessage  `json:"stackTrace"`
			Exception  *cdpRemoteObject `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal([]byte(params), &p) != nil {
		return ConsoleEntry{}, false
	}
	d := p.ExceptionDetails
	text := d.Text
	if d.Exception != nil {
		if desc := renderRemoteObject(*d.Exception); desc != "" {
			text = desc
		}
	}
	url := d.URL
	line := d.LineNumber + 1
	if url == "" {
		if u, l := firstStackFrameURLLine(d.StackTrace); u != "" {
			url, line = u, l
		}
	}
	return ConsoleEntry{
		Kind:  "exception",
		Level: "error",
		Text:  strings.TrimSpace(text),
		URL:   url,
		Line:  line,
		TS:    p.Timestamp,
	}, true
}

func parseLogEntryAdded(params jsontext.Value) (ConsoleEntry, bool) {
	var p struct {
		Entry struct {
			Source     string  `json:"source"`
			Level      string  `json:"level"`
			Text       string  `json:"text"`
			Timestamp  float64 `json:"timestamp"`
			URL        string  `json:"url"`
			LineNumber int64   `json:"lineNumber"`
		} `json:"entry"`
	}
	if json.Unmarshal([]byte(params), &p) != nil {
		return ConsoleEntry{}, false
	}
	e := p.Entry
	level := e.Level
	switch level {
	case "verbose":
		level = "debug"
	case "warning":
		level = "warning"
	}
	line := int64(0)
	if e.LineNumber > 0 {
		line = e.LineNumber + 1
	}
	return ConsoleEntry{
		Kind:   "log",
		Level:  level,
		Text:   strings.TrimSpace(e.Text),
		URL:    e.URL,
		Line:   line,
		Source: e.Source,
		TS:     e.Timestamp,
	}, true
}

// cspViolationTypes maps CDP ContentSecurityPolicyViolationType to a short,
// stable vocabulary.
var cspViolationTypes = map[string]string{
	"kInlineViolation":             "inline",
	"kEvalViolation":               "eval",
	"kWasmEvalViolation":           "wasm-eval",
	"kURLViolation":                "url",
	"kSRIViolation":                "sri",
	"kTrustedTypesSinkViolation":   "trusted-types-sink",
	"kTrustedTypesPolicyViolation": "trusted-types-policy",
}

// parseCSPIssueAdded turns an Audits.issueAdded ContentSecurityPolicyIssue into
// a Kind "csp" entry. Other issue codes (mixed content, cookies, ...) return
// ok=false. Every CSP violation is level "error", report-only included: a
// report-only soak exists to find exactly these, and --fail-on-error should
// trip on them; ReportOnly tells the two apart.
func parseCSPIssueAdded(params jsontext.Value) (ConsoleEntry, bool) {
	var p struct {
		Issue struct {
			Code    string `json:"code"`
			Details struct {
				CSP *struct {
					BlockedURL         string `json:"blockedURL"`
					ViolatedDirective  string `json:"violatedDirective"`
					IsReportOnly       bool   `json:"isReportOnly"`
					ViolationType      string `json:"contentSecurityPolicyViolationType"`
					SourceCodeLocation *struct {
						URL          string `json:"url"`
						LineNumber   int64  `json:"lineNumber"`
						ColumnNumber int64  `json:"columnNumber"`
					} `json:"sourceCodeLocation"`
				} `json:"contentSecurityPolicyIssueDetails"`
			} `json:"details"`
		} `json:"issue"`
	}
	if json.Unmarshal([]byte(params), &p) != nil {
		return ConsoleEntry{}, false
	}
	if p.Issue.Code != "ContentSecurityPolicyIssue" || p.Issue.Details.CSP == nil {
		return ConsoleEntry{}, false
	}
	d := p.Issue.Details.CSP
	v := &CSPViolation{
		Directive:     d.ViolatedDirective,
		ViolationType: cspViolationTypes[d.ViolationType],
		BlockedURL:    d.BlockedURL,
		ReportOnly:    d.IsReportOnly,
	}
	if v.ViolationType == "" {
		v.ViolationType = strings.TrimPrefix(d.ViolationType, "k")
	}
	if loc := d.SourceCodeLocation; loc != nil {
		v.SourceURL = loc.URL
		// CDP SourceCodeLocation is 0-based; lineNumber -1 means "no script
		// position" (e.g. a URL violation attributed to the document).
		if loc.LineNumber >= 0 {
			v.Line, v.Column = loc.LineNumber+1, loc.ColumnNumber+1
		}
	}
	what := v.ViolationType
	if v.BlockedURL != "" {
		what = v.BlockedURL
	}
	text := fmt.Sprintf("CSP %s blocked %s", v.Directive, what)
	if v.ReportOnly {
		text += " (report-only)"
	}
	switch {
	case v.SourceURL != "" && v.Line > 0:
		text += fmt.Sprintf(" at %s:%d:%d", v.SourceURL, v.Line, v.Column)
	case v.SourceURL != "":
		text += " on " + v.SourceURL
	case v.Line > 0:
		text += fmt.Sprintf(" at <inline script>:%d:%d", v.Line, v.Column)
	}
	return ConsoleEntry{
		Kind:  "csp",
		Level: "error",
		Text:  text,
		URL:   v.SourceURL,
		Line:  v.Line,
		CSP:   v,
	}, true
}
