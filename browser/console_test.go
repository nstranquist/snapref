package browser

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// fakeCDPConsoleServer serves the /json target list plus a websocket that
// answers Runtime.enable / Log.enable / Page.navigate and then emits the given
// raw CDP event frames. It is a hermetic stand-in for a Chrome page target, so
// RemoteConsoleCapture's read loop is exercised end-to-end without launching a
// real browser.
func fakeCDPConsoleServer(t *testing.T, events []string) (*httptest.Server, int) {
	t.Helper()
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":                   "target-1",
				"type":                 "page",
				"url":                  "http://example.test/app",
				"title":                "Example App",
				"webSocketDebuggerUrl": "ws://127.0.0.1:" + port + "/devtools/page/target-1",
			}})
		case "/devtools/page/target-1":
			conn, _, _, err := ws.UpgradeHTTP(r, w)
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sawNavigate := false
				for {
					data, _, err := wsutil.ReadClientData(conn)
					if err != nil {
						return
					}
					var msg struct {
						ID     int64  `json:"id"`
						Method string `json:"method"`
					}
					_ = json.Unmarshal(data, &msg)
					// Answer every command so the client's writer never blocks.
					if msg.ID != 0 {
						_ = wsutil.WriteServerText(conn, []byte(`{"id":`+strconv.FormatInt(msg.ID, 10)+`,"result":{}}`))
					}
					// Emit the event stream once navigation is triggered (mirrors
					// real Chrome producing console output during a page load).
					if msg.Method == "Page.navigate" && !sawNavigate {
						sawNavigate = true
						for _, ev := range events {
							if err := wsutil.WriteServerText(conn, []byte(ev)); err != nil {
								return
							}
						}
					}
				}
			}()
		default:
			http.NotFound(w, r)
		}
	}))
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse fake CDP port %q: %v", port, err)
	}
	return srv, portNum
}

func consoleEventFixtures() []string {
	return []string{
		`{"method":"Runtime.consoleAPICalled","params":{"type":"error","timestamp":1,"args":[{"type":"string","value":"table-core crashed"}],"stackTrace":{"callFrames":[{"functionName":"render","url":"http://example.test/app.js","lineNumber":41,"columnNumber":2}]}}}`,
		`{"method":"Runtime.consoleAPICalled","params":{"type":"log","timestamp":2,"args":[{"type":"string","value":"hydrated"}]}}`,
		`{"method":"Runtime.exceptionThrown","params":{"timestamp":3,"exceptionDetails":{"text":"Uncaught","lineNumber":10,"columnNumber":5,"url":"http://example.test/app.js","exception":{"type":"object","subtype":"error","className":"TypeError","description":"TypeError: Cannot read properties of undefined (reading 'rows')\n    at render (app.js:11:6)"}}}}`,
		// Duplicate exception — must be deduped away.
		`{"method":"Runtime.exceptionThrown","params":{"timestamp":3,"exceptionDetails":{"text":"Uncaught","lineNumber":10,"columnNumber":5,"url":"http://example.test/app.js","exception":{"type":"object","subtype":"error","className":"TypeError","description":"TypeError: Cannot read properties of undefined (reading 'rows')\n    at render (app.js:11:6)"}}}}`,
		`{"method":"Log.entryAdded","params":{"entry":{"source":"network","level":"error","text":"Failed to load resource: 404","timestamp":4,"url":"http://example.test/missing.json"}}}`,
		`{"method":"Log.entryAdded","params":{"entry":{"source":"deprecation","level":"warning","text":"Deprecation: foo is deprecated","timestamp":5,"url":"http://example.test/app.js","lineNumber":7}}}`,
		// Audits issues (shapes captured from Chrome 152, 11-04). Only --kind csp
		// enables the Audits domain, so the other kinds must ignore these.
		`{"method":"Audits.issueAdded","params":{"issue":{"code":"ContentSecurityPolicyIssue","details":{"contentSecurityPolicyIssueDetails":{"violatedDirective":"style-src-elem","isReportOnly":false,"contentSecurityPolicyViolationType":"kInlineViolation","sourceCodeLocation":{"scriptId":"8","url":"http://example.test/app.js","lineNumber":0,"columnNumber":81},"violatingNodeId":436}}}}}`,
		`{"method":"Audits.issueAdded","params":{"issue":{"code":"ContentSecurityPolicyIssue","details":{"contentSecurityPolicyIssueDetails":{"blockedURL":"https://evil.example/x.png","violatedDirective":"img-src","isReportOnly":true,"contentSecurityPolicyViolationType":"kURLViolation","sourceCodeLocation":{"url":"http://example.test/app","lineNumber":-1,"columnNumber":0}}}}}}`,
		`{"method":"Audits.issueAdded","params":{"issue":{"code":"MixedContentIssue","details":{"mixedContentIssueDetails":{"insecureURL":"http://x.test/a.js"}}}}}`,
	}
}

