package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// fakeEmulatingChrome models the Chrome behaviour behind the 2026-11-15 bug:
// an Emulation override lives only as long as the DevTools connection that
// set it, while the page keeps its last layout until the next navigation or
// resize. A navigation without an active override lays out at the window
// width. Chrome also drops a connection whose message id exceeds int32.
type fakeEmulatingChrome struct {
	mu          sync.Mutex
	layoutWidth int
	dpr         float64
	touch       bool
	methods     [][]string // per connection, in order
	badIDs      []int64
}

const fakeWindowWidth = 1440

func newFakeEmulatingChrome(t *testing.T) (*fakeEmulatingChrome, *Session) {
	t.Helper()
	fake := &fakeEmulatingChrome{layoutWidth: fakeWindowWidth, dpr: 1}
	var port string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json" || r.URL.Path == "/json/list" {
			_ = json.NewEncoder(w).Encode([]cdpTargetListEntry{{
				ID: "page", Type: "page", WebSocketDebuggerURL: "ws://127.0.0.1:" + port + "/devtools/page/page",
			}})
			return
		}
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		defer conn.Close()
		fake.serve(conn)
	}))
	t.Cleanup(server.Close)
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	p, _ := strconv.Atoi(port)
	return fake, &Session{Port: p, WSURL: "ws://127.0.0.1:" + port + "/devtools/browser/browser"}
}

func (f *fakeEmulatingChrome) serve(conn io.ReadWriter) {
	f.mu.Lock()
	f.methods = append(f.methods, nil)
	index := len(f.methods) - 1
	f.mu.Unlock()
	var override *DeviceEmulation // this connection's active override
	for {
		data, _, err := wsutil.ReadClientData(conn)
		if err != nil {
			return // connection closed: Chrome drops this session's override
		}
		var msg struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		f.mu.Lock()
		f.methods[index] = append(f.methods[index], msg.Method)
		if msg.ID > math.MaxInt32 {
			f.badIDs = append(f.badIDs, msg.ID)
			f.mu.Unlock()
			return
		}
		result := `{}`
		switch msg.Method {
		case "Emulation.setDeviceMetricsOverride":
			override = &DeviceEmulation{
				Width:             int(msg.Params["width"].(float64)),
				Height:            int(msg.Params["height"].(float64)),
				DeviceScaleFactor: msg.Params["deviceScaleFactor"].(float64),
				Mobile:            msg.Params["mobile"].(bool),
			}
			f.layoutWidth, f.dpr = override.Width, override.Scale()
		case "Emulation.clearDeviceMetricsOverride":
			override = nil
			f.layoutWidth, f.dpr = fakeWindowWidth, 1
		case "Emulation.setTouchEmulationEnabled":
			f.touch = msg.Params["enabled"].(bool)
		case "Page.navigate":
			if override == nil {
				f.layoutWidth, f.dpr, f.touch = fakeWindowWidth, 1, false
			}
		case "Runtime.evaluate":
			expr, _ := msg.Params["expression"].(string)
			switch {
			case expr == "document.readyState":
				result = `{"result":{"type":"string","value":"complete"}}`
			case expr == "innerWidth":
				result = fmt.Sprintf(`{"result":{"type":"number","value":%d}}`, f.layoutWidth)
			default:
				result = `{"result":{"type":"boolean","value":true}}`
			}
		case "Page.getLayoutMetrics":
			result = fmt.Sprintf(`{"cssVisualViewport":{"pageX":0,"pageY":0,"clientWidth":%d,"clientHeight":800},"cssContentSize":{"x":0,"y":0,"width":%d,"height":800}}`, f.layoutWidth, f.layoutWidth)
		case "Page.captureScreenshot":
			// The fake "PNG" encodes the captured CSS width and DPR.
			result = fmt.Sprintf(`{"data":%q}`, base64Std(fmt.Sprintf("w=%d dpr=%g", f.layoutWidth, f.dpr)))
		}
		f.mu.Unlock()
		if err := wsutil.WriteServerText(conn, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, msg.ID, result))); err != nil {
			return
		}
	}
}

func (f *fakeEmulatingChrome) snapshot() (width int, dpr float64, touch bool, badIDs []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.layoutWidth, f.dpr, f.touch, append([]int64(nil), f.badIDs...)
}

