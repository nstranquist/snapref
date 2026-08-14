package browser

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultRuntimeKnownGoodURL = "https://googlechromelabs.github.io/chrome-for-testing/known-good-versions-with-downloads.json"
	defaultRuntimeStableURL    = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"
	maxRuntimeMetadataBytes    = 16 << 20
	maxRuntimeArchiveBytes     = 768 << 20
	maxRuntimeExtractedBytes   = 2 << 30
	maxRuntimeArchiveFiles     = 30000
)

type RuntimeInstallOptions struct {
	Kind    RuntimeKind
	Channel string
	Version string
}

type RuntimeInstallResult struct {
	Runtime    RuntimeRecord `json:"runtime"`
	Downloaded bool          `json:"downloaded"`
	Selected   bool          `json:"selected"`
}

type RuntimePruneOptions struct {
	Kind  RuntimeKind
	Keep  int
	Apply bool
}

type RuntimePruneEntry struct {
	Kind     RuntimeKind `json:"runtime_kind"`
	Version  string      `json:"version"`
	Platform string      `json:"platform"`
	Path     string      `json:"path"`
	Removed  bool        `json:"removed"`
	Reason   string      `json:"reason"`
	Error    string      `json:"error,omitempty"`
}

type RuntimePruneReport struct {
	DryRun  bool                `json:"dry_run"`
	Keep    int                 `json:"keep"`
	Entries []RuntimePruneEntry `json:"entries"`
}

type runtimeDownloadSource struct {
	knownGoodURL string
	stableURL    string
	allowedHost  map[string]bool
	client       *http.Client
}

func defaultRuntimeDownloadSource() runtimeDownloadSource {
	allowed := map[string]bool{
		"googlechromelabs.github.io": true,
		"storage.googleapis.com":     true,
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many runtime download redirects")
		}
		if req.URL.Scheme != "https" || !allowed[strings.ToLower(req.URL.Hostname())] {
			return fmt.Errorf("runtime download redirect to untrusted URL %s", req.URL.Redacted())
		}
		return nil
	}
	return runtimeDownloadSource{knownGoodURL: defaultRuntimeKnownGoodURL, stableURL: defaultRuntimeStableURL, allowedHost: allowed, client: client}
}

type cftDownloads struct {
	Chrome        []cftDownload `json:"chrome"`
	HeadlessShell []cftDownload `json:"chrome-headless-shell"`
}

type cftDownload struct {
	Platform string `json:"platform"`
	URL      string `json:"url"`
}

type cftVersion struct {
	Version   string       `json:"version"`
	Downloads cftDownloads `json:"downloads"`
}

type cftKnownGood struct {
	Versions []cftVersion `json:"versions"`
}

type cftLastKnownGood struct {
	Channels map[string]cftVersion `json:"channels"`
}

func InstallRuntime(ctx context.Context, opts RuntimeInstallOptions) (RuntimeInstallResult, error) {
	return installRuntime(ctx, opts, defaultRuntimeDownloadSource())
}

