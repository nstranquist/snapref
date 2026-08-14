package pageskein_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type extractionContract struct {
	SchemaVersion int    `json:"schema_version"`
	ProductID     string `json:"product_id"`
	Algorithm     string `json:"algorithm"`
	SourceSHA256  string `json:"source_sha256"`
}

func TestExtractionContract(t *testing.T) {
	body, err := os.ReadFile("EXTRACTION.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract extractionContract
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != 1 || contract.ProductID != "product.pageskein" {
		t.Fatalf("unexpected extraction contract: %+v", contract)
	}
	if contract.Algorithm != "sha256:path-nul-portable-mode-nul-content-nul:v2" {
		t.Fatalf("unsupported extraction digest algorithm %q", contract.Algorithm)
	}
	digest, err := sharedSourceDigest(".")
	if err != nil {
		t.Fatal(err)
	}
	if digest != contract.SourceSHA256 {
		t.Fatalf("shared source digest = %s, want %s; synchronize the factory and standalone trees, then review EXTRACTION.json", digest, contract.SourceSHA256)
	}
}

func sharedSourceDigest(root string) (string, error) {
	excludedFiles := map[string]bool{
		"EXTRACTION.json":             true,
		"RELEASE_CHECKLIST.md":        true,
		"catalog.yaml":                true,
		"coverage.out":                true,
		"extraction_contract_test.go": true,
		"pageskein":                   true,
		"standalone_contract_test.go": true,
	}
	excludedDirs := map[string]bool{
		".git":            true,
		".nicos":          true,
		".playwright-mcp": true,
		"dist":            true,
		"portfolio":       true,
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			if excludedDirs[relative] {
				return filepath.SkipDir
			}
			return nil
		}
		if excludedFiles[relative] || strings.HasSuffix(relative, ".test") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("shared source path %s is not a regular file", relative)
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return "", err
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return "", err
		}
		mode, err := canonicalSharedFileMode(runtime.GOOS, info.Mode())
		if err != nil {
			return "", fmt.Errorf("shared source path %s: %w", relative, err)
		}
		_, _ = fmt.Fprintf(hash, "%s%c%04o%c", relative, 0, mode, 0)
		_, _ = hash.Write(body)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalSharedFileMode(goos string, mode fs.FileMode) (fs.FileMode, error) {
	const canonicalMode fs.FileMode = 0o644
	if goos != "windows" && mode.Perm() != canonicalMode {
		return 0, fmt.Errorf("permissions are %04o, want %04o", mode.Perm(), canonicalMode)
	}
	return canonicalMode, nil
}

func TestCanonicalSharedFileModeNormalizesWindowsPermissions(t *testing.T) {
	got, err := canonicalSharedFileMode("windows", 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0o644 {
		t.Fatalf("canonical Windows mode = %04o, want 0644", got)
	}
	if _, err := canonicalSharedFileMode("linux", 0o666); err == nil {
		t.Fatal("non-Windows permission drift was accepted")
	}
}
