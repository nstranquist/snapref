package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// EvalPromise is like RemoteEval but awaits a returned Promise. Required
// when the JS calls `chrome.runtime.sendMessage`-style callback APIs that
// we wrap in `new Promise(...)`.
func EvalPromise(ctx context.Context, s *Session, url, js string, out any, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	// Raw CDP with awaitPromise (see remote_raw.go) so the session's page target
	// survives across exec calls — and a returned Promise resolves to its value.
	return rawEvalInto(ctx, s, url, js, true, out, timeout)
}

// EvalInNewTab opens a fresh tab at url through Chrome's remote debugging
// HTTP endpoint, attaches, and evaluates js with promise-await. Use this for
// chrome-extension:// URLs where Page.navigate from an existing tab fails with
// ERR_FILE_NOT_FOUND under headless+extension combos.
func EvalInNewTab(ctx context.Context, s *Session, url, js string, out any, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, s.WSURL)
	defer cancelAlloc()

	newTargetID, err := createDevToolsTarget(ctx, s, url, timeout)
	if err != nil {
		return err
	}
	defer func() {
		// Best-effort cleanup; ignore error so test failures show their root cause.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		_ = closeDevToolsTarget(cleanupCtx, s, newTargetID, 3*time.Second)
	}()

	tabCtx, cancelTab := chromedp.NewContext(allocCtx, chromedp.WithTargetID(newTargetID))
	defer cancelTab()
	// Wait for the page to settle. WaitReady is unreliable for chrome-extension
	// URLs; use a short fixed delay then evaluate.
	if err := chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		// Poll for document existence rather than fixed sleep.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var ready bool
			res, _, err := runtime.Evaluate("document && document.readyState !== 'uninitialized'").
				WithReturnByValue(true).Do(ctx)
			if err == nil && res != nil && len(res.Value) > 0 {
				_ = chromedpUnmarshal(res.Value, &ready)
				if ready {
					return nil
				}
			}
			time.Sleep(150 * time.Millisecond)
		}
		return fmt.Errorf("document never became ready in new tab")
	})); err != nil {
		return err
	}

	return chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return runEvalPromise(ctx, js, out)
	}))
}

func createDevToolsTarget(ctx context.Context, s *Session, pageURL string, timeout time.Duration) (target.ID, error) {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	reqURL := fmt.Sprintf("http://127.0.0.1:%d/json/new?%s", s.Port, url.QueryEscape(pageURL))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create target returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("create target returned no id")
	}
	return target.ID(created.ID), nil
}

func closeDevToolsTarget(ctx context.Context, s *Session, id target.ID, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	reqURL := fmt.Sprintf("http://127.0.0.1:%d/json/close/%s", s.Port, url.PathEscape(string(id)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("close target returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// EvalInExtensionSW attaches to the loaded extension's service worker
// and evaluates js with promise-await. The session must have been opened
// with ExtensionPath set, so ExtensionID is populated.
//
// SW context is the right target for tests that need full chrome.*
// access (storage, runtime.sendMessage, scripting) without the
// headless-mode chrome-extension://… page-load quirks.
func EvalInExtensionSW(ctx context.Context, s *Session, js string, out any, timeout time.Duration) error {
	if s.ExtensionID == "" {
		return fmt.Errorf("session %q has no recorded ExtensionID", s.Name)
	}
	prefix := "chrome-extension://" + s.ExtensionID + "/"
	return rawEvalIntoTarget(ctx, s, "service_worker", prefix, js, true, out, timeout)
}

// EvalPromiseInTarget evaluates promise-returning js inside the first CDP
// target whose URL starts with targetURLPrefix. Use this for extension
// pages such as the action popup where the test needs DOM events and async
// chrome.runtime responses from that exact page target.
func EvalPromiseInTarget(ctx context.Context, s *Session, targetURLPrefix string, js string, out any, timeout time.Duration) error {
	return rawEvalIntoTarget(ctx, s, "", targetURLPrefix, js, true, out, timeout)
}

func runEvalPromise(ctx context.Context, js string, out any) error {
	res, exc, err := runtime.Evaluate(js).
		WithAwaitPromise(true).
		WithReturnByValue(true).
		Do(ctx)
	if err != nil {
		return err
	}
	if exc != nil {
		return fmt.Errorf("js exception: %s", exc.Text)
	}
	if res == nil || len(res.Value) == 0 || out == nil {
		return nil
	}
	return chromedpUnmarshal(res.Value, out)
}

// chromedpUnmarshal handles the JSON-decoded value returned by
// runtime.Evaluate. Pulled out to keep the test surface narrow.
func chromedpUnmarshal(raw []byte, out any) error {
	return jsonUnmarshalLoose(raw, out)
}