func installRuntime(ctx context.Context, opts RuntimeInstallOptions, source runtimeDownloadSource) (RuntimeInstallResult, error) {
	if opts.Kind != RuntimeHeadlessShell && opts.Kind != RuntimeChromeForTesting {
		return RuntimeInstallResult{}, fmt.Errorf("runtime install requires headless-shell or chrome-for-testing, got %q", opts.Kind)
	}
	if (opts.Channel == "") == (opts.Version == "") {
		return RuntimeInstallResult{}, errors.New("runtime install requires exactly one of channel or version")
	}
	if opts.Channel != "" && opts.Channel != "stable" {
		return RuntimeInstallResult{}, fmt.Errorf("unsupported runtime channel %q (want stable)", opts.Channel)
	}
	platform, err := runtimePlatform()
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	cftPlatform, err := chromeForTestingPlatform(platform)
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	unlock, err := acquireRuntimeKindLocks([]RuntimeKind{opts.Kind}, 30*time.Second)
	if err != nil {
		return RuntimeInstallResult{}, fmt.Errorf("lock runtime install: %w", err)
	}
	defer unlock()

	// Exact-version retries are a true local no-op when the immutable install
	// already verifies. Do not make that recovery path depend on metadata uptime.
	if opts.Version != "" {
		if resolved, manifest, verifyErr := verifyInstalledRuntime(opts.Kind, platform, opts.Version); verifyErr == nil {
			if err := selectInstalledRuntime(opts.Kind, platform, opts.Version); err != nil {
				return RuntimeInstallResult{}, err
			}
			return RuntimeInstallResult{Runtime: runtimeRecordFromVerified(resolved, manifest, platform, true), Selected: true}, nil
		}
	}

	version, downloadURL, err := resolveRuntimeDownload(ctx, source, opts, cftPlatform)
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	finalDir, err := runtimeInstallDir(opts.Kind, platform, version)
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	if resolved, manifest, verifyErr := verifyInstalledRuntime(opts.Kind, platform, version); verifyErr == nil {
		if err := selectInstalledRuntime(opts.Kind, platform, version); err != nil {
			return RuntimeInstallResult{}, err
		}
		return RuntimeInstallResult{Runtime: runtimeRecordFromVerified(resolved, manifest, platform, true), Selected: true}, nil
	}

	root, err := runtimesDir()
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	stagingRoot := filepath.Join(root, ".staging")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return RuntimeInstallResult{}, err
	}
	stageDir, err := os.MkdirTemp(stagingRoot, "install-"+string(opts.Kind)+"-*")
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	defer os.RemoveAll(stageDir)
	archivePath := filepath.Join(stageDir, "runtime.zip")
	archiveDigest, err := downloadRuntimeArchive(ctx, source, downloadURL, archivePath)
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	extractedDir := filepath.Join(stageDir, "extracted")
	if err := extractRuntimeZip(archivePath, extractedDir); err != nil {
		return RuntimeInstallResult{}, err
	}
	binarySource, err := findRuntimeBinary(extractedDir, opts.Kind)
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	if err := os.Chmod(binarySource, 0o700); err != nil {
		return RuntimeInstallResult{}, fmt.Errorf("mark runtime executable: %w", err)
	}
	versionOutput, err := probeRuntimeVersion(binarySource)
	if err != nil {
		return RuntimeInstallResult{}, fmt.Errorf("probe downloaded runtime version: %w", err)
	}
	if !strings.Contains(versionOutput, version) {
		return RuntimeInstallResult{}, fmt.Errorf("downloaded runtime version output %q does not contain expected %s", versionOutput, version)
	}
	binaryDigest, err := fileSHA256(binarySource)
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	promoteDir := filepath.Join(stageDir, "promote")
	browserDir := filepath.Join(promoteDir, "browser")
	if err := os.MkdirAll(promoteDir, 0o700); err != nil {
		return RuntimeInstallResult{}, err
	}
	if err := os.Rename(extractedDir, browserDir); err != nil {
		return RuntimeInstallResult{}, fmt.Errorf("stage extracted runtime: %w", err)
	}
	binaryRelFromExtract, err := filepath.Rel(filepath.Join(stageDir, "extracted"), binarySource)
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	// binarySource moved with extractedDir; retain its path relative to the
	// archive root under the canonical browser/ directory.
	binaryRel := filepath.Join("browser", binaryRelFromExtract)
	capabilities, _ := managedRuntimeCapabilities(opts.Kind)
	manifest := RuntimeManifest{
		SchemaVersion: runtimeSchemaVersion, RuntimeKind: opts.Kind, Version: version,
		RequestedChannel: opts.Channel, Platform: platform, SourceURL: downloadURL,
		InstalledAt: time.Now().UTC(), ArchiveSHA256: archiveDigest, BinarySHA256: binaryDigest,
		BinaryRelPath: binaryRel, VersionOutput: versionOutput, Capabilities: capabilities,
	}
	if err := writeJSONAtomic(filepath.Join(promoteDir, "runtime.json"), manifest, 0o600); err != nil {
		return RuntimeInstallResult{}, err
	}
	liveSessions, err := liveManagedRuntimeReferences(opts.Kind, version)
	if err != nil {
		return RuntimeInstallResult{}, err
	}
	if err := ensureRuntimeReplaceable(opts.Kind, version, liveSessions); err != nil {
		return RuntimeInstallResult{}, err
	}
	if err := promoteRuntimeDirectory(promoteDir, finalDir); err != nil {
		return RuntimeInstallResult{}, err
	}
	resolved, verifiedManifest, err := verifyInstalledRuntime(opts.Kind, platform, version)
	if err != nil {
		return RuntimeInstallResult{}, fmt.Errorf("verify promoted runtime: %w", err)
	}
	if err := selectInstalledRuntime(opts.Kind, platform, version); err != nil {
		return RuntimeInstallResult{}, err
	}
	return RuntimeInstallResult{
		Runtime: runtimeRecordFromVerified(resolved, verifiedManifest, platform, true), Downloaded: true, Selected: true,
	}, nil
}

