package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// requireManagedChromeForTesting gives real-browser tests an isolated session
// home backed by the operator's immutable managed-runtime installations. It
// never searches for or exports a host/personal browser executable.
func requireManagedChromeForTesting(t *testing.T) {
	t.Helper()
	isolatedHome := t.TempDir()
	home, err := SessionRoot()
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
	if _, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeChromeForTesting}); err != nil {
		t.Skipf("managed Chrome for Testing unavailable: %v; run `snapref runtime install --runtime chrome-for-testing --channel stable`", err)
	}
}