func captureWith(t *testing.T, opts ConsoleCaptureOptions) []ConsoleEntry {
	t.Helper()
	srv, port := fakeCDPConsoleServer(t, consoleEventFixtures())
	defer srv.Close()
	s := &Session{Name: "test", Port: port}
	if opts.Window == 0 {
		opts.Window = 600 * time.Millisecond
	}
	if opts.Navigate == "" && !opts.Reload && opts.Eval == "" {
		opts.Navigate = "http://example.test/app"
	}
	entries, err := RemoteConsoleCapture(context.Background(), s, opts)
	if err != nil {
		t.Fatalf("RemoteConsoleCapture: %v", err)
	}
	return entries
}

func countLevels(entries []ConsoleEntry) map[string]int {
	m := map[string]int{}
	for _, e := range entries {
		m[e.Level]++
	}
	return m
}

func TestRemoteConsoleCaptureNormalizesAndDedupes(t *testing.T) {
	entries := captureWith(t, ConsoleCaptureOptions{Level: "all"})

	// 6 events in, but the duplicate exception is deduped → 5 entries.
	if len(entries) != 5 {
		t.Fatalf("want 5 entries, got %d: %+v", len(entries), entries)
	}
	levels := countLevels(entries)
	if levels["error"] != 3 { // console error + exception + network log error
		t.Fatalf("want 3 error entries, got %d: %+v", levels["error"], entries)
	}
	if levels["warning"] != 1 { // deprecation log
		t.Fatalf("want 1 warning entry, got %d: %+v", levels["warning"], entries)
	}
	if levels["log"] != 1 { // hydrated
		t.Fatalf("want 1 log entry, got %d: %+v", levels["log"], entries)
	}

	var consoleErr, exc, netLog *ConsoleEntry
	for i := range entries {
		e := &entries[i]
		switch {
		case e.Kind == "console" && e.Level == "error":
			consoleErr = e
		case e.Kind == "exception":
			exc = e
		case e.Kind == "log" && e.Source == "network":
			netLog = e
		}
	}
	if consoleErr == nil || consoleErr.Text != "table-core crashed" {
		t.Fatalf("console error not normalized: %+v", consoleErr)
	}
	// CDP line numbers are 0-based; we report 1-based (41 → 42).
	if consoleErr.Line != 42 || consoleErr.URL != "http://example.test/app.js" {
		t.Fatalf("console error stack frame wrong: %+v", consoleErr)
	}
	if exc == nil || !strings.Contains(exc.Text, "Cannot read properties of undefined") {
		t.Fatalf("exception not normalized: %+v", exc)
	}
	if exc.Line != 11 { // exceptionDetails.lineNumber 10 → 11
		t.Fatalf("exception line = %d want 11: %+v", exc.Line, exc)
	}
	if netLog == nil || netLog.Source != "network" || netLog.Level != "error" {
		t.Fatalf("network log entry not normalized: %+v", netLog)
	}
}

func TestRemoteConsoleCaptureLevelFilterError(t *testing.T) {
	entries := captureWith(t, ConsoleCaptureOptions{Level: "error"})
	if len(entries) != 3 {
		t.Fatalf("want 3 error-level entries, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Level != "error" {
			t.Fatalf("level filter leaked non-error entry: %+v", e)
		}
	}
}

func TestRemoteConsoleCaptureLevelFilterWarning(t *testing.T) {
	entries := captureWith(t, ConsoleCaptureOptions{Level: "warning"})
	// 3 errors + 1 warning, no log/info.
	if len(entries) != 4 {
		t.Fatalf("want 4 entries at warning+, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if consoleLevelRank(e.Level) < consoleLevelRank("warning") {
			t.Fatalf("warning filter leaked entry below threshold: %+v", e)
		}
	}
}

func TestRemoteConsoleCaptureKindJSDropsLogDomain(t *testing.T) {
	entries := captureWith(t, ConsoleCaptureOptions{Level: "all", Kind: "js"})
	// console error + console log + exception (deduped). Network + deprecation
	// Log-domain entries are dropped.
	if len(entries) != 3 {
		t.Fatalf("want 3 js entries, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Kind == "log" {
			t.Fatalf("--kind js leaked a Log-domain entry: %+v", e)
		}
	}
}

func TestRemoteConsoleCaptureKindLogKeepsOnlyLogDomain(t *testing.T) {
	entries := captureWith(t, ConsoleCaptureOptions{Level: "all", Kind: "log"})
	// network error + deprecation warning.
	if len(entries) != 2 {
		t.Fatalf("want 2 log entries, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Kind != "log" {
			t.Fatalf("--kind log leaked a non-log entry: %+v", e)
		}
	}
}

func TestRemoteConsoleCaptureDefaultsWindowAndLevel(t *testing.T) {
	// Empty options must default to a 1.5s window and "all" level, and still
	// dial + navigate via the fixture default.
	entries := captureWith(t, ConsoleCaptureOptions{})
	if len(entries) != 5 {
		t.Fatalf("want 5 entries with default options, got %d", len(entries))
	}
}