func ensureRuntimeReplaceable(kind RuntimeKind, version string, liveSessions []string) error {
	if len(liveSessions) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to replace %s %s while live session(s) reference it: %s", kind, version, strings.Join(liveSessions, ", "))
}

func liveManagedRuntimeReferences(kind RuntimeKind, version string) ([]string, error) {
	sessions, err := ListSessions()
	if err != nil {
		return nil, fmt.Errorf("list sessions before runtime replacement: %w", err)
	}
	return collectLiveManagedRuntimeReferences(sessions, kind, version, func(session *Session) (bool, error) {
		return session.ownsLaunchedProcess()
	})
}

func collectLiveManagedRuntimeReferences(sessions []Session, kind RuntimeKind, version string, owns func(*Session) (bool, error)) ([]string, error) {
	var live []string
	for i := range sessions {
		session := &sessions[i]
		if !session.RuntimeManaged || session.RuntimeKind != kind || session.RuntimeVersion != version || !session.IsAlive() {
			continue
		}
		owned, err := owns(session)
		if err != nil {
			return nil, fmt.Errorf("verify live session %q before runtime replacement: %w", session.Name, err)
		}
		if owned {
			live = append(live, session.Name)
		}
	}
	sort.Strings(live)
	return live, nil
}

func resolveRuntimeDownload(ctx context.Context, source runtimeDownloadSource, opts RuntimeInstallOptions, platform string) (string, string, error) {
	metadataURL := source.knownGoodURL
	if opts.Channel != "" {
		metadataURL = source.stableURL
	}
	body, err := fetchRuntimeBytes(ctx, source, metadataURL, maxRuntimeMetadataBytes)
	if err != nil {
		return "", "", fmt.Errorf("fetch Chrome for Testing metadata: %w", err)
	}
	var selected *cftVersion
	if opts.Version != "" {
		var metadata cftKnownGood
		if err := json.Unmarshal(body, &metadata); err != nil {
			return "", "", fmt.Errorf("parse Chrome for Testing metadata: %w", err)
		}
		for i := range metadata.Versions {
			if metadata.Versions[i].Version == opts.Version {
				selected = &metadata.Versions[i]
				break
			}
		}
	} else {
		var metadata cftLastKnownGood
		if err := json.Unmarshal(body, &metadata); err != nil {
			return "", "", fmt.Errorf("parse Chrome for Testing channel metadata: %w", err)
		}
		stable, ok := metadata.Channels["Stable"]
		if !ok {
			return "", "", errors.New("Chrome for Testing metadata has no Stable channel")
		}
		selected = &stable
	}
	if selected == nil || !chromeVersionRE.MatchString(selected.Version) {
		return "", "", fmt.Errorf("Chrome for Testing metadata has no matching version for %s", opts.Version)
	}
	downloadURL := findCFTDownload(selected.Downloads, opts.Kind, platform)
	if downloadURL == "" {
		return "", "", fmt.Errorf("Chrome for Testing %s has no %s download for %s", selected.Version, opts.Kind, platform)
	}
	if err := validateRuntimeURL(downloadURL, source.allowedHost); err != nil {
		return "", "", err
	}
	return selected.Version, downloadURL, nil
}

