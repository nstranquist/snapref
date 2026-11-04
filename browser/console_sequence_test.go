package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// scriptedCDP is a fake Chrome page target whose replies are scripted per CDP
// command. It records the command order, so tests can prove when the capture
// sends each trigger, not only what it collects.
type scriptedCDP struct {
	mu      sync.Mutex
	methods []string
}

func (f *scriptedCDP) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.methods)
}

// newScriptedCDP serves /json plus one page websocket. reply returns the raw
// frames to write for a command: usually its response, then any events. A
// cdpPause frame delays the frames after it (a promise that awaits), cdpHangUp
// closes the socket, and an empty non-nil slice sends no answer at all.
func newScriptedCDP(t *testing.T, reply func(method string, id int64) []string) (*Session, *scriptedCDP) {
	t.Helper()
	fake := &scriptedCDP{}
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "target-1", "type": "page", "url": "http://example.test/app",
				"webSocketDebuggerUrl": "ws://127.0.0.1:" + port + "/devtools/page/target-1",
			}})
		case "/devtools/page/target-1":
			conn, _, _, err := ws.UpgradeHTTP(r, w)
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
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
					fake.mu.Lock()
					fake.methods = append(fake.methods, msg.Method)
					fake.mu.Unlock()
					frames := reply(msg.Method, msg.ID)
					if frames == nil {
						frames = []string{cdpResult(msg.ID, `{}`)}
					}
					for _, frame := range frames {
						if frame == cdpHangUp {
							return // the target went away
						}
						if d, ok := strings.CutPrefix(frame, cdpPausePrefix); ok {
							pause, _ := time.ParseDuration(d)
							time.Sleep(pause)
							continue
						}
						if err := wsutil.WriteServerText(conn, []byte(frame)); err != nil {
							return
						}
					}
				}
			}()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse fake CDP port %q: %v", port, err)
	}
	return &Session{Name: "test", Port: portNum}, fake
}

func cdpResult(id int64, result string) string {
	return fmt.Sprintf(`{"id":%d,"result":%s}`, id, result)
}

func cdpConsole(text string) string {
	return fmt.Sprintf(`{"method":"Runtime.consoleAPICalled","params":{"type":"log","timestamp":1,"args":[{"type":"string","value":%q}]}}`, text)
}

const (
	cdpPausePrefix     = "pause:"
	cdpHangUp          = "hangup"
	cdpContextsCleared = `{"method":"Runtime.executionContextsCleared","params":{}}`
	cdpLoadFired       = `{"method":"Page.loadEventFired","params":{"timestamp":2}}`
)

func cdpPause(d time.Duration) string { return cdpPausePrefix + d.String() }

func cdpFrameNavigated(parentID, url string) string {
	return fmt.Sprintf(`{"method":"Page.frameNavigated","params":{"frame":{"id":"f","parentId":%q,"url":%q}}}`, parentID, url)
}

func entryTexts(entries []ConsoleEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Text)
	}
	return out
}

func indexOf(methods []string, method string) int {
	return slices.Index(methods, method)
}