func (f *fakeEmulatingChrome) lastConnection() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.methods) == 0 {
		return nil
	}
	return append([]string(nil), f.methods[len(f.methods)-1]...)
}

func fakeInnerWidth(t *testing.T, s *Session) int {
	t.Helper()
	var width int
	if err := RemoteEval(context.Background(), s, "", "innerWidth", &width, 5*time.Second); err != nil {
		t.Fatalf("eval innerWidth: %v", err)
	}
	return width
}

// Regression (2026-11-15): `exec viewport 390x844` then `exec navigate`
// reverted to 1440 because each exec opened a new CDP connection and the
// override died with the previous one. The session now persists the
// emulation and every connection re-applies it before navigating.
func TestViewportSurvivesNavigateAcrossConnections(t *testing.T) {
	fake, sess := newFakeEmulatingChrome(t)
	ctx := context.Background()
	mobile := &DeviceEmulation{Width: 390, Height: 844, DeviceScaleFactor: 2, Mobile: true, Touch: true}
	if err := RemoteSetDeviceEmulation(ctx, sess, mobile, 5*time.Second); err != nil {
		t.Fatalf("set viewport: %v", err)
	}
	if sess.Emulation == nil || *sess.Emulation != *mobile {
		t.Fatalf("session emulation = %+v, want %+v", sess.Emulation, mobile)
	}
	for i, url := range []string{"http://127.0.0.1/one", "http://127.0.0.1/two"} {
		if err := RemoteNavigate(ctx, sess, url, 5*time.Second); err != nil {
			t.Fatalf("navigate %d: %v", i, err)
		}
		calls := fake.lastConnection()
		if len(calls) < 3 || calls[0] != "Emulation.setDeviceMetricsOverride" || calls[1] != "Emulation.setTouchEmulationEnabled" {
			t.Fatalf("navigate %d connection did not re-apply emulation first: %v", i, calls)
		}
		if got := fakeInnerWidth(t, sess); got != 390 {
			t.Fatalf("innerWidth after navigate %d = %d, want 390", i, got)
		}
	}
	png, err := RemoteScreenshotCurrentWithTimeout(sess, false, 5*time.Second)
	if err != nil {
		t.Fatalf("screenshot-current: %v", err)
	}
	if string(png) != "w=390 dpr=2" {
		t.Fatalf("screenshot-current captured %q, want w=390 dpr=2", png)
	}
	if _, _, touch, bad := fake.snapshot(); !touch || len(bad) != 0 {
		t.Fatalf("touch=%v bad ids=%v", touch, bad)
	}
}

func TestOneShotScreenshotViewportRestoresSessionEmulation(t *testing.T) {
	fake, sess := newFakeEmulatingChrome(t)
	ctx := context.Background()
	if err := RemoteSetDeviceEmulation(ctx, sess, &DeviceEmulation{Width: 390, Height: 844}, 5*time.Second); err != nil {
		t.Fatalf("set viewport: %v", err)
	}
	png, err := RemoteScreenshotCurrentWithOptions(ctx, sess, ScreenshotCurrentOptions{
		Timeout:      5 * time.Second,
		Emulation:    &DeviceEmulation{Width: 1280, Height: 800},
		Navigate:     "http://127.0.0.1/desktop",
		WaitSelector: "main",
	})
	if err != nil {
		t.Fatalf("one-shot screenshot: %v", err)
	}
	if string(png) != "w=1280 dpr=1" {
		t.Fatalf("one-shot captured %q, want w=1280 dpr=1", png)
	}
	if width, _, _, _ := fake.snapshot(); width != 390 {
		t.Fatalf("layout after one-shot = %d, want session viewport 390", width)
	}
	if sess.Emulation == nil || sess.Emulation.Width != 390 {
		t.Fatalf("one-shot changed persisted emulation: %+v", sess.Emulation)
	}
}

