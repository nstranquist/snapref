package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeadlessOnlyBlocksLaunchAndAllConnectionPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SNAPREF_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "headless-only"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	s := &Session{Headful: true, Port: 1, WSURL: "ws://127.0.0.1:1/devtools/browser/test"}
	checks := map[string]func() error{
		"launch": func() error { _, err := Open(OpenOptions{Name: "denied", Headful: true}); return err },
		"offscreen": func() error {
			_, err := Open(OpenOptions{Name: "denied", Headful: true, HeadfulOffscreen: true})
			return err
		},
		"stateless":  func() error { _, err := (&LaunchOptions{}).normalize(); return err },
		"remote":     func() error { _, _, _, err := remoteCtx(context.Background(), s); return err },
		"raw":        func() error { _, err := dialFirstPage(context.Background(), s); return err },
		"target":     func() error { return EvalInTarget(context.Background(), s, "", "1", nil, time.Second) },
		"screenshot": func() error { _, err := RemoteScreenshotCurrentWithTimeout(s, false, time.Second); return err },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); err == nil || !strings.Contains(err.Error(), "disabled by host policy") {
				t.Fatalf("expected rejection before contacting browser, got %v", err)
			}
		})
	}
	if err := CheckForegroundPolicy(false); err != nil {
		t.Fatal(err)
	}
}

func TestForegroundPolicyAbsentPreservesExplicitHeadfulSupport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SNAPREF_HOME", t.TempDir())
	if err := CheckForegroundPolicy(true); err != nil {
		t.Fatal(err)
	}
}

func TestUserWidePolicySurvivesSessionRootOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SNAPREF_HOME", t.TempDir())
	dir := filepath.Join(home, ".config", "snapref")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "headless-only"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckForegroundPolicy(true); err == nil {
		t.Fatal("alternate session root bypassed user-wide policy")
	}
}