func findCFTDownload(downloads cftDownloads, kind RuntimeKind, platform string) string {
	list := downloads.Chrome
	if kind == RuntimeHeadlessShell {
		list = downloads.HeadlessShell
	}
	for _, item := range list {
		if item.Platform == platform {
			return item.URL
		}
	}
	return ""
}

func chromeForTestingPlatform(platform string) (string, error) {
	switch platform {
	case "darwin-arm64":
		return "mac-arm64", nil
	case "darwin-amd64":
		return "mac-x64", nil
	case "linux-amd64":
		return "linux64", nil
	case "windows-amd64":
		return "win64", nil
	default:
		return "", fmt.Errorf("Chrome for Testing does not publish a supported artifact for %s", platform)
	}
}

func fetchRuntimeBytes(ctx context.Context, source runtimeDownloadSource, rawURL string, limit int64) ([]byte, error) {
	if err := validateRuntimeURL(rawURL, source.allowedHost); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := source.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %s", req.URL.Redacted(), resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s exceeded %d-byte limit", req.URL.Redacted(), limit)
	}
	return body, nil
}

func validateRuntimeURL(rawURL string, allowed map[string]bool) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || !allowed[strings.ToLower(parsed.Hostname())] || parsed.User != nil {
		return fmt.Errorf("untrusted runtime download URL %s", parsed.Redacted())
	}
	return nil
}

func downloadRuntimeArchive(ctx context.Context, source runtimeDownloadSource, rawURL, destination string) (string, error) {
	if err := validateRuntimeURL(rawURL, source.allowedHost); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := source.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download runtime archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %s", req.URL.Redacted(), resp.Status)
	}
	if resp.ContentLength > maxRuntimeArchiveBytes {
		return "", fmt.Errorf("runtime archive exceeds %d-byte limit", maxRuntimeArchiveBytes)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, maxRuntimeArchiveBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return "", closeErr
	}
	if written > maxRuntimeArchiveBytes {
		_ = os.Remove(destination)
		return "", fmt.Errorf("runtime archive exceeds %d-byte limit", maxRuntimeArchiveBytes)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractRuntimeZip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open runtime archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxRuntimeArchiveFiles {
		return fmt.Errorf("runtime archive has unsafe file count %d", len(reader.File))
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	var extracted int64
	for _, entry := range reader.File {
		name, pathErr := portableArchiveRelativePath(entry.Name)
		if pathErr != nil || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("runtime archive contains unsafe path %q", entry.Name)
		}
		target := filepath.Join(destination, name)
		if rel, relErr := filepath.Rel(destination, target); relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("runtime archive path escapes destination: %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			if entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > 4096 {
				return fmt.Errorf("runtime archive symlink %q has unsafe target size", entry.Name)
			}
			source, err := entry.Open()
			if err != nil {
				return err
			}
			body, readErr := io.ReadAll(io.LimitReader(source, 4097))
			closeErr := source.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			rawLinkTarget := string(body)
			linkTarget, targetErr := portableArchiveRelativePath(rawLinkTarget)
			if targetErr != nil || linkTarget == "" {
				return fmt.Errorf("runtime archive symlink %q has unsafe target %q", entry.Name, rawLinkTarget)
			}
			resolvedTarget := filepath.Clean(filepath.Join(filepath.Dir(target), linkTarget))
			rel, relErr := filepath.Rel(destination, resolvedTarget)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("runtime archive symlink %q escapes destination", entry.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.Symlink(linkTarget, target); err != nil {
				return fmt.Errorf("create runtime archive symlink %q: %w", entry.Name, err)
			}
			continue
		}
		if entry.UncompressedSize64 > maxRuntimeExtractedBytes || extracted+int64(entry.UncompressedSize64) > maxRuntimeExtractedBytes {
			return errors.New("runtime archive exceeds extracted-size limit")
		}
		extracted += int64(entry.UncompressedSize64)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		mode := entry.Mode().Perm()
		if mode == 0 {
			mode = 0o600
		}
		destinationFile, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			source.Close()
			return err
		}
		written, copyErr := io.Copy(destinationFile, io.LimitReader(source, int64(entry.UncompressedSize64)+1))
		closeErr := destinationFile.Close()
		sourceErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if written != int64(entry.UncompressedSize64) {
			return fmt.Errorf("runtime archive entry %q size mismatch", entry.Name)
		}
		if closeErr != nil {
			return closeErr
		}
		if sourceErr != nil {
			return sourceErr
		}
	}
	return nil
}

