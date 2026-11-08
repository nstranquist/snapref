package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// VerifyPin checks the generated factory copy without requiring the canonical
// checkout or network access. The independent extraction digest remains a second
// check on the exported source set.
func VerifyPin(root string) error {
	var pin struct {
		SchemaVersion int    `json:"schema_version"`
		Authority     string `json:"authority"`
		Source        struct {
			Revision string `json:"revision"`
			SHA256   string `json:"sha256"`
			Files    int    `json:"files"`
		} `json:"source"`
		Files map[string]string `json:"files"`
	}
	body, err := os.ReadFile(filepath.Join(root, "SOURCE.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, &pin); err != nil {
		return err
	}
	revision, revisionErr := hex.DecodeString(pin.Source.Revision)
	if pin.SchemaVersion != 1 || pin.Authority != "github.com/nstranquist/snapref" || revisionErr != nil || len(revision) != 20 || len(pin.Files) == 0 {
		return fmt.Errorf("invalid source pin")
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			switch rel {
			case ".git", ".nicos", ".playwright-mcp", "bin", "dist", "portfolio":
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "catalog.yaml" || rel == "SOURCE.json" || rel == "RELEASE_CHECKLIST.md" || rel == "standalone_contract_test.go" || rel == "coverage.out" || strings.HasSuffix(rel, ".test") {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular pinned source: %s", rel)
		}
		want, ok := pin.Files[rel]
		if !ok {
			return fmt.Errorf("unexpected pinned source: %s", rel)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != want {
			return fmt.Errorf("pinned source changed: %s; edit the canonical repository and export", rel)
		}
		seen[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(pin.Files) {
		return fmt.Errorf("pinned source missing")
	}
	digest, paths, err := Digest(root)
	if err != nil {
		return err
	}
	if digest != pin.Source.SHA256 || len(paths) != pin.Source.Files {
		return fmt.Errorf("source pin digest mismatch")
	}
	return nil
}
