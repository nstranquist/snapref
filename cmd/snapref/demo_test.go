package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/snapref/browser"
)

func TestDemoRefIntegrityFixtureMutatesAccessibleName(t *testing.T) {
	html := refIntegrityFixtureHTML()
	if !strings.Contains(html, `id="target"`) {
		t.Fatalf("fixture missing #target: %s", html)
	}
	if !strings.Contains(html, "Original") || !strings.Contains(html, "Clicked") {
		t.Fatalf("fixture must change accessible name on click: %s", html)
	}
	if !strings.Contains(html, "dataset.clicked") {
		t.Fatalf("fixture must record the click: %s", html)
	}
}

func TestDemoCLIHelp(t *testing.T) {
	for _, want := range []string{
		"snapref demo mcp-policy",
		"snapref demo ref-integrity",
		"does not launch",
		"fails closed",
	} {
		if !strings.Contains(demoHelpText, want) {
			t.Fatalf("demo help missing %q:\n%s", want, demoHelpText)
		}
	}
	if strings.Contains(strings.ToLower(demoHelpText), "jobkit") {
		t.Fatal("demo help leaked JobKit")
	}
}

func TestParseDemoRuntime(t *testing.T) {
	got, err := parseDemoRuntime(nil)
	if err != nil || got != browser.RuntimeAuto {
		t.Fatalf("default runtime = %q, %v", got, err)
	}
	got, err = parseDemoRuntime([]string{"--runtime", "chrome-for-testing"})
	if err != nil || got != browser.RuntimeChromeForTesting {
		t.Fatalf("flag runtime = %q, %v", got, err)
	}
	if _, err := parseDemoRuntime([]string{"--headful"}); err == nil {
		t.Fatal("unexpected argument was accepted")
	}
}

func TestEmitDemoReceiptWritesJSONThenReturnsRunError(t *testing.T) {
	stdout, restore := captureStdout(t)
	err := emitDemoReceipt(map[string]any{"ok": false, "workflow": "mcp-policy"}, errors.New("mcp-policy: click without drive must fail closed"))
	restore()
	if err == nil || !strings.Contains(err.Error(), "click without drive") {
		t.Fatalf("run error = %v", err)
	}
	out := stdout()
	if !strings.Contains(out, `"ok": false`) || !strings.Contains(out, `"workflow": "mcp-policy"`) {
		t.Fatalf("partial receipt missing: %s", out)
	}
}

func TestDemoRefIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	isolateManagedChromeForTesting(t)
	stdout, restore := captureStdout(t)
	err := demoCommand([]string{"ref-integrity", "--runtime", "chrome-for-testing"})
	restore()
	if err != nil {
		t.Fatal(err)
	}
	var receipt refIntegrityReceipt
	if json.Unmarshal([]byte(stdout()), &receipt) != nil {
		t.Fatalf("receipt json: %s", stdout())
	}
	if !receipt.OK || !receipt.Clicked || !receipt.StaleRejected || receipt.FirstRef == "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.FirstRevision == "" || receipt.SecondRevision == "" || receipt.FirstRevision == receipt.SecondRevision {
		t.Fatalf("revisions were not superseded: %+v", receipt)
	}
	if !strings.Contains(receipt.StaleError, "stale") {
		t.Fatalf("stale error = %q", receipt.StaleError)
	}
}

func isolateManagedChromeForTesting(t *testing.T) {
	t.Helper()
	isolatedHome := t.TempDir()
	home, err := browser.SessionRoot()
	if err != nil {
		t.Skipf("managed Chrome for Testing unavailable: resolve browser home: %v", err)
	}
	runtimes := filepath.Join(home, "runtimes")
	if info, err := os.Stat(runtimes); err != nil || !info.IsDir() {
		t.Skipf("managed Chrome for Testing unavailable: run `snapref runtime install --runtime chrome-for-testing --channel stable` (%v)", err)
	}
	if err := os.Symlink(runtimes, filepath.Join(isolatedHome, "runtimes")); err != nil {
		t.Skipf("managed Chrome for Testing unavailable in isolated test home: %v", err)
	}
	t.Setenv("SNAPREF_HOME", isolatedHome)
	t.Setenv("SNAPREF_CHROME", "")
	t.Setenv("NDEV_BROWSER_CHROME", "")
	if _, err := browser.ResolveRuntime(browser.RuntimeRequest{Kind: browser.RuntimeChromeForTesting}); err != nil {
		t.Skipf("managed Chrome for Testing unavailable: %v; run `snapref runtime install --runtime chrome-for-testing --channel stable`", err)
	}
}
