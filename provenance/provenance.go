// Package provenance describes stable source inputs without build timestamps.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
)

// These values can be injected by the checked build/release runner. Ordinary
// go builds fall back to Go's VCS metadata and identify themselves as development.
var SourceRevision = ""
var SourceSHA256 = ""
var BuildKind = "development"

type Info struct {
	SchemaVersion  int    `json:"schema_version"`
	Version        string `json:"version"`
	SourceRevision string `json:"source_revision"`
	SourceSHA256   string `json:"source_sha256"`
	BuildKind      string `json:"build_kind"`
	Target         string `json:"target"`
	Modified       bool   `json:"modified"`
}

func Current(version string) Info {
	info := Info{SchemaVersion: 1, Version: version, SourceRevision: SourceRevision, SourceSHA256: SourceSHA256, BuildKind: BuildKind, Target: runtime.GOOS + "/" + runtime.GOARCH}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.SourceRevision == "" {
					info.SourceRevision = setting.Value
				}
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}
	return info
}

// Digest implements the extraction v2 contract. The generated factory pin and
// extraction receipt are excluded to avoid self-reference. Unknown source files
// are included, so an accidental file cannot silently escape verification.
func Digest(root string) (string, []string, error) {
	excludedFiles := map[string]bool{"EXTRACTION.json": true, "SOURCE.json": true, "RELEASE_CHECKLIST.md": true, "catalog.yaml": true, "coverage.out": true, "extraction_contract_test.go": true, "snapref": true, "standalone_contract_test.go": true}
	excludedDirs := map[string]bool{".git": true, ".nicos": true, ".playwright-mcp": true, "bin": true, "dist": true, "portfolio": true}
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			if excludedDirs[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if excludedFiles[rel] || strings.HasSuffix(rel, ".test") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("source path %s is not a regular file", rel)
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		absolute := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Stat(absolute)
		if err != nil {
			return "", nil, err
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
			return "", nil, fmt.Errorf("source path %s permissions are %04o, want 0644", path, info.Mode().Perm())
		}
		body, err := os.ReadFile(absolute)
		if err != nil {
			return "", nil, err
		}
		fmt.Fprintf(hash, "%s%c0644%c", path, 0, 0)
		hash.Write(body)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), paths, nil
}

func LinkFlags(digest, revision, kind string) string {
	return "-s -w -buildid= -X github.com/nstranquist/snapref/provenance.SourceSHA256=" + digest +
		" -X github.com/nstranquist/snapref/provenance.SourceRevision=" + revision +
		" -X github.com/nstranquist/snapref/provenance.BuildKind=" + kind
}
