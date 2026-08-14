package browser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInstallRuntimeStableIsVerifiedAtomicAndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAGESKEIN_HOME", home)
	t.Setenv("PAGESKEIN_CHROME", "")
	t.Setenv("NDEV_BROWSER_CHROME", "")
	version := "132.0.6834.0"
	archive := runtimeZipFixture(t, "chrome-headless-shell-mac/chrome-headless-shell", []byte("fake-shell"))
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stable.json":
			platform, _ := runtimePlatform()
			cftPlatform, _ := chromeForTestingPlatform(platform)
			_ = json.NewEncoder(w).Encode(cftLastKnownGood{Channels: map[string]cftVersion{
				"Stable": {Version: version, Downloads: cftDownloads{HeadlessShell: []cftDownload{{Platform: cftPlatform, URL: server.URL + "/shell.zip"}}}},
			}})
		case "/shell.zip":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	source := runtimeDownloadSource{
		knownGoodURL: server.URL + "/known.json", stableURL: server.URL + "/stable.json",
		allowedHost: map[string]bool{parsed.Hostname(): true}, client: server.Client(),
	}
	previousProbe := probeRuntimeVersion
	probeRuntimeVersion = func(string) (string, error) { return "HeadlessChrome/" + version, nil }
	t.Cleanup(func() { probeRuntimeVersion = previousProbe })

	result, err := installRuntime(context.Background(), RuntimeInstallOptions{Kind: RuntimeHeadlessShell, Channel: "stable"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || !result.Selected || !result.Runtime.Verified || result.Runtime.Kind != RuntimeHeadlessShell {
		t.Fatalf("install result = %+v", result)
	}
	resolved, err := ResolveRuntime(RuntimeRequest{Kind: RuntimeHeadlessShell})
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Managed || resolved.Version != version || !strings.Contains(resolved.Path, "chrome-headless-shell") {
		t.Fatalf("resolved = %+v", resolved)
	}
	second, err := installRuntime(context.Background(), RuntimeInstallOptions{Kind: RuntimeHeadlessShell, Channel: "stable"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if second.Downloaded || !second.Selected {
		t.Fatalf("idempotent install = %+v", second)
	}
}

func TestInstallRuntimeExactVersionUsesKnownGoodMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAGESKEIN_HOME", home)
	version := "132.0.6834.0"
	archive := runtimeZipFixture(t, "chrome/chrome", []byte("fake-chrome"))
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platform, _ := runtimePlatform()
		cftPlatform, _ := chromeForTestingPlatform(platform)
		switch r.URL.Path {
		case "/known.json":
			_ = json.NewEncoder(w).Encode(cftKnownGood{Versions: []cftVersion{{
				Version: version, Downloads: cftDownloads{Chrome: []cftDownload{{Platform: cftPlatform, URL: server.URL + "/chrome.zip"}}},
			}}})
		case "/chrome.zip":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	source := runtimeDownloadSource{
		knownGoodURL: server.URL + "/known.json", stableURL: server.URL + "/stable.json",
		allowedHost: map[string]bool{parsed.Hostname(): true}, client: server.Client(),
	}
	previousProbe := probeRuntimeVersion
	probeRuntimeVersion = func(string) (string, error) { return "Google Chrome for Testing " + version, nil }
	t.Cleanup(func() { probeRuntimeVersion = previousProbe })
	result, err := installRuntime(context.Background(), RuntimeInstallOptions{Kind: RuntimeChromeForTesting, Version: version}, source)
	if err != nil {
		t.Fatal(err)
	}
	if result.Runtime.Kind != RuntimeChromeForTesting || result.Runtime.Version != version {
		t.Fatalf("result = %+v", result)
	}
	server.Close()
	second, err := installRuntime(context.Background(), RuntimeInstallOptions{Kind: RuntimeChromeForTesting, Version: version}, source)
	if err != nil {
		t.Fatalf("verified exact-version retry required metadata: %v", err)
	}
	if second.Downloaded || !second.Selected {
		t.Fatalf("offline exact-version retry = %+v", second)
	}
}

func TestExtractRuntimeZipRejectsTraversalAndSymlink(t *testing.T) {
	for name, archive := range map[string][]byte{
		"traversal":              runtimeZipFixture(t, "../escape", []byte("bad")),
		"slash-rooted path":      runtimeZipFixture(t, "/absolute/chrome", []byte("bad")),
		"backslash-rooted path":  runtimeZipFixture(t, `\absolute\chrome`, []byte("bad")),
		"Windows drive path":     runtimeZipFixture(t, "C:/absolute/chrome", []byte("bad")),
		"escaping symlink":       runtimeSymlinkZipFixture(t, "chrome-link", "../../escape"),
		"Windows drive symlink":  runtimeSymlinkZipFixture(t, "chrome-link", "C:/absolute/chrome"),
		"backslash root symlink": runtimeSymlinkZipFixture(t, "chrome-link", `\absolute\chrome`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.zip")
			if err := os.WriteFile(path, archive, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := extractRuntimeZip(path, filepath.Join(t.TempDir(), "out")); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestRuntimeArchivePartialDownloadAndInterruptedExtractionFailClosed(t *testing.T) {
	body := runtimeZipFixture(t, "chrome/chrome", []byte("partial"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)+17))
		_, _ = w.Write(body)
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	source := runtimeDownloadSource{allowedHost: map[string]bool{parsed.Hostname(): true}, client: server.Client()}
	destination := filepath.Join(t.TempDir(), "runtime.zip")
	if _, err := downloadRuntimeArchive(context.Background(), source, server.URL, destination); err == nil {
		t.Fatal("partial runtime download accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("partial runtime archive was retained: %v", err)
	}

	truncated := filepath.Join(t.TempDir(), "truncated.zip")
	if err := os.WriteFile(truncated, body[:len(body)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractRuntimeZip(truncated, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("interrupted runtime archive accepted")
	}
}

func TestCollectLiveManagedRuntimeReferencesFailsClosedAndSorts(t *testing.T) {
	sessions := []Session{
		{Name: "z-live", PID: os.Getpid(), RuntimeManaged: true, RuntimeKind: RuntimeChromeForTesting, RuntimeVersion: "151.0.0.0"},
		{Name: "a-live", PID: os.Getpid(), RuntimeManaged: true, RuntimeKind: RuntimeChromeForTesting, RuntimeVersion: "151.0.0.0"},
		{Name: "other-version", PID: os.Getpid(), RuntimeManaged: true, RuntimeKind: RuntimeChromeForTesting, RuntimeVersion: "150.0.0.0"},
	}
	live, err := collectLiveManagedRuntimeReferences(sessions, RuntimeChromeForTesting, "151.0.0.0", func(*Session) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(live, ",") != "a-live,z-live" {
		t.Fatalf("live references = %v", live)
	}
	_, err = collectLiveManagedRuntimeReferences(sessions, RuntimeChromeForTesting, "151.0.0.0", func(*Session) (bool, error) {
		return false, errors.New("ownership unavailable")
	})
	if err == nil || !strings.Contains(err.Error(), "ownership unavailable") {
		t.Fatalf("ownership failure was not fail-closed: %v", err)
	}
}

func TestRuntimeReplacementRefusesLiveReferences(t *testing.T) {
	if err := ensureRuntimeReplaceable(RuntimeChromeForTesting, "151.0.0.0", nil); err != nil {
		t.Fatalf("unreferenced runtime rejected: %v", err)
	}
	err := ensureRuntimeReplaceable(RuntimeChromeForTesting, "151.0.0.0", []string{"browser-a", "browser-b"})
	if err == nil || !strings.Contains(err.Error(), "browser-a, browser-b") {
		t.Fatalf("live runtime replacement error = %v", err)
	}
}

func TestExtractRuntimeZipAllowsContainedRelativeSymlink(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	dir, _ := writer.Create("bundle/Versions/1/Resources/")
	_ = dir
	file, _ := writer.Create("bundle/Versions/1/Resources/data")
	_, _ = file.Write([]byte("ok"))
	header := &zip.FileHeader{Name: "bundle/Resources", Method: zip.Store}
	header.SetMode(os.ModeSymlink | 0o777)
	link, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = link.Write([]byte("Versions/1/Resources"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "runtime.zip")
	if err := os.WriteFile(archive, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	if err := extractRuntimeZip(archive, out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(out, "bundle", "Resources"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("contained symlink missing: info=%v err=%v", info, err)
	}
}

func TestRuntimeDownloadRejectsUntrustedAndOversizedMetadata(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	source := runtimeDownloadSource{allowedHost: map[string]bool{"example.com": true}, client: client}
	if _, err := fetchRuntimeBytes(context.Background(), source, "http://example.com/runtime.json", 10); err == nil {
		t.Fatal("insecure URL accepted")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("01234567890"))
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	source = runtimeDownloadSource{allowedHost: map[string]bool{parsed.Hostname(): true}, client: server.Client()}
	if _, err := fetchRuntimeBytes(context.Background(), source, server.URL, 10); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestPruneRuntimesIsDryRunAndPreservesSelectedAndLiveReferences(t *testing.T) {
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	platform, err := runtimePlatform()
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{"132.0.0.1", "132.0.0.2", "132.0.0.3", "132.0.0.4"}
	resolved := map[string]ResolvedRuntime{}
	for _, version := range versions {
		resolved[version] = writeManagedRuntimeFixture(t, RuntimeChromeForTesting, platform, version)
	}
	if err := saveRuntimeSelections(RuntimeSelections{Selected: map[RuntimeKind]RuntimeSelection{
		RuntimeChromeForTesting: {Platform: platform, Version: versions[3]},
	}}); err != nil {
		t.Fatal(err)
	}
	profile, err := ProfileDirFor("runtime-prune-live")
	if err != nil {
		t.Fatal(err)
	}
	liveRuntime := resolved[versions[0]]
	if err := SaveSession(&Session{
		Name: "runtime-prune-live", PID: os.Getpid(), ProfileDir: profile,
		ChromePath: liveRuntime.Path, RuntimeKind: liveRuntime.Kind, RuntimeVersion: liveRuntime.Version,
		RuntimeManaged: true, RuntimeManifest: liveRuntime.ManifestDigest, RuntimeCaps: liveRuntime.Capabilities,
	}); err != nil {
		t.Fatal(err)
	}

	dryRun, err := PruneRuntimes(RuntimePruneOptions{Kind: RuntimeChromeForTesting, Keep: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || len(dryRun.Entries) != 2 {
		t.Fatalf("dry-run report = %+v", dryRun)
	}
	for _, entry := range dryRun.Entries {
		if entry.Removed {
			t.Fatalf("dry-run removed %+v", entry)
		}
		if _, err := os.Stat(entry.Path); err != nil {
			t.Fatalf("dry-run mutated %s: %v", entry.Path, err)
		}
	}

	applied, err := PruneRuntimes(RuntimePruneOptions{Kind: RuntimeChromeForTesting, Keep: 1, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if applied.DryRun || len(applied.Entries) != 2 {
		t.Fatalf("apply report = %+v", applied)
	}
	for _, entry := range applied.Entries {
		if !entry.Removed {
			t.Fatalf("apply did not remove %+v", entry)
		}
		if _, err := os.Stat(entry.Path); !os.IsNotExist(err) {
			t.Fatalf("pruned path still exists %s: %v", entry.Path, err)
		}
	}
	for _, version := range []string{versions[0], versions[3]} {
		dir, _ := runtimeInstallDir(RuntimeChromeForTesting, platform, version)
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("preserved runtime %s missing: %v", version, err)
		}
	}
}

func TestPromoteRuntimeDirectoryRestoresPriorRuntimeOnFailure(t *testing.T) {
	root := t.TempDir()
	final := filepath.Join(root, "final")
	if err := os.MkdirAll(final, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(final, "known-good"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := promoteRuntimeDirectory(filepath.Join(root, "missing-stage"), final); err == nil {
		t.Fatal("promotion unexpectedly succeeded")
	}
	if body, err := os.ReadFile(filepath.Join(final, "known-good")); err != nil || string(body) != "ok" {
		t.Fatalf("prior runtime was not restored: body=%q err=%v", body, err)
	}
}

func writeManagedRuntimeFixture(t *testing.T, kind RuntimeKind, platform, version string) ResolvedRuntime {
	t.Helper()
	dir, err := runtimeInstallDir(kind, platform, version)
	if err != nil {
		t.Fatal(err)
	}
	binaryName := "chrome"
	versionOutput := "Google Chrome for Testing " + version
	if kind == RuntimeHeadlessShell {
		binaryName = "chrome-headless-shell"
		versionOutput = "HeadlessChrome/" + version
	}
	rel := filepath.Join("browser", binaryName)
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture-"+version), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := managedRuntimeCapabilities(kind)
	if err != nil {
		t.Fatal(err)
	}
	manifest := RuntimeManifest{
		SchemaVersion: runtimeSchemaVersion, RuntimeKind: kind, Version: version,
		RequestedChannel: "stable", Platform: platform,
		SourceURL:   "https://storage.googleapis.com/chrome-for-testing-public/fixture.zip",
		InstalledAt: time.Now().UTC(), ArchiveSHA256: strings.Repeat("a", 64), BinarySHA256: digest,
		BinaryRelPath: rel, VersionOutput: versionOutput, Capabilities: caps,
	}
	if err := writeJSONAtomic(filepath.Join(dir, "runtime.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, _, err := verifyInstalledRuntime(kind, platform, version)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func runtimeZipFixture(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(0o700)
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func runtimeSymlinkZipFixture(t *testing.T, name, target string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(os.ModeSymlink | 0o777)
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte(target))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