// --navigate + --eval: load the page, wait for the NEW document's load event,
// then run the interaction, and capture both.
func TestConsoleNavigateThenEvalRunsAfterLoad(t *testing.T) {
	s, fake := newScriptedCDP(t, func(method string, id int64) []string {
		switch method {
		case "Runtime.enable":
			return []string{cdpResult(id, `{}`), cdpConsole("OLD PAGE replay")}
		case "Page.navigate":
			return []string{cdpResult(id, `{"frameId":"f","loaderId":"L1"}`), cdpContextsCleared, cdpConsole("loaded"), cdpLoadFired}
		case "Runtime.evaluate":
			return []string{cdpConsole("clicked"), cdpResult(id, `{"result":{"type":"undefined"}}`)}
		}
		return nil
	})
	entries, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{
		Navigate: "http://example.test/login", Eval: "document.querySelector('a').click()", Window: 400 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if got, want := entryTexts(entries), []string{"loaded", "clicked"}; !slices.Equal(got, want) {
		t.Fatalf("entries = %q, want %q (replay dropped, load + interaction kept)", got, want)
	}
	methods := fake.seen()
	page, nav, eval := indexOf(methods, "Page.enable"), indexOf(methods, "Page.navigate"), indexOf(methods, "Runtime.evaluate")
	if page < 0 || nav < 0 || eval < 0 || !(page < nav && nav < eval) {
		t.Fatalf("command order = %v, want Page.enable < Page.navigate < Runtime.evaluate", methods)
	}
}

// A load event before the navigation commits belongs to the outgoing page. It
// must not release the eval.
func TestConsoleStaleLoadDoesNotReleaseEval(t *testing.T) {
	restore := consoleLoadTimeout
	consoleLoadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { consoleLoadTimeout = restore })
	s, fake := newScriptedCDP(t, func(method string, id int64) []string {
		if method == "Page.navigate" {
			return []string{cdpResult(id, `{"frameId":"f","loaderId":"L1"}`), cdpLoadFired}
		}
		return nil
	})
	_, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{
		Navigate: "http://example.test/slow", Eval: "1", Window: 200 * time.Millisecond,
	})
	var timeout *ConsoleLoadTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("err = %v, want *ConsoleLoadTimeoutError", err)
	}
	if indexOf(fake.seen(), "Runtime.evaluate") >= 0 {
		t.Fatalf("eval sent on the outgoing page's load: %v", fake.seen())
	}
}

func TestConsoleReloadThenEvalRunsAfterLoad(t *testing.T) {
	s, fake := newScriptedCDP(t, func(method string, id int64) []string {
		switch method {
		case "Page.reload":
			return []string{cdpResult(id, `{}`), cdpContextsCleared, cdpLoadFired}
		case "Runtime.evaluate":
			return []string{cdpConsole("after reload"), cdpResult(id, `{"result":{"type":"number","value":1}}`)}
		}
		return nil
	})
	entries, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{Reload: true, Eval: "1", Window: 300 * time.Millisecond})
	if err != nil || !slices.Equal(entryTexts(entries), []string{"after reload"}) {
		t.Fatalf("entries = %q err = %v", entryTexts(entries), err)
	}
	if methods := fake.seen(); indexOf(methods, "Page.reload") > indexOf(methods, "Runtime.evaluate") {
		t.Fatalf("eval before reload: %v", methods)
	}
}

// Chrome omits loaderId for a same-document (fragment) navigation and fires no
// load event, so the eval must run at once instead of timing out.
func TestConsoleSameDocumentNavigateEvalsAtOnce(t *testing.T) {
	s, fake := newScriptedCDP(t, func(method string, id int64) []string {
		switch method {
		case "Page.navigate":
			return []string{cdpResult(id, `{"frameId":"f"}`)}
		case "Runtime.evaluate":
			return []string{cdpConsole("hash route"), cdpResult(id, `{"result":{"type":"undefined"}}`)}
		}
		return nil
	})
	entries, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{
		Navigate: "http://example.test/app#/settings", Eval: "1", Window: 300 * time.Millisecond,
	})
	if err != nil || !slices.Equal(entryTexts(entries), []string{"hash route"}) {
		t.Fatalf("entries = %q err = %v (methods %v)", entryTexts(entries), err, fake.seen())
	}
}

// An eval that navigates on (a sign-in link) clears contexts again. What the
// first page logged is evidence and must survive.
func TestConsoleEvalNavigationKeepsEarlierEvidence(t *testing.T) {
	s, _ := newScriptedCDP(t, func(method string, id int64) []string {
		switch method {
		case "Page.navigate":
			return []string{cdpResult(id, `{"frameId":"f","loaderId":"L1"}`), cdpContextsCleared, cdpConsole("first page"), cdpLoadFired}
		case "Runtime.evaluate":
			return []string{cdpResult(id, `{"result":{"type":"undefined"}}`), cdpContextsCleared, cdpConsole("second page")}
		}
		return nil
	})
	entries, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{
		Navigate: "http://example.test/login", Eval: "location.href='/next'", Window: 300 * time.Millisecond,
	})
	if err != nil || !slices.Equal(entryTexts(entries), []string{"first page", "second page"}) {
		t.Fatalf("entries = %q err = %v", entryTexts(entries), err)
	}
}

