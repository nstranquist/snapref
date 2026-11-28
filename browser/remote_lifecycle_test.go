package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestRunWithTimeoutStopsActionBeforeReturning(t *testing.T) {
	for _, parentCanceled := range []bool{false, true} {
		t.Run(fmt.Sprint(parentCanceled), func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			tab, closeTab := context.WithCancel(context.Background())
			defer closeTab()
			stopped := false
			err := runWithTimeout(parent, tab, 20*time.Millisecond, func(ctx context.Context) error {
				if parentCanceled {
					cancel()
				}
				<-ctx.Done()
				stopped = true
				return ctx.Err()
			})
			want := context.DeadlineExceeded
			if parentCanceled {
				want = context.Canceled
			}
			if !errors.Is(err, want) || !stopped || tab.Err() != nil {
				t.Fatalf("err=%v stopped=%v tab=%v", err, stopped, tab.Err())
			}
		})
	}
}

// The peer models an existing page and counts destructive lifecycle commands.
// Every connection must close, including a canceled caller, without CloseTarget.
func TestRemoteContextReleasesConnectionsAndPreservesPage(t *testing.T) {
	var port string
	var closedTargets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json" {
			_ = json.NewEncoder(w).Encode([]cdpTargetListEntry{{ID: "page", Type: "page"}})
			return
		}
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			data, _, err := wsutil.ReadClientData(conn)
			if err != nil {
				return
			}
			var msg struct {
				ID        int64          `json:"id"`
				Method    string         `json:"method"`
				SessionID string         `json:"sessionId"`
				Params    map[string]any `json:"params"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				return
			}
			result := `{}`
			switch msg.Method {
			case "Target.attachToTarget":
				result = `{"sessionId":"attached"}`
			case "Page.getFrameTree":
				result = `{"frameTree":{"frame":{"id":"frame","url":"about:blank","securityOrigin":"://","mimeType":"text/html"}}}`
			case "DOM.getDocument":
				result = `{"root":{"nodeId":1,"backendNodeId":1,"nodeType":9,"nodeName":"#document","localName":"","nodeValue":""}}`
			case "Runtime.evaluate":
				result = `{"result":{"type":"boolean","value":false}}`
				// Script eval awaits a returned Promise (and returns by value).
				if msg.Params["expression"] == "42" && msg.Params["awaitPromise"] == true && msg.Params["returnByValue"] == true {
					result = `{"result":{"type":"number","value":42}}`
				}
			case "Target.closeTarget", "Browser.close":
				closedTargets.Add(1)
			}
			response := fmt.Sprintf(`{"id":%d,"sessionId":%q,"result":%s}`, msg.ID, msg.SessionID, result)
			if err := wsutil.WriteServerText(conn, []byte(response)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	p, _ := strconv.Atoi(port)
	session := &Session{Port: p, WSURL: "ws://127.0.0.1:" + port + "/devtools/browser/browser"}
	for i := 0; i < 20; i++ {
		parent, cancelParent := context.WithTimeout(context.Background(), 2*time.Second)
		ctx, cancelAlloc, cancelTask, err := NewRemoteContext(parent, session)
		if err != nil {
			cancelParent()
			t.Fatal(err)
		}
		var value int
		if err := EvaluateOn(parent, ctx, "42", &value, time.Second); err != nil || value != 42 {
			t.Fatalf("eval=%d err=%v", value, err)
		}
		connection := chromedp.FromContext(ctx).Browser
		if i%2 == 0 {
			cancelParent()
		} else {
			cancelTask()
			cancelAlloc()
			cancelParent()
		}
		select {
		case <-connection.LostConnection:
		case <-time.After(time.Second):
			t.Fatal("remote connection was not released")
		}
	}
	if closedTargets.Load() != 0 {
		t.Fatalf("persistent page closed %d times", closedTargets.Load())
	}
}

func TestTargetDiscoveryHonorsParentDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	p, _ := strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, ok := firstPageWebSocketURLContext(ctx, p); ok {
		t.Fatal("unexpected target")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("target discovery ignored the operation deadline")
	}
}

func TestTargetDiscoveryHonorsPollingTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	p, _ := strconv.Atoi(port)
	start := time.Now()
	if _, ok := waitForPageWebSocketURL(context.Background(), p, 20*time.Millisecond); ok {
		t.Fatal("unexpected target")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("target discovery ignored polling timeout")
	}
}

func TestRawTargetRejectsForeignWebSocket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]cdpTargetListEntry{{ID: "worker", Type: "service_worker", URL: "chrome-extension://review/worker.js", WebSocketDebuggerURL: "ws://example.test/devtools/page/worker"}})
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	p, _ := strconv.Atoi(port)
	var result any
	err := rawEvalIntoTarget(context.Background(), &Session{Port: p}, "service_worker", "chrome-extension://review/", "42", &result, time.Second)
	if err == nil || !strings.Contains(err.Error(), "invalid target websocket") {
		t.Fatalf("foreign endpoint was not rejected: %v", err)
	}
}

func TestRawDiscoveryPreservesDeadlineError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	p, _ := strconv.Atoi(port)
	s := &Session{Port: p}
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprint(worker), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			var err error
			if worker {
				err = rawEvalIntoTarget(ctx, s, "service_worker", "", "42", nil, time.Second)
			} else {
				_, err = dialFirstPage(ctx, s)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline classification lost: %v", err)
			}
		})
	}
}
