package browser

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const runtimeConsumerEvidenceSchemaVersion = 1

var runtimeAcceptanceBuildVersion = RuntimeAcceptanceBuildIdentity

var (
	runtimeAcceptanceBuildIdentityOnce sync.Once
	runtimeAcceptanceBuildIdentity     string
)

// RuntimeAcceptanceBuildIdentity binds acceptance evidence to the exact
// executable bytes, not merely a VCS revision plus a dirty-worktree marker.
// Hashing is cached because a process may resolve auto more than once.
func RuntimeAcceptanceBuildIdentity() string {
	runtimeAcceptanceBuildIdentityOnce.Do(func() {
		path, err := os.Executable()
		if err != nil {
			runtimeAcceptanceBuildIdentity = "dev"
			return
		}
		file, err := os.Open(path)
		if err != nil {
			runtimeAcceptanceBuildIdentity = "dev"
			return
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			runtimeAcceptanceBuildIdentity = "dev"
			return
		}
		label := BrowserBuildVersion()
		if label == "" {
			label = "dev"
		}
		runtimeAcceptanceBuildIdentity = fmt.Sprintf("%s+sha256.%x", label, hash.Sum(nil))
	})
	return runtimeAcceptanceBuildIdentity
}

// RuntimeConsumerSurface is one product surface that must prove the selected
// Headless Shell is a compatible SnapRef runtime before auto may prefer it.
type RuntimeConsumerSurface string

const (
	RuntimeConsumerSelfTest           RuntimeConsumerSurface = "self-test"
	RuntimeConsumerTypedTools         RuntimeConsumerSurface = "typed-tools"
	RuntimeConsumerScreenshotFixtures RuntimeConsumerSurface = "screenshot-fixtures"
	RuntimeConsumerObserver           RuntimeConsumerSurface = "observer"
	RuntimeConsumerScripts            RuntimeConsumerSurface = "scripts"
	RuntimeConsumerSkills             RuntimeConsumerSurface = "skills"
	RuntimeConsumerMCP                RuntimeConsumerSurface = "mcp"
)

type RuntimeConsumerStatus string

const (
	RuntimeConsumerStatusPass RuntimeConsumerStatus = "pass"
	RuntimeConsumerStatusFail RuntimeConsumerStatus = "fail"
)

// RuntimeConsumerCheck binds one typed consumer result to the SHA-256 of its
// durable receipt. Receipt contents remain outside runtime state; the digest
// makes replacing or accidentally mixing a receipt detectable to the producer.
type RuntimeConsumerCheck struct {
	Surface       RuntimeConsumerSurface `json:"surface"`
	Status        RuntimeConsumerStatus  `json:"status"`
	ReceiptPath   string                 `json:"receipt_path"`
	ReceiptSHA256 string                 `json:"receipt_sha256"`
}

// RuntimeConsumerResult is the bounded input accepted from a host consumer
// smoke orchestrator. Status is derived from ExitCode when receipts are written.
type RuntimeConsumerResult struct {
	Surface    RuntimeConsumerSurface
	Command    []string
	ExitCode   int
	Output     string
	StartedAt  time.Time
	FinishedAt time.Time
}

type runtimeConsumerReceipt struct {
	SchemaVersion           int                    `json:"schema_version"`
	PolicyVersion           int                    `json:"policy_version"`
	Surface                 RuntimeConsumerSurface `json:"surface"`
	Status                  RuntimeConsumerStatus  `json:"status"`
	Platform                string                 `json:"platform"`
	HeadlessShellVersion    string                 `json:"headless_shell_version"`
	ChromeForTestingVersion string                 `json:"chrome_for_testing_version"`
	SnapRefBuildVersion   string                 `json:"snapref_build_version"`
	StartedAt               time.Time              `json:"started_at"`
	FinishedAt              time.Time              `json:"finished_at"`
	Command                 []string               `json:"command"`
	ExitCode                int                    `json:"exit_code"`
	Output                  string                 `json:"output"`
}