// An eval that throws used to be dropped with its response, so the capture
// reported a clean page for an interaction that never happened.
func TestConsoleEvalExceptionIsTypedError(t *testing.T) {
	threw := `{"result":{"type":"object","subtype":"error","className":"TypeError","description":"TypeError: Cannot read properties of null (reading 'click')\n    at <anonymous>:1:30"},` +
		`"exceptionDetails":{"exceptionId":1,"text":"Uncaught","lineNumber":0,"columnNumber":29,"exception":{"type":"object","subtype":"error","className":"TypeError","description":"TypeError: Cannot read properties of null (reading 'click')\n    at <anonymous>:1:30"}}}`
	rejected := `{"result":{"type":"string","value":"nope"},"exceptionDetails":{"exceptionId":2,"text":"Uncaught (in promise)","exception":{"type":"string","value":"nope"}}}`
	for _, tc := range []struct {
		name, result, want string
	}{
		{"thrown TypeError", threw, "TypeError: Cannot read properties of null (reading 'click')"},
		{"rejected promise", rejected, "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newScriptedCDP(t, func(method string, id int64) []string {
				if method == "Runtime.evaluate" {
					return []string{cdpConsole("before throw"), cdpResult(id, tc.result)}
				}
				return nil
			})
			entries, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{Eval: "x()", Window: 300 * time.Millisecond})
			var evalErr *ConsoleEvalError
			if !errors.As(err, &evalErr) || evalErr.Description != tc.want {
				t.Fatalf("err = %v, want *ConsoleEvalError %q", err, tc.want)
			}
			if !slices.Equal(entryTexts(entries), []string{"before throw"}) {
				t.Fatalf("entries collected before the throw were dropped: %q", entryTexts(entries))
			}
			if ClassifyError(err) != ErrorClassUsage {
				t.Fatalf("class = %q, want usage", ClassifyError(err))
			}
		})
	}
}

// Chrome reports an unreachable target in the navigate response and then shows
// its own error page; capturing that page must not read as clean.
func TestConsoleNavigateFailureIsTypedError(t *testing.T) {
	for _, tc := range []struct {
		name, frame, want string
	}{
		{"errorText", `{"id":%d,"result":{"frameId":"f","loaderId":"L1","errorText":"net::ERR_CONNECTION_REFUSED"}}`, "net::ERR_CONNECTION_REFUSED"},
		{"protocol error", `{"id":%d,"error":{"code":-32000,"message":"Cannot navigate to invalid URL"}}`, "Cannot navigate to invalid URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fake := newScriptedCDP(t, func(method string, id int64) []string {
				if method == "Page.navigate" {
					return []string{fmt.Sprintf(tc.frame, id), cdpContextsCleared, cdpLoadFired}
				}
				return nil
			})
			_, err := RemoteConsoleCapture(context.Background(), s, ConsoleCaptureOptions{
				Navigate: "http://127.0.0.1:1/", Eval: "1", Window: 300 * time.Millisecond,
			})
			var navErr *ConsoleNavigateError
			if !errors.As(err, &navErr) || navErr.ErrorText != tc.want || navErr.URL != "http://127.0.0.1:1/" {
				t.Fatalf("err = %v, want *ConsoleNavigateError %q", err, tc.want)
			}
			if indexOf(fake.seen(), "Runtime.evaluate") >= 0 {
				t.Fatalf("eval ran on Chrome's error page: %v", fake.seen())
			}
			if ClassifyError(err) != ErrorClassProduct {
				t.Fatalf("class = %q, want product", ClassifyError(err))
			}
		})
	}
}

func TestConsoleLoadTimeoutClassifiesAsTimeout(t *testing.T) {
	if got := ClassifyError(&ConsoleLoadTimeoutError{Timeout: time.Second}); got != ErrorClassTimeout {
		t.Fatalf("class = %q, want timeout", got)
	}
}

