package browser

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReadCDPVersionRequiresOwnedLoopbackBrowserEndpoint(t *testing.T) {
	var response cdpVersion
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Browser":"` + response.Browser + `","webSocketDebuggerUrl":"` + response.WebSocketDebuggerURL + `"}`))
	}))
	defer server.Close()
	_, portRaw, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatal(err)
	}

	valid := "ws://127.0.0.1:" + portRaw + "/devtools/browser/runtime-test"
	for name, candidate := range map[string]cdpVersion{
		"missing browser identity": {WebSocketDebuggerURL: valid},
		"missing websocket":        {Browser: "Chrome/151.0.0.0"},
		"remote host":              {Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: "ws://example.com:" + portRaw + "/devtools/browser/runtime-test"},
		"hostname not literal":     {Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: "ws://localhost:" + portRaw + "/devtools/browser/runtime-test"},
		"wrong port":               {Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: "ws://127.0.0.1:1/devtools/browser/runtime-test"},
		"wrong path":               {Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: "ws://127.0.0.1:" + portRaw + "/devtools/page/runtime-test"},
		"query injection":          {Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: valid + "?target=other"},
		"empty query injection":    {Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: valid + "?"},
		"path traversal":           {Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: valid + "/../other"},
		"encoded path injection":   {Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: valid + "%2Fother"},
	} {
		t.Run(name, func(t *testing.T) {
			response = candidate
			if _, err := readCDPVersion(port, time.Second); err == nil {
				t.Fatalf("unsafe CDP response accepted: %+v", candidate)
			}
		})
	}

	response = cdpVersion{Browser: "Chrome/151.0.0.0", WebSocketDebuggerURL: valid}
	got, err := readCDPVersion(port, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got.WebSocketDebuggerURL != valid {
		t.Fatalf("websocket URL = %q, want %q", got.WebSocketDebuggerURL, valid)
	}
}

func TestReadCDPVersionRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/devtools", http.StatusFound)
	}))
	defer server.Close()
	_, portRaw, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	port, _ := strconv.Atoi(portRaw)
	if _, err := readCDPVersion(port, time.Second); err == nil || !strings.Contains(err.Error(), "must not redirect") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestWaitForCDPRejectsExitedProcess(t *testing.T) {
	if _, err := waitForCDP(1, 999999, time.Second); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("exited-process readiness error = %v", err)
	}
}

func TestRequireExtensionReadinessFailsClosed(t *testing.T) {
	const expected = "abcdefghijklmnop"
	for name, discovered := range map[string]struct {
		id  string
		err error
	}{
		"discovery error": {err: errors.New("deadline")},
		"empty id":        {},
		"wrong id":        {id: "ponmlkjihgfedcba"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := requireExtensionReadiness(expected, discovered.id, discovered.err); err == nil {
				t.Fatal("extension readiness unexpectedly passed")
			}
		})
	}
	if id, err := requireExtensionReadiness(expected, expected, nil); err != nil || id != expected {
		t.Fatalf("valid extension readiness: id=%q err=%v", id, err)
	}
}

func TestDiscoverExtensionByIDRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()
	_, portRaw, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	port, _ := strconv.Atoi(portRaw)
	_, err := discoverExtensionByID(port, "abcdefghijklmnop", 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "must not redirect") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestReadExtensionTargetsBoundsTargetListResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"type":"service_worker","url":"chrome-extension://abcdefghijklmnop/worker.js","padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 1<<20)))
		_, _ = w.Write([]byte(`"}]`))
	}))
	defer server.Close()
	_, portRaw, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	port, _ := strconv.Atoi(portRaw)
	client := &http.Client{Timeout: 5 * time.Second}
	_, err := readExtensionTargets(client, port)
	if err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("oversized response error = %v", err)
	}
}
