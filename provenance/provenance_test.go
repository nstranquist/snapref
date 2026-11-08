package provenance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDigestTracksContentAndIgnoresGeneratedPin(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n")
	first, paths, err := Digest(root)
	if err != nil || len(paths) != 1 {
		t.Fatalf("digest: %v %v", paths, err)
	}
	write("SOURCE.json", `{"revision":"other"}`)
	second, _, err := Digest(root)
	if err != nil || second != first {
		t.Fatalf("pin changed digest: %v", err)
	}
	write("main.go", "package changed\n")
	third, _, err := Digest(root)
	if err != nil || third == first {
		t.Fatalf("source change lost: %v", err)
	}
	write("unexpected.txt", "included")
	fourth, paths, err := Digest(root)
	if err != nil || fourth == third || len(paths) != 2 {
		t.Fatalf("unexpected source file lost: %v", err)
	}
}

func TestDigestRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("outside", filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	if _, _, err := Digest(root); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestInjectedIdentityIsReported(t *testing.T) {
	oldRev, oldDigest, oldKind := SourceRevision, SourceSHA256, BuildKind
	defer func() { SourceRevision, SourceSHA256, BuildKind = oldRev, oldDigest, oldKind }()
	SourceRevision, SourceSHA256, BuildKind = "revision-a", "digest-a", "candidate"
	a := Current("0.2.0-rc.2")
	SourceSHA256 = "digest-b"
	b := Current("0.2.0-rc.2")
	if a.SourceSHA256 == b.SourceSHA256 || a.Version != b.Version || a.BuildKind != "candidate" || a.Target == "" {
		t.Fatalf("identity: %+v %+v", a, b)
	}
}
