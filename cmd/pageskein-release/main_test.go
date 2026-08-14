package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testReleaseVersion = "0.2.0-rc.1"

func TestReleaseVersionMirrorsCLI(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	version, err := validateReleaseVersion(root)
	if err != nil {
		t.Fatal(err)
	}
	if version != testReleaseVersion {
		t.Fatalf("validateReleaseVersion() = %q, want %q", version, testReleaseVersion)
	}
}

func TestArchiveEntriesPutBinaryFirstAndLegalFilesAfter(t *testing.T) {
	root := t.TempDir()
	entries := archiveEntries(root, "/tmp/pageskein.exe", "pageskein.exe")
	if len(entries) != 1+len(releaseFiles) || entries[0].name != "pageskein.exe" || entries[0].mode != 0o755 {
		t.Fatalf("unexpected archive entries: %+v", entries)
	}
	for _, entry := range entries[1:] {
		if entry.mode != 0o644 {
			t.Fatalf("release file %s mode = %o", entry.name, entry.mode)
		}
		if !strings.HasPrefix(entry.path, root+string(filepath.Separator)) {
			t.Fatalf("release file %s escaped source root: %s", entry.name, entry.path)
		}
	}
}

func TestReleaseArtifactsRejectProtectedOutputPaths(t *testing.T) {
	root := releaseFixture(t)
	marker := filepath.Join(root, "source-marker")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	buildCalls := 0
	builder := func(_ string, _ target, _ string) error {
		buildCalls++
		return errors.New("builder must not run")
	}
	volumeRoot := filepath.Clean(filepath.VolumeName(root) + string(filepath.Separator))
	for _, out := range []string{"", ".", root, filepath.Dir(root), volumeRoot} {
		if err := releaseArtifacts(root, out, testReleaseVersion, builder); err == nil {
			t.Fatalf("releaseArtifacts(%q) succeeded, want protected-output error", out)
		}
	}
	if buildCalls != 0 {
		t.Fatalf("builder ran %d times for protected outputs", buildCalls)
	}
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "preserve" {
		t.Fatalf("source marker changed: body=%q err=%v", body, err)
	}
}

func TestReleaseArtifactsPreserveUnrelatedOutputAndAreReproducible(t *testing.T) {
	root := releaseFixture(t)
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.Mkdir(first, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(first, "operator-notes.txt")
	if err := os.WriteFile(sentinel, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldArtifact := filepath.Join(first, "pageskein-"+testReleaseVersion+"-darwin-arm64.tar.gz")
	if err := os.WriteFile(oldArtifact, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := releaseArtifacts(root, first, testReleaseVersion, deterministicTestBuilder); err != nil {
		t.Fatal(err)
	}
	if err := releaseArtifacts(root, second, testReleaseVersion, deterministicTestBuilder); err != nil {
		t.Fatal(err)
	}
	if err := compareReleaseDirsIgnoring(first, second, "operator-notes.txt"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "keep me" {
		t.Fatalf("unrelated output changed: body=%q err=%v", body, err)
	}
	body, err = os.ReadFile(oldArtifact)
	if err != nil || string(body) == "old" {
		t.Fatalf("managed artifact was not replaced: size=%d err=%v", len(body), err)
	}
}

func TestReleaseArtifactsLeaveOutputUntouchedWhenBuildFails(t *testing.T) {
	root := releaseFixture(t)
	out := filepath.Join(root, "dist")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(out, "SHA256SUMS")
	if err := os.WriteFile(existing, []byte("old manifest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	builds := 0
	failingBuilder := func(_ string, target target, output string) error {
		builds++
		if builds == 2 {
			return errors.New("injected build failure")
		}
		return os.WriteFile(output, []byte(target.OS+"/"+target.Arch), 0o755)
	}
	if err := releaseArtifacts(root, out, testReleaseVersion, failingBuilder); err == nil {
		t.Fatal("releaseArtifacts succeeded, want build failure")
	}
	body, err := os.ReadFile(existing)
	if err != nil || string(body) != "old manifest\n" {
		t.Fatalf("existing output changed after build failure: body=%q err=%v", body, err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("output gained partial artifacts: %+v", entries)
	}
}

func TestReleaseArtifactsRejectNonRegularManagedDestination(t *testing.T) {
	root := releaseFixture(t)
	out := filepath.Join(root, "dist")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(out, "pageskein-"+testReleaseVersion+"-darwin-arm64.tar.gz")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := releaseArtifacts(root, out, testReleaseVersion, deterministicTestBuilder); err == nil {
		t.Fatal("releaseArtifacts replaced a non-regular destination")
	}
	info, err := os.Stat(blocked)
	if err != nil || !info.IsDir() {
		t.Fatalf("blocked destination changed: info=%v err=%v", info, err)
	}
}

func TestSafeReleaseOutputRejectsSymlink(t *testing.T) {
	root := releaseFixture(t)
	targetDir := filepath.Join(root, "real-dist")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "dist-link")
	if err := os.Symlink(targetDir, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := safeReleaseOutput(root, link); err == nil {
		t.Fatal("safeReleaseOutput accepted a symbolic link")
	}
}

func TestSetEnvironmentReplacesNamesCaseInsensitively(t *testing.T) {
	base := []string{"Path=/bin", "GOOS=old", "LANG=C"}
	got := setEnvironment(base, map[string]string{"GOOS": "linux", "GOARCH": "amd64"})
	want := []string{"Path=/bin", "LANG=C", "GOARCH=amd64", "GOOS=linux"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("setEnvironment() = %v, want %v", got, want)
	}
}

func TestValidateSemanticVersionRejectsLeadingZeroPrerelease(t *testing.T) {
	if err := validateSemanticVersion("0.2.0-rc.01"); err == nil {
		t.Fatal("validateSemanticVersion accepted a numeric prerelease identifier with a leading zero")
	}
}

func releaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range releaseFiles {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func deterministicTestBuilder(_ string, target target, output string) error {
	return os.WriteFile(output, []byte("pageskein "+target.OS+"/"+target.Arch+"\n"), 0o755)
}

func compareReleaseDirsIgnoring(first, second string, ignored ...string) error {
	ignoredNames := make(map[string]bool, len(ignored))
	for _, name := range ignored {
		ignoredNames[name] = true
	}
	firstEntries, err := os.ReadDir(first)
	if err != nil {
		return err
	}
	for _, entry := range firstEntries {
		if ignoredNames[entry.Name()] {
			continue
		}
		firstSum, err := fileSHA256(filepath.Join(first, entry.Name()))
		if err != nil {
			return err
		}
		secondSum, err := fileSHA256(filepath.Join(second, entry.Name()))
		if err != nil {
			return err
		}
		if firstSum != secondSum {
			return errors.New("artifact differs: " + entry.Name())
		}
	}
	return nil
}
