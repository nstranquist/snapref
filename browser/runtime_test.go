package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRuntimeKindClosed(t *testing.T) {
	for _, raw := range []string{"", "auto", "headless-shell", "chrome-for-testing", "external"} {
		if _, err := ParseRuntimeKind(raw); err != nil {
			t.Fatalf("ParseRuntimeKind(%q): %v", raw, err)
		}
	}
	if _, err := ParseRuntimeKind("chrome"); err == nil {
		t.Fatal("unknown runtime accepted")
	}
}

func TestResolveRuntimeFailsClosedWithoutManagedSelection(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	t.Setenv("PAGESKEIN_CHROME", "")
	t.Setenv("NDEV_BROWSER_CHROME", "")
	_, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeAuto})
	if err == nil || !strings.Contains(err.Error(), "runtime install --runtime chrome-for-testing") {
		t.Fatalf("ResolveRuntime error = %v", err)
	}
}

func TestResolveRuntimeRejectsIncompatibleShellBeforeInstallLookup(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	_, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeHeadlessShell, Headful: true})
	if err == nil || !strings.Contains(err.Error(), "does not support headful") {
		t.Fatalf("headful shell error = %v", err)
	}
	_, err = ResolveRuntime(RuntimeRequest{Kind: RuntimeHeadlessShell, ExtensionPath: "/tmp/ext"})
	if err == nil || !strings.Contains(err.Error(), "does not support unpacked extensions") {
		t.Fatalf("extension shell error = %v", err)
	}
}

func TestResolveRuntimeUsesExplicitExternalOnly(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	t.Setenv("PAGESKEIN_CHROME", os.Args[0])
	previousProbe := probeRuntimeVersion
	probeRuntimeVersion = func(string) (string, error) { return "Chromium 132.0.6834.0", nil }
	t.Cleanup(func() { probeRuntimeVersion = previousProbe })
	resolved, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Kind != RuntimeExternal || resolved.Managed || resolved.Path != os.Args[0] {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestInstalledRuntimeVerificationAndSelection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAGESKEIN_HOME", home)
	t.Setenv("PAGESKEIN_CHROME", "")
	t.Setenv("NDEV_BROWSER_CHROME", "")
	platform, err := runtimePlatform()
	if err != nil {
		t.Fatal(err)
	}
	version := "132.0.6834.0"
	dir, err := runtimeInstallDir(RuntimeChromeForTesting, platform, version)
	if err != nil {
		t.Fatal(err)
	}
	binRel := filepath.Join("browser", "chrome")
	binPath := filepath.Join(dir, binRel)
	if err := os.MkdirAll(filepath.Dir(binPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("runtime-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("runtime-binary"))
	manifest := RuntimeManifest{
		SchemaVersion: runtimeSchemaVersion, RuntimeKind: RuntimeChromeForTesting,
		Version: version, RequestedChannel: "stable", Platform: platform,
		SourceURL:   "https://storage.googleapis.com/chrome-for-testing-public/test.zip",
		InstalledAt: time.Now().UTC(), ArchiveSHA256: strings.Repeat("a", 64),
		BinarySHA256: hex.EncodeToString(digest[:]), BinaryRelPath: binRel,
		VersionOutput: "Google Chrome for Testing " + version,
		Capabilities:  RuntimeCapabilities{Headless: true, Headful: true, Extensions: true, Screenshots: true},
	}
	if err := writeJSONAtomic(filepath.Join(dir, "runtime.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveRuntimeSelections(RuntimeSelections{Selected: map[RuntimeKind]RuntimeSelection{
		RuntimeChromeForTesting: {Platform: platform, Version: version},
	}}); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Managed || resolved.Kind != RuntimeChromeForTesting || resolved.ManifestDigest == "" {
		t.Fatalf("resolved = %+v", resolved)
	}
	previousProbe := probeRuntimeVersion
	probeRuntimeVersion = func(string) (string, error) { return "Google Chrome for Testing 999.0.0.0", nil }
	records, verifyErr := VerifyRuntimes(RuntimeChromeForTesting)
	probeRuntimeVersion = previousProbe
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
	if len(records) != 1 || records[0].Verified || !strings.Contains(records[0].Error, "does not contain expected") {
		t.Fatalf("deep verify records = %+v", records)
	}
	// Launch resolution intentionally remains digest-based and does not pay the
	// process-start cost of explicit deep verification on every operation.
	if _, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeAuto}); err != nil {
		t.Fatalf("fast resolve after deep verify = %v", err)
	}
	if err := os.WriteFile(binPath, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeAuto}); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered runtime error = %v", err)
	}
}

func TestContainedRuntimePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"", ".", "../chrome", "/tmp/chrome"} {
		if _, err := containedRuntimePath(root, rel); err == nil {
			t.Fatalf("containedRuntimePath(%q) accepted", rel)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "chrome-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := containedRuntimePath(root, "chrome-link"); err == nil || !strings.Contains(err.Error(), "symlink escapes") {
		t.Fatalf("escaping executable symlink error = %v", err)
	}
}

func TestHeadlessShellAcceptanceMustMatchVersionsAndThresholds(t *testing.T) {
	useRuntimeTestBuildVersion(t)
	home := t.TempDir()
	t.Setenv("PAGESKEIN_HOME", home)
	platform, err := runtimePlatform()
	if err != nil {
		t.Fatal(err)
	}
	selections := RuntimeSelections{SchemaVersion: 1, Selected: map[RuntimeKind]RuntimeSelection{
		RuntimeHeadlessShell:    {Platform: platform, Version: "132.0.6834.0"},
		RuntimeChromeForTesting: {Platform: platform, Version: "132.0.6834.0"},
	}}
	acceptance := RuntimeAcceptance{
		SchemaVersion: 1, PolicyVersion: runtimeAcceptancePolicyVersion, Platform: platform,
		HeadlessShellVersion: "132.0.6834.0", ChromeForTestingVersion: "132.0.6834.0",
		TestedAt: time.Now().UTC(), Samples: 30, FunctionalPass: true, VisualPass: true,
		FocusIsolationStatus: "pass", ConsumerPass: true, Pass: true,
		ConsumerEvidence: passingRuntimeConsumerEvidence(t, platform, "132.0.6834.0", "132.0.6834.0"),
		Metrics: RuntimeAcceptanceMetrics{
			HeadlessShellColdP50MS: 80, ChromeForTestingColdP50MS: 100,
			HeadlessShellMedianRSSBytes: 70, ChromeForTestingMedianRSSBytes: 100,
		},
	}
	path, err := runtimeAcceptancePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(path, acceptance, 0o600); err != nil {
		t.Fatal(err)
	}
	if !headlessShellAccepted(platform, selections) {
		t.Fatal("matching passing acceptance rejected")
	}
	acceptance.HeadlessShellVersion = "133.0.0.0"
	if err := writeJSONAtomic(path, acceptance, 0o600); err != nil {
		t.Fatal(err)
	}
	if headlessShellAccepted(platform, selections) {
		t.Fatal("stale acceptance accepted")
	}
	acceptance.HeadlessShellVersion = "132.0.6834.0"
	if err := writeJSONAtomic(path, acceptance, 0o600); err != nil {
		t.Fatal(err)
	}
	acceptance.ConsumerEvidence.PageSkeinBuildVersion = "other-build"
	if err := writeJSONAtomic(path, acceptance, 0o600); err != nil {
		t.Fatal(err)
	}
	if headlessShellAccepted(platform, selections) {
		t.Fatal("cross-build consumer evidence accepted")
	}
	acceptance.ConsumerEvidence.PageSkeinBuildVersion = runtimeTestBuildVersion
	if err := writeJSONAtomic(path, acceptance, 0o600); err != nil {
		t.Fatal(err)
	}
	selections.Selected[RuntimeChromeForTesting] = RuntimeSelection{Platform: platform, Version: "133.0.0.0"}
	if headlessShellAccepted(platform, selections) {
		t.Fatal("cross-version runtime pair accepted")
	}
}