func TestViewportResetClearsOverrideAndPersistedState(t *testing.T) {
	fake, sess := newFakeEmulatingChrome(t)
	ctx := context.Background()
	if err := RemoteSetDeviceEmulation(ctx, sess, &DeviceEmulation{Width: 390, Height: 844, Touch: true}, 5*time.Second); err != nil {
		t.Fatalf("set viewport: %v", err)
	}
	if err := RemoteSetDeviceEmulation(ctx, sess, nil, 5*time.Second); err != nil {
		t.Fatalf("reset viewport: %v", err)
	}
	if sess.Emulation != nil {
		t.Fatalf("emulation after reset = %+v, want nil", sess.Emulation)
	}
	if err := RemoteNavigate(ctx, sess, "http://127.0.0.1/after-reset", 5*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	for _, method := range fake.lastConnection() {
		if strings.HasPrefix(method, "Emulation.") {
			t.Fatalf("navigate after reset sent %s", method)
		}
	}
	if width, _, touch, _ := fake.snapshot(); width != fakeWindowWidth || touch {
		t.Fatalf("after reset width=%d touch=%v, want %d/false", width, touch, fakeWindowWidth)
	}
}

func TestDeviceEmulationValidateAndString(t *testing.T) {
	for _, bad := range []DeviceEmulation{{Width: 0, Height: 10}, {Width: 10, Height: -1}, {Width: 20000, Height: 10}, {Width: 10, Height: 10, DeviceScaleFactor: 9}} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("Validate(%+v) = nil, want error", bad)
		}
	}
	got := DeviceEmulation{Width: 390, Height: 844, DeviceScaleFactor: 2, Mobile: true, Touch: true}.String()
	if got != "390x844@2x mobile touch" {
		t.Fatalf("String() = %q", got)
	}
	if got := (DeviceEmulation{Width: 1280, Height: 800}).String(); got != "1280x800" {
		t.Fatalf("String() = %q", got)
	}
	if emulationCallIDBase+1000 > math.MaxInt32 || waitCallIDBase+1_000_000 > math.MaxInt32 {
		t.Fatal("reserved CDP id ranges must stay below 2^31: Chrome drops the connection otherwise")
	}
}

// Real Chrome: the full sequence from the bug report, plus mobile DPR in the
// captured PNG and one-shot restore.
func TestViewportSurvivesNavigateRealChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head>
<body><main>viewport fixture</main></body></html>`)
	}))
	defer fixture.Close()

	const name = "viewport-persist-e2e"
	sess, err := Open(OpenOptions{Name: name, Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close(name, true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	mobile := &DeviceEmulation{Width: 390, Height: 844, DeviceScaleFactor: 2, Mobile: true, Touch: true}
	if err := RemoteSetDeviceEmulation(ctx, sess, mobile, 10*time.Second); err != nil {
		t.Fatalf("set viewport: %v", err)
	}
	// A later exec loads the session from disk: prove the record persisted.
	reloaded, err := LoadSession(name)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if reloaded.Emulation == nil || *reloaded.Emulation != *mobile {
		t.Fatalf("persisted emulation = %+v, want %+v", reloaded.Emulation, mobile)
	}
	type metrics struct {
		W     int     `json:"w"`
		DPR   float64 `json:"dpr"`
		Touch int     `json:"touch"`
	}
	read := func(label string) metrics {
		var m metrics
		if err := RemoteEval(ctx, reloaded, "", `({w: innerWidth, dpr: devicePixelRatio, touch: navigator.maxTouchPoints})`, &m, 10*time.Second); err != nil {
			t.Fatalf("%s eval: %v", label, err)
		}
		return m
	}
	for i := 0; i < 2; i++ {
		if err := RemoteNavigate(ctx, reloaded, fixture.URL+fmt.Sprintf("/?n=%d", i), 30*time.Second); err != nil {
			t.Fatalf("navigate %d: %v", i, err)
		}
		if m := read(fmt.Sprintf("after navigate %d", i)); m.W != 390 || m.DPR != 2 || m.Touch < 1 {
			t.Fatalf("after navigate %d: %+v, want 390 px, DPR 2, touch", i, m)
		}
	}
	png, err := RemoteScreenshotCurrentWithTimeout(reloaded, false, 20*time.Second)
	if err != nil {
		t.Fatalf("screenshot-current: %v", err)
	}
	if w, _ := pngSize(t, png); w != 780 {
		t.Fatalf("screenshot-current PNG width = %d, want 780 (390 CSS px at DPR 2)", w)
	}
	png, err = RemoteScreenshotCurrentWithOptions(ctx, reloaded, ScreenshotCurrentOptions{
		Timeout: 20 * time.Second, Emulation: &DeviceEmulation{Width: 1280, Height: 800},
	})
	if err != nil {
		t.Fatalf("one-shot screenshot: %v", err)
	}
	if w, _ := pngSize(t, png); w != 1280 {
		t.Fatalf("one-shot PNG width = %d, want 1280", w)
	}
	if m := read("after one-shot"); m.W != 390 || m.DPR != 2 {
		t.Fatalf("after one-shot: %+v, want session viewport restored", m)
	}
	if err := RemoteSetDeviceEmulation(ctx, reloaded, nil, 10*time.Second); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := RemoteNavigate(ctx, reloaded, fixture.URL+"/?reset", 30*time.Second); err != nil {
		t.Fatalf("navigate after reset: %v", err)
	}
	if m := read("after reset"); m.W == 390 || m.DPR != 1 {
		t.Fatalf("after reset: %+v, want window size and DPR 1", m)
	}
}

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func pngSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	return cfg.Width, cfg.Height
}

// plainWidth reads innerWidth and the recorded media-query flips over a
// connection that applies no emulation, as an exec call does while a holder
// serves the viewport. It shows the page's state between calls.
func plainWidth(t *testing.T, s *Session) (int, []bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialFirstPageNoEmulation(ctx, s)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	defer conn.Close()
	raw, err := cdpCall(ctx, conn, 1, "Runtime.evaluate", map[string]any{
		"expression": `JSON.stringify({w: innerWidth, hits: window.hits || []})`, "returnByValue": true,
	})
	if err != nil {
		t.Fatalf("plain eval: %v", err)
	}
	var r struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	var v struct {
		W    int    `json:"w"`
		Hits []bool `json:"hits"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || json.Unmarshal([]byte(r.Result.Value), &v) != nil {
		t.Fatalf("plain eval decode: %s", raw)
	}
	return v.W, v.Hits
}

