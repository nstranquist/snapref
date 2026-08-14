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
	home := os.Getenv("NDEV_BROWSER_HOME")
	if home == "" {
		home = os.Getenv("PAGESKEIN_HOME")
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("managed Chrome for Testing unavailable: resolve browser home: %v", err)
		}
		home = filepath.Join(userHome, ".nicos-dev", "browser")
	}
	runtimes := filepath.Join(home, "runtimes")
	if info, err := os.Stat(runtimes); err != nil || !info.IsDir() {
		t.Skipf("managed Chrome for Testing unavailable: run `ndev browser runtime install --runtime chrome-for-testing --channel stable` (%v)", err)
	}
	if err := os.Symlink(runtimes, filepath.Join(isolatedHome, "runtimes")); err != nil {
		t.Skipf("managed Chrome for Testing unavailable in isolated test home: %v", err)
	}
	t.Setenv("PAGESKEIN_HOME", isolatedHome)
	t.Setenv("PAGESKEIN_CHROME", "")
	t.Setenv("NDEV_BROWSER_CHROME", "")
	if _, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeChromeForTesting}); err != nil {
		t.Skipf("managed Chrome for Testing unavailable: %v; run `ndev browser runtime install --runtime chrome-for-testing --channel stable`", err)
	}
}
