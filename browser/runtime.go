package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// RuntimeKind identifies the executable class beneath PageSkein's CDP layer.
// RuntimeAuto is request-only; successful resolutions always return a concrete
// managed or explicitly external kind.
type RuntimeKind string

const (
	RuntimeAuto             RuntimeKind = "auto"
	RuntimeHeadlessShell    RuntimeKind = "headless-shell"
	RuntimeChromeForTesting RuntimeKind = "chrome-for-testing"
	RuntimeExternal         RuntimeKind = "external"
)

const runtimeSchemaVersion = 1

var chromeVersionRE = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){3}$`)

var probeRuntimeVersion = chromeVersion

// RuntimeCapabilities is the closed capability contract used before process
// creation. Values for managed runtimes are code-owned rather than trusted
// from downloaded metadata.
type RuntimeCapabilities struct {
	Headless    bool `json:"headless"`
	Headful     bool `json:"headful"`
	Extensions  bool `json:"extensions"`
	Screenshots bool `json:"screenshots"`
}

// RuntimeRequest describes the capabilities required by one launch.
type RuntimeRequest struct {
	Kind          RuntimeKind
	Headful       bool
	ExtensionPath string
	ExternalPath  string
}

// ResolvedRuntime is immutable launch authority for one browser process.
type ResolvedRuntime struct {
	Kind           RuntimeKind         `json:"runtime_kind"`
	Version        string              `json:"version"`
	Managed        bool                `json:"managed"`
	Path           string              `json:"path"`
	Source         string              `json:"source"`
	ManifestDigest string              `json:"manifest_digest,omitempty"`
	Capabilities   RuntimeCapabilities `json:"capabilities"`
}

// RuntimeManifest records the exact locally installed artifact. SHA-256 values
// are observations of received/local bytes, not publisher signatures.
type RuntimeManifest struct {
	SchemaVersion    int                 `json:"schema_version"`
	RuntimeKind      RuntimeKind         `json:"runtime_kind"`
	Version          string              `json:"version"`
	RequestedChannel string              `json:"requested_channel,omitempty"`
	Platform         string              `json:"platform"`
	SourceURL        string              `json:"source_url"`
	InstalledAt      time.Time           `json:"installed_at"`
	ArchiveSHA256    string              `json:"archive_sha256"`
	BinarySHA256     string              `json:"binary_sha256"`
	BinaryRelPath    string              `json:"binary_relpath"`
	VersionOutput    string              `json:"version_output"`
	Capabilities     RuntimeCapabilities `json:"capabilities"`
}

type RuntimeSelection struct {
	Platform string `json:"platform"`
	Version  string `json:"version"`
}

type RuntimeSelections struct {
	SchemaVersion int                              `json:"schema_version"`
	Selected      map[RuntimeKind]RuntimeSelection `json:"selected"`
}

// RuntimeAcceptance is the evidence gate controlling whether auto may prefer
// Headless Shell over full Chrome for Testing.
type RuntimeAcceptance struct {
	SchemaVersion           int                      `json:"schema_version"`
	PolicyVersion           int                      `json:"policy_version"`
	Platform                string                   `json:"platform"`
	HeadlessShellVersion    string                   `json:"headless_shell_version"`
	ChromeForTestingVersion string                   `json:"chrome_for_testing_version"`
	TestedAt                time.Time                `json:"tested_at"`
	Samples                 int                      `json:"samples"`
	Metrics                 RuntimeAcceptanceMetrics `json:"metrics"`
	FunctionalPass          bool                     `json:"functional_pass"`
	VisualPass              bool                     `json:"visual_pass"`
	// FocusIsolationStatus is the Headless Shell target-runtime gate for
	// ordinary automatic headless routing. ChromeForTestingFocusIsolationStatus
	// is retained as comparator diagnostics: a CFT activation is evidence in
	// favor of Headless Shell, not a reason to fall back to the activating
	// runtime.
	FocusIsolationStatus                 string                   `json:"focus_isolation_status"`
	ChromeForTestingFocusIsolationStatus string                   `json:"chrome_for_testing_focus_isolation_status"`
	ChromeForTestingFocusIsolationReason string                   `json:"chrome_for_testing_focus_isolation_reason,omitempty"`
	ConsumerPass                         bool                     `json:"consumer_pass"`
	ConsumerEvidence                     *RuntimeConsumerEvidence `json:"consumer_evidence,omitempty"`
	Pass                                 bool                     `json:"pass"`
}

type RuntimeAcceptanceMetrics struct {
	HeadlessShellColdP50MS         float64 `json:"headless_shell_cold_p50_ms"`
	ChromeForTestingColdP50MS      float64 `json:"chrome_for_testing_cold_p50_ms"`
	HeadlessShellMedianRSSBytes    int64   `json:"headless_shell_median_rss_bytes"`
	ChromeForTestingMedianRSSBytes int64   `json:"chrome_for_testing_median_rss_bytes"`
}

// RuntimeRecord is the stable list/verify/doctor representation.
type RuntimeRecord struct {
	Kind           RuntimeKind         `json:"runtime_kind"`
	Version        string              `json:"version"`
	Platform       string              `json:"platform"`
	Selected       bool                `json:"selected"`
	Managed        bool                `json:"managed"`
	Path           string              `json:"path,omitempty"`
	InstalledAt    time.Time           `json:"installed_at"`
	ArchiveSHA256  string              `json:"archive_sha256,omitempty"`
	BinarySHA256   string              `json:"binary_sha256,omitempty"`
	ManifestDigest string              `json:"manifest_digest,omitempty"`
	Capabilities   RuntimeCapabilities `json:"capabilities"`
	Verified       bool                `json:"verified"`
	Error          string              `json:"error,omitempty"`
}

func ParseRuntimeKind(raw string) (RuntimeKind, error) {
	kind := RuntimeKind(strings.TrimSpace(raw))
	if kind == "" {
		return RuntimeAuto, nil
	}
	switch kind {
	case RuntimeAuto, RuntimeHeadlessShell, RuntimeChromeForTesting, RuntimeExternal:
		return kind, nil
	default:
		return "", fmt.Errorf("unknown browser runtime %q (want auto, headless-shell, chrome-for-testing, or external)", raw)
	}
}

func managedRuntimeCapabilities(kind RuntimeKind) (RuntimeCapabilities, error) {
	switch kind {
	case RuntimeHeadlessShell:
		return RuntimeCapabilities{Headless: true, Screenshots: true}, nil
	case RuntimeChromeForTesting:
		return RuntimeCapabilities{Headless: true, Headful: true, Extensions: true, Screenshots: true}, nil
	default:
		return RuntimeCapabilities{}, fmt.Errorf("runtime %q is not managed", kind)
	}
}

func runtimePlatform() (string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64", "arm64":
		arch = runtime.GOARCH
	default:
		return "", fmt.Errorf("unsupported browser runtime architecture %s", runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		return runtime.GOOS + "-" + arch, nil
	default:
		return "", fmt.Errorf("unsupported browser runtime platform %s", runtime.GOOS)
	}
}

func runtimesDir() (string, error) {
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "runtimes"), nil
}

func runtimeSelectionsPath() (string, error) {
	root, err := runtimesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "runtime-selections.json"), nil
}

func runtimeAcceptancePath() (string, error) {
	root, err := runtimesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "runtime-acceptance.json"), nil
}

func runtimeInstallDir(kind RuntimeKind, platform, version string) (string, error) {
	if kind != RuntimeHeadlessShell && kind != RuntimeChromeForTesting {
		return "", fmt.Errorf("runtime %q is not installable", kind)
	}
	if !chromeVersionRE.MatchString(version) {
		return "", fmt.Errorf("invalid Chrome runtime version %q", version)
	}
	if platform == "" || strings.ContainsAny(platform, `/\\`) || strings.Contains(platform, "..") {
		return "", fmt.Errorf("invalid runtime platform %q", platform)
	}
	root, err := runtimesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, string(kind), platform, version), nil
}

func runtimeLockKindsForRequest(request RuntimeRequest) []RuntimeKind {
	if request.ExternalPath != "" || request.Kind == RuntimeExternal {
		return nil
	}
	switch request.Kind {
	case RuntimeHeadlessShell:
		return []RuntimeKind{RuntimeHeadlessShell}
	case RuntimeChromeForTesting:
		return []RuntimeKind{RuntimeChromeForTesting}
	case RuntimeAuto, "":
		if request.Headful || request.ExtensionPath != "" {
			return []RuntimeKind{RuntimeChromeForTesting}
		}
		// Auto may resolve either managed runtime. Lock both in the same order as
		// prune so selection/acceptance changes cannot race resolution.
		return []RuntimeKind{RuntimeChromeForTesting, RuntimeHeadlessShell}
	default:
		return nil
	}
}

func acquireRuntimeKindLocks(kinds []RuntimeKind, timeout time.Duration) (func(), error) {
	wanted := map[RuntimeKind]bool{}
	for _, kind := range kinds {
		if kind == RuntimeChromeForTesting || kind == RuntimeHeadlessShell {
			wanted[kind] = true
		}
	}
	ordered := []RuntimeKind{RuntimeChromeForTesting, RuntimeHeadlessShell}
	var unlocks []func()
	for _, kind := range ordered {
		if !wanted[kind] {
			continue
		}
		unlock, err := acquireSessionLifecycleLock("runtime_"+string(kind), timeout)
		if err != nil {
			for i := len(unlocks) - 1; i >= 0; i-- {
				unlocks[i]()
			}
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}, nil
}

func loadRuntimeSelections() (RuntimeSelections, error) {
	path, err := runtimeSelectionsPath()
	if err != nil {
		return RuntimeSelections{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimeSelections{SchemaVersion: runtimeSchemaVersion, Selected: map[RuntimeKind]RuntimeSelection{}}, nil
		}
		return RuntimeSelections{}, err
	}
	var selections RuntimeSelections
	if err := json.Unmarshal(body, &selections); err != nil {
		return RuntimeSelections{}, fmt.Errorf("parse runtime selections: %w", err)
	}
	if selections.SchemaVersion != runtimeSchemaVersion {
		return RuntimeSelections{}, fmt.Errorf("unsupported runtime selections schema %d", selections.SchemaVersion)
	}
	if selections.Selected == nil {
		selections.Selected = map[RuntimeKind]RuntimeSelection{}
	}
	for kind, selection := range selections.Selected {
		if kind != RuntimeHeadlessShell && kind != RuntimeChromeForTesting {
			return RuntimeSelections{}, fmt.Errorf("runtime selections contain unknown managed kind %q", kind)
		}
		if !chromeVersionRE.MatchString(selection.Version) || selection.Platform == "" {
			return RuntimeSelections{}, fmt.Errorf("runtime selections contain invalid %s selection", kind)
		}
	}
	return selections, nil
}

func saveRuntimeSelections(selections RuntimeSelections) error {
	selections.SchemaVersion = runtimeSchemaVersion
	if selections.Selected == nil {
		selections.Selected = map[RuntimeKind]RuntimeSelection{}
	}
	path, err := runtimeSelectionsPath()
	if err != nil {
		return err
	}
	return writeJSONAtomic(path, selections, 0o600)
}

func loadRuntimeAcceptance() (RuntimeAcceptance, error) {
	path, err := runtimeAcceptancePath()
	if err != nil {
		return RuntimeAcceptance{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return RuntimeAcceptance{}, err
	}
	var acceptance RuntimeAcceptance
	if err := json.Unmarshal(body, &acceptance); err != nil {
		return RuntimeAcceptance{}, fmt.Errorf("parse runtime acceptance: %w", err)
	}
	if acceptance.SchemaVersion != runtimeSchemaVersion || acceptance.PolicyVersion != runtimeAcceptancePolicyVersion {
		return RuntimeAcceptance{}, fmt.Errorf("unsupported runtime acceptance schema/policy %d/%d", acceptance.SchemaVersion, acceptance.PolicyVersion)
	}
	return acceptance, nil
}

func headlessShellAccepted(platform string, selections RuntimeSelections) bool {
	acceptance, err := loadRuntimeAcceptance()
	if err != nil || !acceptance.Pass || acceptance.Samples < 30 || acceptance.Platform != platform {
		return false
	}
	shell, shellOK := selections.Selected[RuntimeHeadlessShell]
	cft, cftOK := selections.Selected[RuntimeChromeForTesting]
	if !shellOK || !cftOK || shell.Platform != platform || cft.Platform != platform || shell.Version != cft.Version {
		return false
	}
	if acceptance.HeadlessShellVersion != shell.Version || acceptance.ChromeForTestingVersion != cft.Version || acceptance.HeadlessShellVersion != acceptance.ChromeForTestingVersion {
		return false
	}
	if !acceptance.FunctionalPass || !acceptance.VisualPass {
		return false
	}
	if !acceptance.ConsumerPass || validateRuntimeConsumerEvidence(
		acceptance.ConsumerEvidence,
		platform,
		acceptance.HeadlessShellVersion,
		acceptance.ChromeForTestingVersion,
		runtimeAcceptanceBuildVersion(),
	) != nil {
		return false
	}
	if runtime.GOOS == "darwin" && acceptance.FocusIsolationStatus != "pass" {
		return false
	}
	if runtime.GOOS != "darwin" && acceptance.FocusIsolationStatus != "pass" && acceptance.FocusIsolationStatus != "not-applicable" {
		return false
	}
	m := acceptance.Metrics
	return m.HeadlessShellColdP50MS > 0 && m.ChromeForTestingColdP50MS > 0 &&
		m.HeadlessShellColdP50MS <= m.ChromeForTestingColdP50MS*0.90 &&
		m.HeadlessShellMedianRSSBytes > 0 && m.ChromeForTestingMedianRSSBytes > 0 &&
		float64(m.HeadlessShellMedianRSSBytes) <= float64(m.ChromeForTestingMedianRSSBytes)*0.80
}

// ResolveRuntime resolves one launch without host-browser search or network
// activity. An explicit path or environment override is the only external path.
func ResolveRuntime(request RuntimeRequest) (ResolvedRuntime, error) {
	kind := request.Kind
	if kind == "" {
		kind = RuntimeAuto
	}
	if _, err := ParseRuntimeKind(string(kind)); err != nil {
		return ResolvedRuntime{}, err
	}
	externalPath := strings.TrimSpace(request.ExternalPath)
	externalSource := "option"
	if externalPath == "" {
		externalPath, externalSource = chromeOverride()
	}
	if kind == RuntimeAuto && externalPath != "" {
		kind = RuntimeExternal
	}
	if kind == RuntimeExternal {
		if externalPath == "" {
			return ResolvedRuntime{}, errors.New("external browser runtime requires PAGESKEIN_CHROME, NDEV_BROWSER_CHROME, or an explicit browser path")
		}
		return resolveExternalRuntime(externalPath, externalSource, request)
	}

	platform, err := runtimePlatform()
	if err != nil {
		return ResolvedRuntime{}, err
	}
	selections, err := loadRuntimeSelections()
	if err != nil {
		return ResolvedRuntime{}, err
	}
	if kind == RuntimeAuto {
		if request.Headful || request.ExtensionPath != "" {
			kind = RuntimeChromeForTesting
		} else if headlessShellAccepted(platform, selections) {
			kind = RuntimeHeadlessShell
		} else {
			kind = RuntimeChromeForTesting
		}
	}
	capabilities, err := managedRuntimeCapabilities(kind)
	if err != nil {
		return ResolvedRuntime{}, err
	}
	if err := requireRuntimeCapabilities(kind, capabilities, request); err != nil {
		return ResolvedRuntime{}, err
	}
	selection, ok := selections.Selected[kind]
	if !ok || selection.Platform != platform {
		return ResolvedRuntime{}, missingRuntimeError(kind)
	}
	resolved, _, err := verifyInstalledRuntime(kind, selection.Platform, selection.Version)
	if err != nil {
		return ResolvedRuntime{}, fmt.Errorf("selected %s runtime is not usable: %w; reinstall with `%s`", kind, err, runtimeInstallRemediation(kind))
	}
	if err := requireRuntimeCapabilities(kind, resolved.Capabilities, request); err != nil {
		return ResolvedRuntime{}, err
	}
	return resolved, nil
}

func resolveExternalRuntime(path, source string, request RuntimeRequest) (ResolvedRuntime, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ResolvedRuntime{}, fmt.Errorf("external browser path %q: %w", path, err)
	}
	if info.IsDir() {
		return ResolvedRuntime{}, fmt.Errorf("external browser path %q is a directory", path)
	}
	version, err := probeRuntimeVersion(path)
	if err != nil {
		return ResolvedRuntime{}, fmt.Errorf("probe external browser %q: %w", path, err)
	}
	capabilities := RuntimeCapabilities{
		Headless: true, Headful: true, Extensions: chromeSupportsLoadExtension(version), Screenshots: true,
	}
	if err := requireRuntimeCapabilities(RuntimeExternal, capabilities, request); err != nil {
		return ResolvedRuntime{}, err
	}
	return ResolvedRuntime{Kind: RuntimeExternal, Version: version, Path: path, Source: "explicit:" + source, Capabilities: capabilities}, nil
}

func requireRuntimeCapabilities(kind RuntimeKind, capabilities RuntimeCapabilities, request RuntimeRequest) error {
	if request.Headful && !capabilities.Headful {
		return fmt.Errorf("browser runtime %s does not support headful sessions; use --runtime chrome-for-testing", kind)
	}
	if request.ExtensionPath != "" && !capabilities.Extensions {
		return fmt.Errorf("browser runtime %s does not support unpacked extensions; use --runtime chrome-for-testing", kind)
	}
	if !request.Headful && !capabilities.Headless {
		return fmt.Errorf("browser runtime %s does not support headless sessions", kind)
	}
	return nil
}

func missingRuntimeError(kind RuntimeKind) error {
	return fmt.Errorf("managed browser runtime %s is not installed; run `%s`", kind, runtimeInstallRemediation(kind))
}

func runtimeInstallRemediation(kind RuntimeKind) string {
	return fmt.Sprintf("ndev browser runtime install --runtime %s --channel stable", kind)
}

func verifyInstalledRuntime(kind RuntimeKind, platform, version string) (ResolvedRuntime, RuntimeManifest, error) {
	dir, err := runtimeInstallDir(kind, platform, version)
	if err != nil {
		return ResolvedRuntime{}, RuntimeManifest{}, err
	}
	manifestPath := filepath.Join(dir, "runtime.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return ResolvedRuntime{}, RuntimeManifest{}, err
	}
	manifestDigest := sha256.Sum256(body)
	var manifest RuntimeManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return ResolvedRuntime{}, RuntimeManifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	capabilities, err := validateRuntimeManifest(manifest, kind, platform, version)
	if err != nil {
		return ResolvedRuntime{}, RuntimeManifest{}, err
	}
	binaryPath, err := containedRuntimePath(dir, manifest.BinaryRelPath)
	if err != nil {
		return ResolvedRuntime{}, RuntimeManifest{}, err
	}
	gotDigest, err := fileSHA256(binaryPath)
	if err != nil {
		return ResolvedRuntime{}, RuntimeManifest{}, err
	}
	if !strings.EqualFold(gotDigest, manifest.BinarySHA256) {
		return ResolvedRuntime{}, RuntimeManifest{}, fmt.Errorf("binary digest mismatch: got %s want %s", gotDigest, manifest.BinarySHA256)
	}
	return ResolvedRuntime{
		Kind: kind, Version: version, Managed: true, Path: binaryPath, Source: "managed-selection",
		ManifestDigest: hex.EncodeToString(manifestDigest[:]), Capabilities: capabilities,
	}, manifest, nil
}

func validateRuntimeManifest(manifest RuntimeManifest, kind RuntimeKind, platform, version string) (RuntimeCapabilities, error) {
	if manifest.SchemaVersion != runtimeSchemaVersion {
		return RuntimeCapabilities{}, fmt.Errorf("unsupported runtime manifest schema %d", manifest.SchemaVersion)
	}
	if manifest.RuntimeKind != kind || manifest.Platform != platform || manifest.Version != version {
		return RuntimeCapabilities{}, errors.New("runtime manifest identity does not match its installation path")
	}
	if manifest.InstalledAt.IsZero() || manifest.SourceURL == "" || manifest.VersionOutput == "" {
		return RuntimeCapabilities{}, errors.New("runtime manifest is missing provenance or version evidence")
	}
	if !validSHA256(manifest.ArchiveSHA256) || !validSHA256(manifest.BinarySHA256) {
		return RuntimeCapabilities{}, errors.New("runtime manifest contains an invalid SHA-256 digest")
	}
	expected, err := managedRuntimeCapabilities(kind)
	if err != nil {
		return RuntimeCapabilities{}, err
	}
	if manifest.Capabilities != expected {
		return RuntimeCapabilities{}, errors.New("runtime manifest capabilities disagree with code-owned policy")
	}
	return expected, nil
}

func containedRuntimePath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe runtime executable path %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe runtime executable path %q", rel)
	}
	path := filepath.Join(root, clean)
	relCheck, err := filepath.Rel(root, path)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime executable escapes installation root: %q", rel)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime executable symlink escapes installation root: %q", rel)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("runtime executable path %q is a directory", rel)
	}
	return resolvedPath, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-runtime-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ListRuntimeRecords returns deterministic installed-runtime state. Corrupt
// installations remain visible with a typed verification error.
func ListRuntimeRecords() ([]RuntimeRecord, error) {
	root, err := runtimesDir()
	if err != nil {
		return nil, err
	}
	selections, selectionErr := loadRuntimeSelections()
	if selectionErr != nil {
		return nil, selectionErr
	}
	records := []RuntimeRecord{}
	for _, kind := range []RuntimeKind{RuntimeChromeForTesting, RuntimeHeadlessShell} {
		kindDir := filepath.Join(root, string(kind))
		platforms, err := os.ReadDir(kindDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, platformEntry := range platforms {
			if !platformEntry.IsDir() {
				continue
			}
			versions, err := os.ReadDir(filepath.Join(kindDir, platformEntry.Name()))
			if err != nil {
				return nil, err
			}
			for _, versionEntry := range versions {
				if !versionEntry.IsDir() {
					continue
				}
				platform, version := platformEntry.Name(), versionEntry.Name()
				record := RuntimeRecord{Kind: kind, Version: version, Platform: platform, Managed: true}
				if selected, ok := selections.Selected[kind]; ok && selected.Platform == platform && selected.Version == version {
					record.Selected = true
				}
				resolved, manifest, verifyErr := verifyInstalledRuntime(kind, platform, version)
				if verifyErr != nil {
					record.Error = verifyErr.Error()
				} else {
					record.Verified = true
					record.Path = resolved.Path
					record.ManifestDigest = resolved.ManifestDigest
					record.Capabilities = resolved.Capabilities
					record.InstalledAt = manifest.InstalledAt
					record.ArchiveSHA256 = manifest.ArchiveSHA256
					record.BinarySHA256 = manifest.BinarySHA256
				}
				records = append(records, record)
			}
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Kind != records[j].Kind {
			return records[i].Kind < records[j].Kind
		}
		if records[i].Platform != records[j].Platform {
			return records[i].Platform < records[j].Platform
		}
		return records[i].Version < records[j].Version
	})
	return records, nil
}

func sessionRuntimeIdentityValid(session *Session) (bool, error) {
	if session == nil || session.RuntimeKind == "" {
		return true, nil // legacy session; native process identity checks still apply.
	}
	if !session.RuntimeManaged {
		return session.RuntimeKind == RuntimeExternal, nil
	}
	platform, err := runtimePlatform()
	if err != nil {
		return false, err
	}
	resolved, _, err := verifyInstalledRuntime(session.RuntimeKind, platform, session.RuntimeVersion)
	if err != nil {
		return false, fmt.Errorf("verify session runtime: %w", err)
	}
	if filepath.Clean(resolved.Path) != filepath.Clean(session.ChromePath) ||
		!strings.EqualFold(resolved.ManifestDigest, session.RuntimeManifest) ||
		resolved.Capabilities != session.RuntimeCaps {
		return false, nil
	}
	return true, nil
}