// RuntimeConsumerEvidence is supplied by the consumer smoke orchestrator. It
// is deliberately exact-build and exact-runtime bound; a partial, stale, or
// cross-build document can never enable Headless Shell automatic selection.
type RuntimeConsumerEvidence struct {
	SchemaVersion           int                    `json:"schema_version"`
	PolicyVersion           int                    `json:"policy_version"`
	Platform                string                 `json:"platform"`
	HeadlessShellVersion    string                 `json:"headless_shell_version"`
	ChromeForTestingVersion string                 `json:"chrome_for_testing_version"`
	SnapRefBuildVersion   string                 `json:"snapref_build_version"`
	TestedAt                time.Time              `json:"tested_at"`
	Checks                  []RuntimeConsumerCheck `json:"checks"`
}

var requiredRuntimeConsumerSurfaces = []RuntimeConsumerSurface{
	RuntimeConsumerSelfTest,
	RuntimeConsumerTypedTools,
	RuntimeConsumerScreenshotFixtures,
	RuntimeConsumerObserver,
	RuntimeConsumerScripts,
	RuntimeConsumerSkills,
	RuntimeConsumerMCP,
}

// LoadRuntimeConsumerEvidence decodes a bounded, closed JSON contract. Runtime
// identity validation happens inside BenchmarkRuntimes after it has verified
// the exact selected pair.
func LoadRuntimeConsumerEvidence(path string) (*RuntimeConsumerEvidence, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("runtime consumer evidence path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open runtime consumer evidence: %w", err)
	}
	defer file.Close()
	const maxEvidenceBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(file, maxEvidenceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read runtime consumer evidence: %w", err)
	}
	if len(body) > maxEvidenceBytes {
		return nil, errors.New("runtime consumer evidence exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var evidence RuntimeConsumerEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return nil, fmt.Errorf("parse runtime consumer evidence: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("parse runtime consumer evidence: %w", err)
	}
	return &evidence, nil
}