// portableArchiveRelativePath rejects absolute and volume-qualified paths using
// platform-independent rules. filepath.IsAbs alone is insufficient here: for
// example, "/absolute" is not absolute under Windows filepath semantics, while
// "C:/absolute" is not absolute when this validation runs on Unix.
func portableArchiveRelativePath(raw string) (string, error) {
	if raw == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", errors.New("empty or NUL-containing archive path")
	}
	normalized := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(normalized, "/") || hasWindowsDrivePrefix(normalized) {
		return "", errors.New("absolute or volume-qualified archive path")
	}
	return filepath.FromSlash(path.Clean(normalized)), nil
}

func hasWindowsDrivePrefix(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	letter := value[0]
	return letter >= 'A' && letter <= 'Z' || letter >= 'a' && letter <= 'z'
}

func findRuntimeBinary(root string, kind RuntimeKind) (string, error) {
	wanted := map[string]bool{}
	if kind == RuntimeHeadlessShell {
		wanted["chrome-headless-shell"] = true
		wanted["chrome-headless-shell.exe"] = true
	} else {
		wanted["chrome"] = true
		wanted["chrome.exe"] = true
		wanted["Google Chrome for Testing"] = true
	}
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && wanted[entry.Name()] {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("runtime archive contains %d expected executables, want exactly one", len(matches))
	}
	return matches[0], nil
}

func promoteRuntimeDirectory(staged, final string) error {
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(final); errors.Is(err, os.ErrNotExist) {
		return os.Rename(staged, final)
	} else if err != nil {
		return err
	}
	backup := final + ".replaced-" + fmt.Sprintf("%d", time.Now().UnixNano())
	if err := os.Rename(final, backup); err != nil {
		return fmt.Errorf("stage prior runtime for replacement: %w", err)
	}
	if err := os.Rename(staged, final); err != nil {
		_ = os.Rename(backup, final)
		return fmt.Errorf("promote runtime: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove replaced runtime backup: %w", err)
	}
	return nil
}

func selectInstalledRuntime(kind RuntimeKind, platform, version string) error {
	selections, err := loadRuntimeSelections()
	if err != nil {
		return err
	}
	if selections.Selected == nil {
		selections.Selected = map[RuntimeKind]RuntimeSelection{}
	}
	selections.Selected[kind] = RuntimeSelection{Platform: platform, Version: version}
	return saveRuntimeSelections(selections)
}

func runtimeRecordFromVerified(resolved ResolvedRuntime, manifest RuntimeManifest, platform string, selected bool) RuntimeRecord {
	return RuntimeRecord{
		Kind: resolved.Kind, Version: resolved.Version, Platform: platform, Selected: selected,
		Managed: true, Path: resolved.Path, InstalledAt: manifest.InstalledAt,
		ArchiveSHA256: manifest.ArchiveSHA256, BinarySHA256: manifest.BinarySHA256,
		ManifestDigest: resolved.ManifestDigest, Capabilities: resolved.Capabilities, Verified: true,
	}
}