func TestRuntimeKindLocksSerializeAndHonorTimeout(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	unlock, err := acquireRuntimeKindLocks([]RuntimeKind{RuntimeChromeForTesting}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	started := time.Now()
	secondUnlock, err := acquireRuntimeKindLocks([]RuntimeKind{RuntimeChromeForTesting}, 75*time.Millisecond)
	if secondUnlock != nil {
		secondUnlock()
	}
	if err == nil {
		t.Fatal("concurrent runtime mutation lock unexpectedly succeeded")
	}
	if time.Since(started) < 50*time.Millisecond {
		t.Fatalf("runtime lock timeout returned too early: %s", time.Since(started))
	}
}

func TestRuntimeLockKindsCoverAutomaticResolution(t *testing.T) {
	if got := runtimeLockKindsForRequest(RuntimeRequest{Kind: RuntimeExternal, ExternalPath: "/tmp/chrome"}); len(got) != 0 {
		t.Fatalf("external lock kinds = %v", got)
	}
	if got := runtimeLockKindsForRequest(RuntimeRequest{Kind: RuntimeAuto, Headful: true}); len(got) != 1 || got[0] != RuntimeChromeForTesting {
		t.Fatalf("headful auto lock kinds = %v", got)
	}
	if got := runtimeLockKindsForRequest(RuntimeRequest{Kind: RuntimeAuto}); len(got) != 2 || got[0] != RuntimeChromeForTesting || got[1] != RuntimeHeadlessShell {
		t.Fatalf("ordinary auto lock kinds = %v", got)
	}
}

func TestResolveRuntimeAutoRoutesVersionMatchedManagedCapabilities(t *testing.T) {
	useRuntimeTestBuildVersion(t)
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	t.Setenv("PAGESKEIN_CHROME", "")
	t.Setenv("NDEV_BROWSER_CHROME", "")
	platform, err := runtimePlatform()
	if err != nil {
		t.Fatal(err)
	}
	const version = "151.0.0.0"
	writeManagedRuntimeFixture(t, RuntimeHeadlessShell, platform, version)
	writeManagedRuntimeFixture(t, RuntimeChromeForTesting, platform, version)
	selections := RuntimeSelections{Selected: map[RuntimeKind]RuntimeSelection{
		RuntimeHeadlessShell:    {Platform: platform, Version: version},
		RuntimeChromeForTesting: {Platform: platform, Version: version},
	}}
	if err := saveRuntimeSelections(selections); err != nil {
		t.Fatal(err)
	}
	acceptance := RuntimeAcceptance{
		SchemaVersion: runtimeSchemaVersion, PolicyVersion: runtimeAcceptancePolicyVersion,
		Platform: platform, HeadlessShellVersion: version, ChromeForTestingVersion: version,
		TestedAt: time.Now().UTC(), Samples: 30, FunctionalPass: true, VisualPass: true,
		FocusIsolationStatus: "pass", ConsumerPass: true, Pass: true,
		ConsumerEvidence: passingRuntimeConsumerEvidence(t, platform, version, version),
		Metrics: RuntimeAcceptanceMetrics{
			HeadlessShellColdP50MS: 80, ChromeForTestingColdP50MS: 100,
			HeadlessShellMedianRSSBytes: 70, ChromeForTestingMedianRSSBytes: 100,
		},
	}
	acceptancePath, _ := runtimeAcceptancePath()
	if err := writeJSONAtomic(acceptancePath, acceptance, 0o600); err != nil {
		t.Fatal(err)
	}

	ordinary, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeAuto})
	if err != nil || ordinary.Kind != RuntimeHeadlessShell {
		t.Fatalf("ordinary auto = %+v err=%v", ordinary, err)
	}
	for name, request := range map[string]RuntimeRequest{
		"headful":   {Kind: RuntimeAuto, Headful: true},
		"extension": {Kind: RuntimeAuto, ExtensionPath: "/tmp/extension"},
	} {
		t.Run(name, func(t *testing.T) {
			resolved, err := ResolveRuntime(request)
			if err != nil || resolved.Kind != RuntimeChromeForTesting {
				t.Fatalf("capability auto = %+v err=%v", resolved, err)
			}
		})
	}
}

func TestRuntimeManifestIdentityValidationMatrix(t *testing.T) {
	mutations := map[string]func(*RuntimeManifest){
		"schema":       func(m *RuntimeManifest) { m.SchemaVersion++ },
		"kind":         func(m *RuntimeManifest) { m.RuntimeKind = RuntimeHeadlessShell },
		"platform":     func(m *RuntimeManifest) { m.Platform = "wrong-platform" },
		"version":      func(m *RuntimeManifest) { m.Version = "150.0.0.0" },
		"provenance":   func(m *RuntimeManifest) { m.InstalledAt = time.Time{} },
		"digest":       func(m *RuntimeManifest) { m.ArchiveSHA256 = "not-a-digest" },
		"capabilities": func(m *RuntimeManifest) { m.Capabilities.Extensions = false },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PAGESKEIN_HOME", t.TempDir())
			platform, err := runtimePlatform()
			if err != nil {
				t.Fatal(err)
			}
			const version = "151.0.0.0"
			writeManagedRuntimeFixture(t, RuntimeChromeForTesting, platform, version)
			dir, _ := runtimeInstallDir(RuntimeChromeForTesting, platform, version)
			path := filepath.Join(dir, "runtime.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest RuntimeManifest
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			mutate(&manifest)
			if err := writeJSONAtomic(path, manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := verifyInstalledRuntime(RuntimeChromeForTesting, platform, version); err == nil {
				t.Fatal("invalid runtime manifest accepted")
			}
		})
	}
}

func TestListRuntimeRecordsSortsVersionsDeterministically(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	platform, err := runtimePlatform()
	if err != nil {
		t.Fatal(err)
	}
	writeManagedRuntimeFixture(t, RuntimeChromeForTesting, platform, "151.0.0.2")
	writeManagedRuntimeFixture(t, RuntimeChromeForTesting, platform, "151.0.0.1")
	records, err := ListRuntimeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Version != "151.0.0.1" || records[1].Version != "151.0.0.2" {
		t.Fatalf("runtime records = %+v", records)
	}
}
