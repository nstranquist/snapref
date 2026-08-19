package main

import (
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

func TestDemoRefIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in -short mode")
	}
	isolateManagedChromeForTesting(t)
	receipt, err := runRefIntegrityDemo(browser.RuntimeChromeForTesting)
	if err != nil {
		if strings.Contains(err.Error(), "Chrome for Testing") || strings.Contains(err.Error(), "no Chromium") {
			t.Skip(err.Error())
		}
		t.Fatal(err)
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
