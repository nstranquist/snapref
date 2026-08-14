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

func TestRuntimeAcceptanceBuildIdentityIncludesExecutableDigest(t *testing.T) {
	identity := RuntimeAcceptanceBuildIdentity()
	marker := "+sha256."
	index := strings.LastIndex(identity, marker)
	if index < 1 {
		t.Fatalf("identity=%q", identity)
	}
	digest := identity[index+len(marker):]
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		t.Fatalf("digest=%q bytes=%d err=%v", digest, len(decoded), err)
	}
}

const runtimeTestBuildVersion = "pageskein-test-build"

func passingRuntimeConsumerEvidence(t *testing.T, platform, shellVersion, cftVersion string) *RuntimeConsumerEvidence {
	t.Helper()
	checks := make([]RuntimeConsumerCheck, 0, len(requiredRuntimeConsumerSurfaces))
	receiptDir := t.TempDir()
	now := time.Now().UTC()
	for _, surface := range requiredRuntimeConsumerSurfaces {
		receiptPath := filepath.Join(receiptDir, string(surface)+".json")
		receipt := runtimeConsumerReceipt{
			SchemaVersion: runtimeConsumerEvidenceSchemaVersion, PolicyVersion: runtimeAcceptancePolicyVersion,
			Surface: surface, Status: RuntimeConsumerStatusPass, Platform: platform,
			HeadlessShellVersion: shellVersion, ChromeForTestingVersion: cftVersion,
			PageSkeinBuildVersion: runtimeTestBuildVersion, StartedAt: now.Add(-time.Second), FinishedAt: now,
			Command: []string{"test", string(surface)}, ExitCode: 0, Output: "ok",
		}
		if err := writeJSONAtomic(receiptPath, receipt, 0o600); err != nil {
			t.Fatal(err)
		}
		digest, err := fileSHA256(receiptPath)
		if err != nil {
			t.Fatal(err)
		}
		checks = append(checks, RuntimeConsumerCheck{
			Surface: surface, Status: RuntimeConsumerStatusPass, ReceiptPath: receiptPath, ReceiptSHA256: digest,
		})
	}
	return &RuntimeConsumerEvidence{
		SchemaVersion: runtimeConsumerEvidenceSchemaVersion,
		PolicyVersion: runtimeAcceptancePolicyVersion,
		Platform:      platform, HeadlessShellVersion: shellVersion, ChromeForTestingVersion: cftVersion,
		PageSkeinBuildVersion: runtimeTestBuildVersion,
		TestedAt:              now,
		Checks:                checks,
	}
}

func useRuntimeTestBuildVersion(t *testing.T) {
	t.Helper()
	previous := runtimeAcceptanceBuildVersion
	runtimeAcceptanceBuildVersion = func() string { return runtimeTestBuildVersion }
	t.Cleanup(func() { runtimeAcceptanceBuildVersion = previous })
}

func TestRuntimeConsumerEvidenceValidationFailsClosed(t *testing.T) {
	const platform = "darwin-arm64"
	const version = "151.0.0.0"
	evidence := passingRuntimeConsumerEvidence(t, platform, version, version)
	if err := validateRuntimeConsumerEvidence(evidence, platform, version, version, runtimeTestBuildVersion); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}

	tests := map[string]func(*RuntimeConsumerEvidence){
		"wrong policy":   func(e *RuntimeConsumerEvidence) { e.PolicyVersion-- },
		"wrong platform": func(e *RuntimeConsumerEvidence) { e.Platform = "linux-amd64" },
		"wrong runtime":  func(e *RuntimeConsumerEvidence) { e.HeadlessShellVersion = "150.0.0.0" },
		"wrong build":    func(e *RuntimeConsumerEvidence) { e.PageSkeinBuildVersion = "other-build" },
		"missing check":  func(e *RuntimeConsumerEvidence) { e.Checks = e.Checks[:len(e.Checks)-1] },
		"failed check":   func(e *RuntimeConsumerEvidence) { e.Checks[0].Status = RuntimeConsumerStatusFail },
		"bad receipt":    func(e *RuntimeConsumerEvidence) { e.Checks[0].ReceiptSHA256 = strings.Repeat("0", 64) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := normalizedRuntimeConsumerEvidence(evidence)
			mutate(candidate)
			if err := validateRuntimeConsumerEvidence(candidate, platform, version, version, runtimeTestBuildVersion); err == nil {
				t.Fatal("invalid consumer evidence accepted")
			}
		})
	}
	if err := validateRuntimeConsumerEvidence(evidence, platform, version, version, "dev"); err == nil {
		t.Fatal("unversioned current build accepted")
	}
	if err := os.WriteFile(evidence.Checks[0].ReceiptPath, []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeConsumerEvidence(evidence, platform, version, version, runtimeTestBuildVersion); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered receipt error = %v", err)
	}
}

func TestLoadRuntimeConsumerEvidenceIsBoundedAndClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consumer.json")
	evidence := passingRuntimeConsumerEvidence(t, "darwin-arm64", "151.0.0.0", "151.0.0.0")
	body, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConsumerEvidence(path); err != nil {
		t.Fatalf("valid evidence did not load: %v", err)
	}
	if err := os.WriteFile(path, append(body[:len(body)-1], []byte(`,"unknown":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConsumerEvidence(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConsumerEvidence(path); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("oversized evidence error = %v", err)
	}
}

func TestWriteRuntimeConsumerEvidenceProducesVerifiableReceipts(t *testing.T) {
	useRuntimeTestBuildVersion(t)
	t.Setenv("PAGESKEIN_HOME", t.TempDir())
	platform, err := runtimePlatform()
	if err != nil {
		t.Fatal(err)
	}
	const version = "151.0.0.0"
	writeManagedRuntimeFixture(t, RuntimeHeadlessShell, platform, version)
	writeManagedRuntimeFixture(t, RuntimeChromeForTesting, platform, version)
	if err := saveRuntimeSelections(RuntimeSelections{Selected: map[RuntimeKind]RuntimeSelection{
		RuntimeHeadlessShell: {Platform: platform, Version: version}, RuntimeChromeForTesting: {Platform: platform, Version: version},
	}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	results := make([]RuntimeConsumerResult, 0, len(requiredRuntimeConsumerSurfaces))
	for _, surface := range requiredRuntimeConsumerSurfaces {
		results = append(results, RuntimeConsumerResult{
			Surface: surface, Command: []string{"go", "test", string(surface)}, ExitCode: 0,
			Output: "ok", StartedAt: now.Add(-time.Second), FinishedAt: now,
		})
	}
	outputPath := filepath.Join(t.TempDir(), "consumer-evidence.json")
	evidence, err := WriteRuntimeConsumerEvidence(outputPath, results)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Checks) != len(requiredRuntimeConsumerSurfaces) {
		t.Fatalf("checks = %d", len(evidence.Checks))
	}
	if err := validateRuntimeConsumerEvidence(evidence, platform, version, version, runtimeTestBuildVersion); err != nil {
		t.Fatalf("written evidence rejected: %v", err)
	}
	loaded, err := LoadRuntimeConsumerEvidence(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeConsumerEvidence(loaded, platform, version, version, runtimeTestBuildVersion); err != nil {
		t.Fatalf("loaded evidence rejected: %v", err)
	}
}