// WriteRuntimeConsumerEvidence writes typed per-surface receipts and an
// evidence document bound to the exact verified selected runtime pair.
func WriteRuntimeConsumerEvidence(path string, results []RuntimeConsumerResult) (*RuntimeConsumerEvidence, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("runtime consumer evidence output path is empty")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime consumer evidence output: %w", err)
	}
	platform, err := runtimePlatform()
	if err != nil {
		return nil, err
	}
	selections, err := loadRuntimeSelections()
	if err != nil {
		return nil, err
	}
	shell, shellOK := selections.Selected[RuntimeHeadlessShell]
	cft, cftOK := selections.Selected[RuntimeChromeForTesting]
	if err := validateRuntimeBenchmarkSelections(shell, shellOK, cft, cftOK, platform); err != nil {
		return nil, err
	}
	if _, _, err := verifyInstalledRuntime(RuntimeHeadlessShell, platform, shell.Version); err != nil {
		return nil, fmt.Errorf("verify headless-shell for consumer evidence: %w", err)
	}
	if _, _, err := verifyInstalledRuntime(RuntimeChromeForTesting, platform, cft.Version); err != nil {
		return nil, fmt.Errorf("verify chrome-for-testing for consumer evidence: %w", err)
	}
	buildVersion := runtimeAcceptanceBuildVersion()
	if buildVersion == "" || buildVersion == "dev" {
		return nil, errors.New("current SnapRef build has no stable build identity")
	}
	if len(results) != len(requiredRuntimeConsumerSurfaces) {
		return nil, fmt.Errorf("runtime consumer results have %d surfaces; want %d", len(results), len(requiredRuntimeConsumerSurfaces))
	}
	receiptDir := strings.TrimSuffix(absolutePath, filepath.Ext(absolutePath)) + ".receipts"
	if err := os.MkdirAll(receiptDir, 0o700); err != nil {
		return nil, fmt.Errorf("create runtime consumer receipt directory: %w", err)
	}
	seen := map[RuntimeConsumerSurface]bool{}
	checks := make([]RuntimeConsumerCheck, 0, len(results))
	var testedAt time.Time
	for _, result := range results {
		if !runtimeConsumerSurfaceRequired(result.Surface) || seen[result.Surface] {
			return nil, fmt.Errorf("runtime consumer result contains unknown or duplicate surface %q", result.Surface)
		}
		seen[result.Surface] = true
		if len(result.Command) == 0 || result.StartedAt.IsZero() || result.FinishedAt.Before(result.StartedAt) {
			return nil, fmt.Errorf("runtime consumer result %q has invalid command or timestamps", result.Surface)
		}
		if len(result.Output) > 1<<20 {
			return nil, fmt.Errorf("runtime consumer result %q output exceeds 1 MiB", result.Surface)
		}
		status := RuntimeConsumerStatusPass
		if result.ExitCode != 0 {
			status = RuntimeConsumerStatusFail
		}
		receipt := runtimeConsumerReceipt{
			SchemaVersion: runtimeConsumerEvidenceSchemaVersion, PolicyVersion: runtimeAcceptancePolicyVersion,
			Surface: result.Surface, Status: status, Platform: platform,
			HeadlessShellVersion: shell.Version, ChromeForTestingVersion: cft.Version,
			SnapRefBuildVersion: buildVersion, StartedAt: result.StartedAt.UTC(), FinishedAt: result.FinishedAt.UTC(),
			Command: append([]string(nil), result.Command...), ExitCode: result.ExitCode, Output: result.Output,
		}
		receiptPath := filepath.Join(receiptDir, string(result.Surface)+".json")
		if err := writeJSONAtomic(receiptPath, receipt, 0o600); err != nil {
			return nil, fmt.Errorf("write runtime consumer receipt %q: %w", result.Surface, err)
		}
		digest, err := fileSHA256(receiptPath)
		if err != nil {
			return nil, fmt.Errorf("hash runtime consumer receipt %q: %w", result.Surface, err)
		}
		checks = append(checks, RuntimeConsumerCheck{Surface: result.Surface, Status: status, ReceiptPath: receiptPath, ReceiptSHA256: digest})
		if result.FinishedAt.After(testedAt) {
			testedAt = result.FinishedAt.UTC()
		}
	}
	evidence := &RuntimeConsumerEvidence{
		SchemaVersion: runtimeConsumerEvidenceSchemaVersion, PolicyVersion: runtimeAcceptancePolicyVersion,
		Platform: platform, HeadlessShellVersion: shell.Version, ChromeForTestingVersion: cft.Version,
		SnapRefBuildVersion: buildVersion, TestedAt: testedAt, Checks: checks,
	}
	evidence = normalizedRuntimeConsumerEvidence(evidence)
	if err := writeJSONAtomic(absolutePath, evidence, 0o600); err != nil {
		return nil, fmt.Errorf("write runtime consumer evidence: %w", err)
	}
	return evidence, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}

func normalizedRuntimeConsumerEvidence(evidence *RuntimeConsumerEvidence) *RuntimeConsumerEvidence {
	if evidence == nil {
		return nil
	}
	copyEvidence := *evidence
	copyEvidence.Checks = append([]RuntimeConsumerCheck(nil), evidence.Checks...)
	sort.Slice(copyEvidence.Checks, func(i, j int) bool {
		return copyEvidence.Checks[i].Surface < copyEvidence.Checks[j].Surface
	})
	return &copyEvidence
}