// plainDPR reads devicePixelRatio over a connection that applies no
// emulation.
func plainDPR(t *testing.T, s *Session) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialFirstPageNoEmulation(ctx, s)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	defer conn.Close()
	dpr, err := pageDevicePixelRatio(ctx, conn, 1)
	if err != nil {
		t.Fatalf("plain DPR: %v", err)
	}
	return dpr
}

// foreignClipCapture sends a clipped capture over a connection without the
// override, as a client from before the holder capture would.
func foreignClipCapture(t *testing.T, s *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialFirstPageNoEmulation(ctx, s)
	if err != nil {
		t.Fatalf("foreign dial: %v", err)
	}
	defer conn.Close()
	if _, err := cdpCall(ctx, conn, 1, "Page.captureScreenshot", map[string]any{
		"fromSurface": true,
		"clip":        map[string]any{"x": 0, "y": 0, "width": 390, "height": 844, "scale": 1},
	}); err != nil {
		t.Fatalf("foreign capture: %v", err)
	}
}

// Real Chrome, WORK-1320: the viewport holds between calls. Before the
// holder, each call applied the override on its own connection and Chrome
// dropped it when the call ended, so between calls the page was back at the
// window width and a (max-width) listener flipped on every call.
func TestViewportHoldsBetweenCallsRealChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	requireManagedChromeForTesting(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head>
<body><main>hold fixture</main><script>window.hits = [];
matchMedia("(max-width: 760px)").addEventListener("change", e => hits.push(e.matches));</script></body></html>`)
	}))
	defer fixture.Close()

	holdCtx, stopHolders := context.WithCancel(context.Background())
	held := make(chan error, 4)
	prev := EmulationHolderLauncher
	EmulationHolderLauncher = func(name string, pid int) error {
		go func() { held <- HoldSessionEmulation(holdCtx, name, pid) }()
		return nil
	}
	defer func() { stopHolders(); EmulationHolderLauncher = prev }()

	const name = "viewport-hold-e2e"
	sess, err := Open(OpenOptions{Name: name, Runtime: RuntimeChromeForTesting, ReadyTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer Close(name, true)
	ctx := context.Background()
	if err := RemoteNavigate(ctx, sess, fixture.URL, 30*time.Second); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if w, _ := plainWidth(t, sess); w <= 760 {
		t.Fatalf("window width %d: the fixture needs a desktop start", w)
	}
	mobile := &DeviceEmulation{Width: 390, Height: 844, DeviceScaleFactor: 2, Mobile: true, Touch: true}
	if err := RemoteSetDeviceEmulation(ctx, sess, mobile, 10*time.Second); err != nil {
		t.Fatalf("set viewport: %v", err)
	}
	for i := 0; i < 3; i++ {
		s, err := LoadSession(name) // each exec loads the record afresh
		if err != nil {
			t.Fatal(err)
		}
		var w int
		if err := RemoteEval(ctx, s, "", `innerWidth`, &w, 10*time.Second); err != nil || w != 390 {
			t.Fatalf("call %d: innerWidth %d, err %v", i, w, err)
		}
		if w, hits := plainWidth(t, s); w != 390 || len(hits) != 1 || !hits[0] {
			t.Fatalf("between calls after call %d: width %d, media flips %v; want 390 and [true]", i, w, hits)
		}
	}

	// WORK-1428: captures run on the holder's connection, in device pixels,
	// and leave the page at the held DPR (an exec-connection capture came
	// out at DPR 1 and reset the page to DPR 1).
	s, _ := LoadSession(name)
	for _, beyond := range []bool{false, true} {
		png, err := RemoteScreenshotCurrentWithOptions(ctx, s, ScreenshotCurrentOptions{Timeout: 20 * time.Second, CaptureBeyondViewport: beyond})
		if err != nil {
			t.Fatalf("screenshot-current (full page %v): %v", beyond, err)
		}
		if w, h := pngSize(t, png); w != 780 || (!beyond && h != 1688) {
			t.Fatalf("screenshot-current (full page %v) = %dx%d, want 780 wide (and 1688 high for the viewport)", beyond, w, h)
		}
		if dpr := plainDPR(t, s); dpr != 2 {
			t.Fatalf("after capture (full page %v): page DPR %g, want 2", beyond, dpr)
		}
	}
	png, err := RemoteScreenshotWithCapture(ctx, s, fixture.URL+"/?shot=1", 20*time.Second, false)
	if err != nil {
		t.Fatalf("url screenshot: %v", err)
	}
	if w, _ := pngSize(t, png); w != 780 {
		t.Fatalf("url screenshot width = %d, want 780", w)
	}
	if w, hits := plainWidth(t, s); w != 390 || len(hits) != 0 {
		// The navigation reloaded the page: no media flips since then.
		t.Fatalf("after url capture: width %d, media flips %v", w, hits)
	}

	// A clipped capture from a connection without the override (an older
	// client) resets the page to DPR 1; the holder sees the drift and
	// re-applies the held DPR without changing the width.
	foreignClipCapture(t, s)
	deadline := time.Now().Add(3 * time.Second)
	for plainDPR(t, s) != 2 {
		if time.Now().After(deadline) {
			t.Fatal("holder did not repair the DPR after a foreign capture")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// A one-shot capture size ends with its connection; the holder restores
	// the width and the DPR.
	if _, err := RemoteScreenshotCurrentWithOptions(ctx, s, ScreenshotCurrentOptions{
		Timeout: 20 * time.Second, Emulation: &DeviceEmulation{Width: 1280, Height: 800},
	}); err != nil {
		t.Fatalf("one-shot screenshot: %v", err)
	}
	if w, _ := plainWidth(t, s); w != 390 {
		t.Fatalf("after one-shot capture: width %d, want 390", w)
	}
	if dpr := plainDPR(t, s); dpr != 2 {
		t.Fatalf("after one-shot capture: page DPR %g, want 2", dpr)
	}

	// Clearing stops the holder and the page returns to the window width.
	if err := RemoteSetDeviceEmulation(ctx, s, nil, 10*time.Second); err != nil {
		t.Fatalf("clear viewport: %v", err)
	}
	select {
	case err := <-held:
		if err != nil {
			t.Fatalf("holder: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("holder still running after clear")
	}
	cleared, _ := LoadSession(name)
	if cleared.EmulationHolder != nil {
		t.Fatalf("holder record left after clear: %+v", cleared.EmulationHolder)
	}
	if w, _ := plainWidth(t, cleared); w <= 760 {
		t.Fatalf("after clear: width %d, want the desktop window", w)
	}
}