func TestConsoleCaptureOptionsValidate(t *testing.T) {
	for _, tc := range []struct {
		opts ConsoleCaptureOptions
		want string // "" = valid
	}{
		{ConsoleCaptureOptions{}, ""},
		{ConsoleCaptureOptions{Navigate: "http://x.test/", Eval: "1"}, ""},
		{ConsoleCaptureOptions{Reload: true, Eval: "1"}, ""},
		{ConsoleCaptureOptions{Level: "warn", Kind: "csp"}, ""},
		{ConsoleCaptureOptions{Navigate: "http://x.test/", Reload: true}, "navigate and reload are exclusive"},
		{ConsoleCaptureOptions{Level: "loud"}, `level must be all|warning|error (got "loud")`},
		{ConsoleCaptureOptions{Kind: "css"}, `kind must be all|js|log|csp (got "css")`},
		{ConsoleCaptureOptions{Window: -time.Second}, "duration must not be negative"},
	} {
		err := tc.opts.Validate()
		if tc.want == "" && err != nil {
			t.Errorf("%+v: unexpected %v", tc.opts, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%+v: err = %v, want %q", tc.opts, err, tc.want)
		}
	}
	if _, err := RemoteConsoleCapture(context.Background(), &Session{}, ConsoleCaptureOptions{Navigate: "http://x.test/", Reload: true}); err == nil {
		t.Fatal("capture accepted an invalid request")
	}
}

// Before 11-04 the listen window started when the eval was SENT. An eval that
// awaited longer than --duration lost everything after its await, and a late
// rejection read as clean. The window now counts from the settled eval.
func TestConsoleWindowStartsWhenEvalSettles(t *testing.T) {
	s, _ := newScriptedCDP(t, func(method string, id int64) []string {
		if method == "Runtime.evaluate" {
			return []string{cdpConsole("early"), cdpPause(400 * time.Millisecond), cdpConsole("late"), cdpResult(id, `{"result":{"type":"undefined"}}`), cdpConsole("after settle")}
		}
		return nil
	})
	res, err := RemoteConsoleCaptureResult(context.Background(), s, ConsoleCaptureOptions{Eval: "(async()=>{})()", Window: 150 * time.Millisecond})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if got, want := entryTexts(res.Entries), []string{"early", "late", "after settle"}; !slices.Equal(got, want) {
		t.Fatalf("entries = %q, want %q", got, want)
	}
	if res.Eval != ConsoleEvalSettled || res.NavigatedTo != "" {
		t.Fatalf("eval = %q navigated_to = %q, want settled and no navigation", res.Eval, res.NavigatedTo)
	}
}

func TestConsoleLateRejectionIsNeverClean(t *testing.T) {
	rejected := `{"result":{"type":"string","value":"late nope"},"exceptionDetails":{"exceptionId":3,"text":"Uncaught (in promise)","exception":{"type":"string","value":"late nope"}}}`
	for _, mode := range []ConsoleCaptureOptions{
		{Eval: "x()", Window: 150 * time.Millisecond},
		{Navigate: "http://example.test/app", Eval: "x()", Window: 150 * time.Millisecond},
	} {
		s, _ := newScriptedCDP(t, func(method string, id int64) []string {
			switch method {
			case "Page.navigate":
				return []string{cdpResult(id, `{"frameId":"f","loaderId":"L1"}`), cdpContextsCleared, cdpLoadFired}
			case "Runtime.evaluate":
				return []string{cdpPause(400 * time.Millisecond), cdpResult(id, rejected)}
			}
			return nil
		})
		res, err := RemoteConsoleCaptureResult(context.Background(), s, mode)
		var evalErr *ConsoleEvalError
		if !errors.As(err, &evalErr) || evalErr.Description != "late nope" {
			t.Fatalf("navigate=%q: err = %v, want the late rejection", mode.Navigate, err)
		}
		if res.Eval != "" {
			t.Fatalf("eval state = %q for a rejected eval, want empty", res.Eval)
		}
	}
}

func TestConsoleEvalThatNeverSettlesTimesOut(t *testing.T) {
	restore := consoleEvalTimeout
	consoleEvalTimeout = 300 * time.Millisecond
	t.Cleanup(func() { consoleEvalTimeout = restore })
	s, _ := newScriptedCDP(t, func(method string, id int64) []string {
		if method == "Runtime.evaluate" {
			return []string{cdpConsole("waiting")} // and never an answer
		}
		return nil
	})
	res, err := RemoteConsoleCaptureResult(context.Background(), s, ConsoleCaptureOptions{Eval: "new Promise(()=>{})", Window: 100 * time.Millisecond})
	var timeout *ConsoleEvalTimeoutError
	if !errors.As(err, &timeout) || timeout.Timeout != 300*time.Millisecond || !IsConsoleCaptureFailure(err) {
		t.Fatalf("err = %v, want *ConsoleEvalTimeoutError(300ms) as a capture failure", err)
	}
	if err.Error() != "console --eval did not settle within 300ms" || ClassifyError(err) != ErrorClassTimeout {
		t.Fatalf("message %q class %q", err, ClassifyError(err))
	}
	if !slices.Equal(entryTexts(res.Entries), []string{"waiting"}) {
		t.Fatalf("entries before the timeout were dropped: %q", entryTexts(res.Entries))
	}
}

// A sign-in click that redirects tears the page down under an awaiting eval.
// Chrome answers with a protocol error; before 11-04 that read as "console
// --eval threw", a false failure with the redirect target's evidence lost.
func TestConsoleEvalNavigatedAwayIsNotAThrow(t *testing.T) {
	for _, msg := range []string{"Inspected target navigated or closed", "Execution context was destroyed."} {
		t.Run(msg, func(t *testing.T) {
			s, _ := newScriptedCDP(t, func(method string, id int64) []string {
				switch method {
				case "Page.navigate":
					return []string{cdpResult(id, `{"frameId":"f","loaderId":"L1"}`), cdpFrameNavigated("", "http://example.test/login"), cdpContextsCleared, cdpConsole("login page"), cdpLoadFired}
				case "Runtime.evaluate":
					return []string{
						cdpFrameNavigated("", "http://example.test/desk"),
						cdpContextsCleared,
						fmt.Sprintf(`{"id":%d,"error":{"code":-32000,"message":%q}}`, id, msg),
						cdpFrameNavigated("f", "https://ads.example/frame"), // a child frame does not move the page
						cdpConsole("desk page"),
					}
				}
				return nil
			})
			res, err := RemoteConsoleCaptureResult(context.Background(), s, ConsoleCaptureOptions{
				Navigate: "http://example.test/login", Eval: "signIn()", Window: 200 * time.Millisecond,
			})
			if err != nil {
				t.Fatalf("a navigating eval must not fail the capture: %v", err)
			}
			if res.Eval != ConsoleEvalNavigated || res.NavigatedTo != "http://example.test/desk" {
				t.Fatalf("eval = %q navigated_to = %q, want navigated to /desk", res.Eval, res.NavigatedTo)
			}
			if got, want := entryTexts(res.Entries), []string{"login page", "desk page"}; !slices.Equal(got, want) {
				t.Fatalf("entries = %q, want %q (both documents are evidence)", got, want)
			}
		})
	}
}

// Chrome answers every eval, even one whose page navigated; no answer and a
// closed socket mean the target itself went away. That is a transport fault,
// never a clean capture.
func TestConsoleTargetGoneBeforeEvalSettles(t *testing.T) {
	s, _ := newScriptedCDP(t, func(method string, id int64) []string {
		if method == "Runtime.evaluate" {
			return []string{cdpHangUp}
		}
		return nil
	})
	_, err := RemoteConsoleCaptureResult(context.Background(), s, ConsoleCaptureOptions{Eval: "window.close()", Window: 100 * time.Millisecond})
	if err == nil || IsConsoleCaptureFailure(err) || ClassifyError(err) != ErrorClassTransport {
		t.Fatalf("err = %v (class %q), want a transport error", err, ClassifyError(err))
	}
}

// A driver without ConsoleCapturer still works through CaptureConsole; it
// only cannot report the eval state.
func TestCaptureConsoleFallsBackToDriverConsole(t *testing.T) {
	res, err := CaptureConsole(context.Background(), entriesOnlyDriver{}, "s", ConsoleCaptureOptions{})
	if err != nil || len(res.Entries) != 1 || res.Eval != "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if _, ok := Driver(remoteDriver{}).(ConsoleCapturer); !ok {
		t.Fatal("the remote driver must report the eval state")
	}
}

type entriesOnlyDriver struct{ Driver }

func (entriesOnlyDriver) Console(context.Context, string, ConsoleCaptureOptions) ([]ConsoleEntry, error) {
	return []ConsoleEntry{{Kind: "console", Level: "log", Text: "x"}}, nil
}