func validateRuntimeConsumerEvidence(evidence *RuntimeConsumerEvidence, platform, shellVersion, cftVersion, buildVersion string) error {
	if evidence == nil {
		return errors.New("runtime consumer evidence is required")
	}
	if evidence.SchemaVersion != runtimeConsumerEvidenceSchemaVersion || evidence.PolicyVersion != runtimeAcceptancePolicyVersion {
		return fmt.Errorf("unsupported runtime consumer evidence schema/policy %d/%d", evidence.SchemaVersion, evidence.PolicyVersion)
	}
	if evidence.Platform != platform || evidence.HeadlessShellVersion != shellVersion || evidence.ChromeForTestingVersion != cftVersion {
		return errors.New("runtime consumer evidence does not match the selected platform and runtime versions")
	}
	if buildVersion == "" || buildVersion == "dev" {
		return errors.New("current SnapRef build has no stable build identity")
	}
	if evidence.SnapRefBuildVersion != buildVersion {
		return fmt.Errorf("runtime consumer evidence build %q does not match current SnapRef build %q", evidence.SnapRefBuildVersion, buildVersion)
	}
	if evidence.TestedAt.IsZero() || evidence.TestedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return errors.New("runtime consumer evidence has an invalid tested_at timestamp")
	}
	if len(evidence.Checks) != len(requiredRuntimeConsumerSurfaces) {
		return fmt.Errorf("runtime consumer evidence has %d checks; want %d", len(evidence.Checks), len(requiredRuntimeConsumerSurfaces))
	}
	required := make(map[RuntimeConsumerSurface]bool, len(requiredRuntimeConsumerSurfaces))
	for _, surface := range requiredRuntimeConsumerSurfaces {
		required[surface] = true
	}
	seen := make(map[RuntimeConsumerSurface]bool, len(evidence.Checks))
	for _, check := range evidence.Checks {
		if !required[check.Surface] {
			return fmt.Errorf("runtime consumer evidence contains unknown surface %q", check.Surface)
		}
		if seen[check.Surface] {
			return fmt.Errorf("runtime consumer evidence contains duplicate surface %q", check.Surface)
		}
		seen[check.Surface] = true
		if check.Status != RuntimeConsumerStatusPass {
			return fmt.Errorf("runtime consumer surface %q status is %q, want pass", check.Surface, check.Status)
		}
		if !validSHA256(check.ReceiptSHA256) || strings.Trim(check.ReceiptSHA256, "0") == "" {
			return fmt.Errorf("runtime consumer surface %q has an invalid receipt SHA-256", check.Surface)
		}
		if err := verifyRuntimeConsumerReceipt(check, evidence); err != nil {
			return err
		}
	}
	return nil
}

func runtimeConsumerSurfaceRequired(surface RuntimeConsumerSurface) bool {
	for _, required := range requiredRuntimeConsumerSurfaces {
		if surface == required {
			return true
		}
	}
	return false
}

func verifyRuntimeConsumerReceipt(check RuntimeConsumerCheck, evidence *RuntimeConsumerEvidence) error {
	if !filepath.IsAbs(check.ReceiptPath) {
		return fmt.Errorf("runtime consumer surface %q receipt path is not absolute", check.Surface)
	}
	info, err := os.Lstat(check.ReceiptPath)
	if err != nil {
		return fmt.Errorf("runtime consumer surface %q receipt unavailable: %w", check.Surface, err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("runtime consumer surface %q receipt is not a bounded regular file", check.Surface)
	}
	digest, err := fileSHA256(check.ReceiptPath)
	if err != nil || digest != check.ReceiptSHA256 {
		return fmt.Errorf("runtime consumer surface %q receipt digest mismatch", check.Surface)
	}
	body, err := os.ReadFile(check.ReceiptPath)
	if err != nil {
		return fmt.Errorf("read runtime consumer surface %q receipt: %w", check.Surface, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var receipt runtimeConsumerReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return fmt.Errorf("parse runtime consumer surface %q receipt: %w", check.Surface, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("parse runtime consumer surface %q receipt: %w", check.Surface, err)
	}
	if receipt.SchemaVersion != runtimeConsumerEvidenceSchemaVersion || receipt.PolicyVersion != runtimeAcceptancePolicyVersion ||
		receipt.Surface != check.Surface || receipt.Status != check.Status || receipt.Platform != evidence.Platform ||
		receipt.HeadlessShellVersion != evidence.HeadlessShellVersion || receipt.ChromeForTestingVersion != evidence.ChromeForTestingVersion ||
		receipt.SnapRefBuildVersion != evidence.SnapRefBuildVersion || receipt.StartedAt.IsZero() || receipt.FinishedAt.Before(receipt.StartedAt) ||
		len(receipt.Command) == 0 || receipt.ExitCode != 0 {
		return fmt.Errorf("runtime consumer surface %q receipt identity or result is invalid", check.Surface)
	}
	return nil
}