func VerifyRuntimes(kind RuntimeKind) ([]RuntimeRecord, error) {
	records, err := ListRuntimeRecords()
	if err != nil {
		return nil, err
	}
	var filtered []RuntimeRecord
	for _, record := range records {
		if kind == "" || kind == RuntimeAuto || record.Kind == kind {
			if record.Verified {
				versionOutput, probeErr := probeRuntimeVersion(record.Path)
				if probeErr != nil {
					record.Verified = false
					record.Error = fmt.Sprintf("version probe: %v", probeErr)
				} else if !strings.Contains(versionOutput, record.Version) {
					record.Verified = false
					record.Error = fmt.Sprintf("version probe output %q does not contain expected %s", versionOutput, record.Version)
				}
			}
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

func PruneRuntimes(opts RuntimePruneOptions) (RuntimePruneReport, error) {
	if opts.Keep <= 0 {
		opts.Keep = 2
	}
	if opts.Kind != "" && opts.Kind != RuntimeAuto && opts.Kind != RuntimeHeadlessShell && opts.Kind != RuntimeChromeForTesting {
		return RuntimePruneReport{}, fmt.Errorf("runtime prune does not support %q", opts.Kind)
	}
	kinds := []RuntimeKind{RuntimeChromeForTesting, RuntimeHeadlessShell}
	if opts.Kind == RuntimeChromeForTesting || opts.Kind == RuntimeHeadlessShell {
		kinds = []RuntimeKind{opts.Kind}
	}
	unlock, err := acquireRuntimeKindLocks(kinds, 30*time.Second)
	if err != nil {
		return RuntimePruneReport{}, fmt.Errorf("lock runtime prune: %w", err)
	}
	defer unlock()
	records, err := ListRuntimeRecords()
	if err != nil {
		return RuntimePruneReport{}, err
	}
	selections, err := loadRuntimeSelections()
	if err != nil {
		return RuntimePruneReport{}, err
	}
	live := map[string]bool{}
	sessions, err := ListSessions()
	if err != nil {
		return RuntimePruneReport{}, err
	}
	platform, _ := runtimePlatform()
	for _, session := range sessions {
		if session.IsAlive() && session.RuntimeManaged {
			live[string(session.RuntimeKind)+"\x00"+platform+"\x00"+session.RuntimeVersion] = true
		}
	}
	grouped := map[RuntimeKind][]RuntimeRecord{}
	for _, record := range records {
		if opts.Kind == "" || opts.Kind == RuntimeAuto || record.Kind == opts.Kind {
			grouped[record.Kind] = append(grouped[record.Kind], record)
		}
	}
	report := RuntimePruneReport{DryRun: !opts.Apply, Keep: opts.Keep, Entries: []RuntimePruneEntry{}}
	for _, kind := range []RuntimeKind{RuntimeChromeForTesting, RuntimeHeadlessShell} {
		items := grouped[kind]
		sort.Slice(items, func(i, j int) bool { return compareChromeVersions(items[i].Version, items[j].Version) > 0 })
		for index, record := range items {
			key := string(record.Kind) + "\x00" + record.Platform + "\x00" + record.Version
			selected := selections.Selected[record.Kind]
			if index < opts.Keep || (selected.Platform == record.Platform && selected.Version == record.Version) || live[key] {
				continue
			}
			dir, dirErr := runtimeInstallDir(record.Kind, record.Platform, record.Version)
			if dirErr != nil {
				return report, dirErr
			}
			entry := RuntimePruneEntry{Kind: record.Kind, Version: record.Version, Platform: record.Platform, Path: dir, Reason: "unselected-retention-excess"}
			if opts.Apply {
				if err := os.RemoveAll(dir); err != nil {
					entry.Error = err.Error()
				} else {
					entry.Removed = true
				}
			}
			report.Entries = append(report.Entries, entry)
		}
	}
	return report, nil
}

func compareChromeVersions(a, b string) int {
	parse := func(value string) []int {
		var out []int
		for _, part := range strings.Split(value, ".") {
			var n int
			_, _ = fmt.Sscanf(part, "%d", &n)
			out = append(out, n)
		}
		return out
	}
	aa, bb := parse(a), parse(b)
	for i := 0; i < len(aa) && i < len(bb); i++ {
		if aa[i] < bb[i] {
			return -1
		}
		if aa[i] > bb[i] {
			return 1
		}
	}
	return 0
}
