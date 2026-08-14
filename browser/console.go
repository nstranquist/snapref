package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
// and capture the console output it produces).
//
// One subtlety: Runtime.enable replays the *currently loaded* page's buffered
// console, and we enable before the navigation commits — so a --navigate /
// --reload would otherwise report the outgoing page's history as the new page's
// errors. Runtime.executionContextsCleared fires when navigation tears down the
// old contexts; we reset the collected set on it, so a per-load capture reflects
// only the page it loaded. In the passive (no-trigger) and --eval modes no
// navigation occurs, so the replay surfaces the page's current console state
// (plus the Log domain's buffered network / CSP / deprecation entries).

// ConsoleEntry is one normalized console / exception / log record.
type ConsoleEntry struct {
	Kind   string  `json:"kind"`             // "console" | "exception" | "log"
	Level  string  `json:"level"`            // "log" | "debug" | "info" | "warning" | "error"
	Text   string  `json:"text"`             // joined args / exception description / log text
	URL    string  `json:"url,omitempty"`    // source URL when known
	Line   int64   `json:"line,omitempty"`   // 1-based line in source when known
	Source string  `json:"source,omitempty"` // Log domain source (network/javascript/…); empty for console/exception
	TS     float64 `json:"ts,omitempty"`     // CDP monotonic timestamp
}

// ConsoleCaptureOptions controls a single capture call.
type ConsoleCaptureOptions struct {
	Navigate string        // if set, Page.navigate here before capturing (captures full load)
	Reload   bool          // if set, Page.reload before capturing (captures load of current URL)
	Eval     string        // if set, Runtime.evaluate this JS before capturing (drives an interaction)
	Window   time.Duration // how long to listen after the trigger (default 1.5s)
	Level    string        // "all" | "warning" | "error" — minimum level to keep (default "all")
	Kind     string        // "all" (default) | "js" (console+exception) | "log" (Log-domain only)
}

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

// RemoteConsoleCapture enables the Runtime + Log domains on the session's page,
// optionally fires a trigger, then collects console / exception / log events for
// opts.Window. Entries below opts.Level are dropped. The returned slice is in
// arrival order with exact (kind,level,text) duplicates removed.
func RemoteConsoleCapture(ctx context.Context, s *Session, opts ConsoleCaptureOptions) ([]ConsoleEntry, error) {
	window := opts.Window
	if window <= 0 {
		window = 1500 * time.Millisecond
	}
	minRank := 0
	if r := consoleLevelRank(opts.Level); r > 0 {
		minRank = r
	}

	// Operation budget: the dial + enable + trigger writes plus the listen
	// window, with headroom so a slow navigation doesn't eat the window.
	opCtx, cancel := context.WithTimeout(ctx, window+30*time.Second)
	defer cancel()

	conn, err := dialFirstPage(opCtx, s)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var id int64
	next := func() int64 { id++; return id }
	write := func(method string, params map[string]any) error {
		var pb []byte
		if params != nil {
			pb, _ = json.Marshal(params)
		}
		return conn.Write(opCtx, &cdproto.Message{
			ID:     next(),
			Method: cdproto.MethodType(method),
			Params: jsontext.Value(pb),
		})
	}

	// Enable the event sources before any trigger so load-time output is caught.
	// Commands are processed FIFO on the connection, so the enables land before
	// the trigger even though we do not wait for their responses here.
	if err := write("Runtime.enable", nil); err != nil {
		return nil, fmt.Errorf("Runtime.enable: %w", err)
	}
	if err := write("Log.enable", nil); err != nil {
		return nil, fmt.Errorf("Log.enable: %w", err)
	}

	switch {
	case opts.Navigate != "":
		if err := write("Page.navigate", map[string]any{"url": opts.Navigate}); err != nil {
			return nil, fmt.Errorf("Page.navigate: %w", err)
		}
	case opts.Reload:
		if err := write("Page.reload", map[string]any{"ignoreCache": false}); err != nil {
			return nil, fmt.Errorf("Page.reload: %w", err)
		}
	case opts.Eval != "":
		if err := write("Runtime.evaluate", map[string]any{
			"expression":   opts.Eval,
			"awaitPromise": true,
			"userGesture":  true,
		}); err != nil {
			return nil, fmt.Errorf("Runtime.evaluate: %w", err)
		}
	}

	// Bound the listen window by closing the connection when it elapses.
	// chromedp's Conn.Read ignores its context argument and blocks on the
	// socket, so a read deadline cannot end the window — closing the conn
	// makes the blocked Read return an error, which we treat as a clean stop.
	windowTimer := time.AfterFunc(window, func() { _ = conn.Close() })
	defer windowTimer.Stop()

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

	for {
		in := new(cdproto.Message)
		if err := conn.Read(opCtx, in); err != nil {
			// The window timer closing the conn ends capture cleanly; any other
			// read error is also non-fatal — return what we collected rather than
			// discarding a real capture.
			break
		}
		if in.ID != 0 || in.Method == "" {
			continue // command response or empty frame
		}
		switch string(in.Method) {
		case "Runtime.executionContextsCleared":
			// Navigation tore down the old page's execution contexts. Runtime.enable
			// replays the *outgoing* page's buffered console before the navigation
			// commits, which would otherwise be reported as the new page's errors.
			// This event is the hard boundary — discard everything collected so far
			// so a --navigate / --reload capture reflects only the page it loaded.
			entries = entries[:0]
			seen = map[string]struct{}{}
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
		}
	}
	return entries, nil
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
