package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinDetectsEditedMissingAndUnexpectedFiles(t *testing.T) {
	root := t.TempDir()
	body := []byte("source")
	path := filepath.Join(root, "source.txt")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, paths, err := Digest(root)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	pin, err := json.Marshal(map[string]any{"schema_version": 1, "authority": "github.com/nstranquist/snapref", "source": map[string]any{"revision": strings.Repeat("a", 40), "sha256": digest, "files": len(paths)}, "files": map[string]string{"source.txt": hex.EncodeToString(sum[:])}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SOURCE.json"), pin, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPin(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPin(root); err == nil {
		t.Fatal("edited file accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPin(root); err == nil {
		t.Fatal("missing file accepted")
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unexpected.txt"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPin(root); err == nil {
		t.Fatal("unexpected file accepted")
	}
}
