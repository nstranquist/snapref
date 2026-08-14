package browser

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsurePageTargetIDCreatesPageWhenSessionHasNoTargets(t *testing.T) {
	var port string
	createCalls := 0
	targets := []cdpTargetListEntry{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/json":
			if err := json.NewEncoder(w).Encode(targets); err != nil {
				t.Errorf("encode target list: %v", err)
			}
		case "/json/new":
			if r.Method != http.MethodPut {
				t.Errorf("/json/new method = %s want PUT", r.Method)
				http.Error(w, "wrong method", http.StatusMethodNotAllowed)
				return
			}
			if r.URL.RawQuery != "about%3Ablank" {
				t.Errorf("/json/new query = %q want about%%3Ablank", r.URL.RawQuery)
				http.Error(w, "wrong query", http.StatusBadRequest)
				return
			}
			createCalls++
			target := cdpTargetListEntry{
				ID:                   "target-recovered",
				Type:                 "page",
				URL:                  "about:blank",
				Title:                "about:blank",
				DevToolsFrontendURL:  "/devtools/inspector.html?ws=127.0.0.1:" + port + "/devtools/page/target-recovered",
				WebSocketDebuggerURL: "ws://127.0.0.1:" + port + "/devtools/page/target-recovered",
			}
			targets = append(targets, target)
			if err := json.NewEncoder(w).Encode(target); err != nil {
				t.Errorf("encode created target: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host, parsedPort, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("parse fake CDP address %q: %v", srv.URL, err)
	}
	if host == "" {
		t.Fatalf("parse fake CDP address %q: empty host", srv.URL)
	}
	port = parsedPort
	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse fake CDP port %q: %v", port, err)
	}

	id, ok := ensurePageTargetID(context.Background(), portNum)
	if !ok {
		t.Fatal("ensurePageTargetID returned ok=false")
	}
	if string(id) != "target-recovered" {
		t.Fatalf("target id = %q want target-recovered", id)
	}
	if createCalls != 1 {
		t.Fatalf("create calls = %d want 1", createCalls)
	}

	id, ok = ensurePageTargetID(context.Background(), portNum)
	if !ok || string(id) != "target-recovered" {
		t.Fatalf("second ensure target = %q ok=%v", id, ok)
	}
	if createCalls != 1 {
		t.Fatalf("second ensure should reuse existing page target; create calls = %d", createCalls)
	}
}

func TestWaitForPageTargetCreatesPageBeforeOpenReturns(t *testing.T) {
	var port string
	createCalls := 0
	targets := []cdpTargetListEntry{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/json":
			if err := json.NewEncoder(w).Encode(targets); err != nil {
				t.Errorf("encode target list: %v", err)
			}
		case "/json/new":
			createCalls++
			target := cdpTargetListEntry{
				ID:                   "target-open-ready",
				Type:                 "page",
				URL:                  "about:blank",
				Title:                "about:blank",
				WebSocketDebuggerURL: "ws://127.0.0.1:" + port + "/devtools/page/target-open-ready",
			}
			targets = append(targets, target)
			if err := json.NewEncoder(w).Encode(target); err != nil {
				t.Errorf("encode created target: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_, parsedPort, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("parse fake CDP address %q: %v", srv.URL, err)
	}
	port = parsedPort
	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse fake CDP port %q: %v", port, err)
	}

	if err := waitForPageTarget(portNum, 0, time.Second); err != nil {
		t.Fatalf("waitForPageTarget: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("create calls = %d want 1", createCalls)
	}
}

func TestWaitForPageWebSocketURLHandlesPublicationDelayAndRejectsRemoteURL(t *testing.T) {
	var port string
	publishAt := time.Now().Add(125 * time.Millisecond)
	var remote atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if time.Now().Before(publishAt) {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		host := "127.0.0.1"
		if remote.Load() {
			host = "example.com"
		}
		_ = json.NewEncoder(w).Encode([]cdpTargetListEntry{{
			ID: "delayed", Type: "page", WebSocketDebuggerURL: "ws://" + host + ":" + port + "/devtools/page/delayed",
		}})
	}))
	defer server.Close()
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	portNumber, _ := strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wsURL, ok := waitForPageWebSocketURL(ctx, portNumber, time.Second)
	if !ok || !strings.Contains(wsURL, "/devtools/page/delayed") {
		t.Fatalf("delayed page websocket = %q ok=%v", wsURL, ok)
	}
	remote.Store(true)
	if _, ok := firstPageWebSocketURL(portNumber); ok {
		t.Fatal("remote page websocket URL accepted")
	}
}