func TestRemoteConsoleCaptureResetsOnExecutionContextsCleared(t *testing.T) {
	// Mirrors real Chrome: Runtime.enable replays the OUTGOING page's console
	// before navigation commits, then executionContextsCleared fires, then the
	// NEW page produces its output. Only the new page's entry must survive.
	events := []string{
		// Replayed history from the page we are navigating away from.
		`{"method":"Runtime.consoleAPICalled","params":{"type":"error","timestamp":1,"args":[{"type":"string","value":"OLD PAGE stale error"}]}}`,
		`{"method":"Log.entryAdded","params":{"entry":{"source":"network","level":"error","text":"OLD PAGE 404","timestamp":1}}}`,
		// Navigation boundary.
		`{"method":"Runtime.executionContextsCleared","params":{}}`,
		// New page output.
		`{"method":"Runtime.consoleAPICalled","params":{"type":"error","timestamp":2,"args":[{"type":"string","value":"NEW PAGE error"}]}}`,
	}
	srv, port := fakeCDPConsoleServer(t, events)
	defer srv.Close()
	s := &Session{Name: "test", Port: port}
	entries, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{
		Navigate: "http://example.test/new",
		Window:   600 * time.Millisecond,
		Level:    "all",
	})
	if err != nil {
		t.Fatalf("RemoteConsoleCapture: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want only the post-navigation entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].Text != "NEW PAGE error" {
		t.Fatalf("stale pre-navigation entry survived: %+v", entries)
	}
}

func TestRemoteConsoleCaptureNoPageTargetIsError(t *testing.T) {
	// A session whose port serves no page target must surface a clear error,
	// not panic or hang.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	portNum, _ := strconv.Atoi(port)
	s := &Session{Name: "test", Port: portNum}
	_, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{Window: 200 * time.Millisecond})
	if err == nil {
		t.Fatalf("want error for missing page target, got nil")
	}
}

func TestConsoleLevelRankOrdering(t *testing.T) {
	if !(consoleLevelRank("error") > consoleLevelRank("warning") &&
		consoleLevelRank("warning") > consoleLevelRank("info") &&
		consoleLevelRank("info") > consoleLevelRank("log")) {
		t.Fatalf("level rank ordering is wrong")
	}
	if consoleLevelRank("nonsense") != 0 {
		t.Fatalf("unknown level should rank 0")
	}
}

func TestRenderRemoteObjectPrefersValueThenDescription(t *testing.T) {
	if got := renderRemoteObject(cdpRemoteObject{Type: "string", Value: json.RawMessage(`"hi"`)}); got != "hi" {
		t.Fatalf("string value render = %q want hi", got)
	}
	if got := renderRemoteObject(cdpRemoteObject{Type: "object", Description: "Object{a:1}"}); got != "Object{a:1}" {
		t.Fatalf("object description render = %q", got)
	}
	if got := renderRemoteObject(cdpRemoteObject{Type: "number", Value: json.RawMessage(`42`)}); got != "42" {
		t.Fatalf("number value render = %q want 42", got)
	}
}

func TestRemoteConsoleCaptureKindCSPReturnsTypedIssues(t *testing.T) {
	entries := captureWith(t, ConsoleCaptureOptions{Kind: "csp"})
	if len(entries) != 2 {
		t.Fatalf("want 2 csp entries (mixed-content ignored, console/log dropped), got %d: %+v", len(entries), entries)
	}
	inline, url := entries[0], entries[1]
	for _, e := range entries {
		if e.Kind != "csp" || e.Level != "error" || e.CSP == nil {
			t.Fatalf("csp entry not typed as error: %+v", e)
		}
	}
	if got := *inline.CSP; got.Directive != "style-src-elem" || got.ViolationType != "inline" || got.ReportOnly ||
		got.SourceURL != "http://example.test/app.js" || got.Line != 1 || got.Column != 82 {
		t.Fatalf("inline violation = %+v", got)
	}
	if inline.Text != "CSP style-src-elem blocked inline at http://example.test/app.js:1:82" {
		t.Fatalf("inline text = %q", inline.Text)
	}
	if got := *url.CSP; got.Directive != "img-src" || got.ViolationType != "url" || !got.ReportOnly ||
		got.BlockedURL != "https://evil.example/x.png" || got.Line != 0 || got.Column != 0 {
		t.Fatalf("url violation (lineNumber -1 = no position) = %+v", got)
	}
	if url.Text != "CSP img-src blocked https://evil.example/x.png (report-only) on http://example.test/app" {
		t.Fatalf("url text = %q", url.Text)
	}
}

func TestRemoteConsoleCaptureOtherKindsIgnoreAuditsIssues(t *testing.T) {
	for _, kind := range []string{"", "all", "js", "log"} {
		for _, e := range captureWith(t, ConsoleCaptureOptions{Kind: kind}) {
			if e.Kind == "csp" || e.CSP != nil {
				t.Fatalf("--kind %q surfaced an Audits issue without enabling Audits: %+v", kind, e)
			}
		}
	}
}

func TestValidConsoleKind(t *testing.T) {
	for _, k := range []string{"", "all", "js", "log", "csp"} {
		if !ValidConsoleKind(k) {
			t.Fatalf("ValidConsoleKind(%q) = false", k)
		}
	}
	for _, k := range []string{"CSP", "security", "errors"} {
		if ValidConsoleKind(k) {
			t.Fatalf("ValidConsoleKind(%q) = true", k)
		}
	}
}
