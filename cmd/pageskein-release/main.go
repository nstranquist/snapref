// Command pageskein-release builds deterministic local release archives.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/processtree"
)

type target struct {
	OS, Arch string
}

var targets = []target{{OS: "darwin", Arch: "arm64"}, {OS: "linux", Arch: "amd64"}, {OS: "windows", Arch: "amd64"}}

var releaseFiles = []string{
	"CHANGELOG.md",
	"LICENSE",
	"README.md",
	"SECURITY.md",
	"THIRD_PARTY_NOTICES.md",
	"VERSION",
	"docs/VERSIONING.md",
	"schemas/script-run-v2.schema.json",
	"schemas/telemetry-v2.schema.json",
}

func main() {
	out := flag.String("out", "dist", "release output directory")
	verifyRepro := flag.Bool("verify-repro", false, "build twice and compare without publishing")
	flag.Parse()
	var err error
	if *verifyRepro {
		err = verifyReproducibleRelease()
	} else {
		err = release(*out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pageskein-release:", err)
		os.Exit(1)
	}
}

func release(out string) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
	version, err := validateReleaseVersion(root)
	if err != nil {
		return err
	}
	return releaseArtifacts(root, out, version, build)
}

type buildFunc func(root string, target target, output string) error

func releaseArtifacts(root, out, version string, buildBinary buildFunc) error {
	if err := validateSemanticVersion(version); err != nil {
		return err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
	for _, name := range releaseFiles {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			return fmt.Errorf("release file %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("release file %s is not a regular file", name)
		}
	}
	out, err = safeReleaseOutput(root, out)
	if err != nil {
		return err
	}

	stage, err := os.MkdirTemp(filepath.Dir(out), "."+filepath.Base(out)+".pageskein-release-")
	if err != nil {
		return fmt.Errorf("create release staging directory: %w", err)
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			_ = os.RemoveAll(stage)
		}
	}()
	buildDir := filepath.Join(stage, ".build")
	if err := os.Mkdir(buildDir, 0o700); err != nil {
		return fmt.Errorf("create release build directory: %w", err)
	}

	checksums := make(map[string]string, len(targets))
	for _, t := range targets {
		stem := fmt.Sprintf("pageskein-%s-%s-%s", version, t.OS, t.Arch)
		binaryName := "pageskein"
		if t.OS == "windows" {
			binaryName += ".exe"
		}
		binary := filepath.Join(buildDir, binaryName)
		if err := buildBinary(root, t, binary); err != nil {
			return err
		}
		archivePath := filepath.Join(stage, stem+".tar.gz")
		if t.OS == "windows" {
			archivePath = filepath.Join(stage, stem+".zip")
			err = writeZip(root, archivePath, stem, binary, binaryName)
		} else {
			err = writeTarGz(root, archivePath, stem, binary, binaryName)
		}
		if err != nil {
			return err
		}
		sum, err := fileSHA256(archivePath)
		if err != nil {
			return err
		}
		checksums[filepath.Base(archivePath)] = sum
	}

	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	var body strings.Builder
	for _, name := range names {
		fmt.Fprintf(&body, "%s  %s\n", checksums[name], name)
	}
	if err := os.WriteFile(filepath.Join(stage, "SHA256SUMS"), []byte(body.String()), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(buildDir); err != nil {
		return fmt.Errorf("remove release build directory: %w", err)
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		return fmt.Errorf("set release directory mode: %w", err)
	}
	consumed, err := publishRelease(stage, out, append(names, "SHA256SUMS"))
	if consumed {
		stageOwned = false
	}
	return err
}

func validateSemanticVersion(version string) error {
	semver := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	match := semver.FindStringSubmatch(version)
	if match == nil || strings.ContainsAny(version, `/\\`) {
		return fmt.Errorf("VERSION %q is not Semantic Versioning 2.0.0", version)
	}
	for _, identifier := range strings.Split(match[4], ".") {
		if len(identifier) > 1 && identifier[0] == '0' {
			allDigits := true
			for _, r := range identifier {
				allDigits = allDigits && r >= '0' && r <= '9'
			}
			if allDigits {
				return errors.New("VERSION has a numeric prerelease identifier with a leading zero")
			}
		}
	}
	return nil
}

func validateReleaseVersion(root string) (string, error) {
	versionBody, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "", fmt.Errorf("read VERSION: %w", err)
	}
	version := strings.TrimSpace(string(versionBody))
	if err := validateSemanticVersion(version); err != nil {
		return "", err
	}
	packageRaw, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", fmt.Errorf("read package.json: %w", err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageRaw, &manifest); err != nil {
		return "", fmt.Errorf("parse package.json: %w", err)
	}
	if manifest.Version != version {
		return "", fmt.Errorf("version mirror drift: VERSION=%s package.json=%s", version, manifest.Version)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := processtree.CommandContext(ctx, "go", "run", "-mod=readonly", "./cmd/pageskein", "version")
	command.Dir = root
	command.Env = releaseGoEnvironment(nil)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read CLI version: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if cliVersion := strings.TrimSpace(string(output)); cliVersion != version {
		return "", fmt.Errorf("version mirror drift: VERSION=%s CLI=%s", version, cliVersion)
	}
	return version, nil
}

func build(root string, t target, output string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	command := processtree.CommandContext(ctx, "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", output, "./cmd/pageskein")
	command.Dir = root
	command.Env = releaseGoEnvironment(map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      t.Arch,
		"GOOS":        t.OS,
	})
	if commandOutput, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s/%s: %w: %s", t.OS, t.Arch, err, strings.TrimSpace(string(commandOutput)))
	}
	return nil
}

func archiveEntries(root, binary, binaryName string) []archiveEntry {
	entries := []archiveEntry{{name: binaryName, path: binary, mode: 0o755}}
	for _, name := range releaseFiles {
		entries = append(entries, archiveEntry{name: name, path: filepath.Join(root, name), mode: 0o644})
	}
	return entries
}

type archiveEntry struct {
	name, path string
	mode       os.FileMode
}

var archiveTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

func safeReleaseOutput(root, out string) (string, error) {
	if strings.TrimSpace(out) == "" {
		return "", errors.New("release output directory is empty")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve source root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve source root symlinks: %w", err)
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolve release output: %w", err)
	}
	out = filepath.Clean(out)

	info, err := os.Lstat(out)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("release output %s is a symbolic link", out)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("release output %s is not a directory", out)
		}
		out, err = filepath.EvalSymlinks(out)
		if err != nil {
			return "", fmt.Errorf("resolve release output symlinks: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		parent := filepath.Dir(out)
		parentInfo, statErr := os.Stat(parent)
		if statErr != nil {
			return "", fmt.Errorf("inspect release output parent %s: %w", parent, statErr)
		}
		if !parentInfo.IsDir() {
			return "", fmt.Errorf("release output parent %s is not a directory", parent)
		}
		parent, err = filepath.EvalSymlinks(parent)
		if err != nil {
			return "", fmt.Errorf("resolve release output parent symlinks: %w", err)
		}
		out = filepath.Join(parent, filepath.Base(out))
	default:
		return "", fmt.Errorf("inspect release output %s: %w", out, err)
	}

	volumeRoot := filepath.Clean(filepath.VolumeName(out) + string(filepath.Separator))
	if pathsEqual(out, volumeRoot) {
		return "", fmt.Errorf("release output %s is a filesystem root", out)
	}
	if containsPath(out, root) {
		return "", fmt.Errorf("release output %s is the source root or one of its ancestors", out)
	}
	return out, nil
}

func pathsEqual(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func containsPath(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func publishRelease(stage, out string, names []string) (bool, error) {
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(stage, name))
		if err != nil {
			return false, fmt.Errorf("inspect staged artifact %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("staged artifact %s is not a regular file", name)
		}
	}

	outInfo, err := os.Lstat(out)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stage, out); err != nil {
			return false, fmt.Errorf("publish release directory: %w", err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect release output before publication: %w", err)
	}
	if outInfo.Mode()&os.ModeSymlink != 0 || !outInfo.IsDir() {
		return false, fmt.Errorf("release output %s changed after validation", out)
	}

	existing := make([]string, 0, len(names))
	for _, name := range names {
		destination := filepath.Join(out, name)
		info, err := os.Lstat(destination)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect existing artifact %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("refuse to replace non-regular release artifact %s", destination)
		}
		existing = append(existing, name)
	}

	backup := filepath.Join(stage, ".previous")
	if err := os.Mkdir(backup, 0o700); err != nil {
		return false, fmt.Errorf("create release rollback directory: %w", err)
	}
	movedExisting := make([]string, 0, len(existing))
	for _, name := range existing {
		if err := os.Rename(filepath.Join(out, name), filepath.Join(backup, name)); err != nil {
			rollbackErr := restoreRelease(out, backup, movedExisting, nil)
			return false, errors.Join(fmt.Errorf("stage existing artifact %s for replacement: %w", name, err), rollbackErr)
		}
		movedExisting = append(movedExisting, name)
	}

	installed := make([]string, 0, len(names))
	for _, name := range names {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(out, name)); err != nil {
			rollbackErr := restoreRelease(out, backup, movedExisting, installed)
			return false, errors.Join(fmt.Errorf("publish artifact %s: %w", name, err), rollbackErr)
		}
		installed = append(installed, name)
	}
	return false, nil
}

func restoreRelease(out, backup string, movedExisting, installed []string) error {
	var errs []error
	for i := len(installed) - 1; i >= 0; i-- {
		name := installed[i]
		if err := os.Remove(filepath.Join(out, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove partially published artifact %s: %w", name, err))
		}
	}
	for i := len(movedExisting) - 1; i >= 0; i-- {
		name := movedExisting[i]
		if err := os.Rename(filepath.Join(backup, name), filepath.Join(out, name)); err != nil {
			errs = append(errs, fmt.Errorf("restore previous artifact %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func verifyReproducibleRelease() error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
	version, err := validateReleaseVersion(root)
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(root), ".pageskein-repro-")
	if err != nil {
		return fmt.Errorf("create reproducibility workspace: %w", err)
	}
	defer os.RemoveAll(temp)
	first := filepath.Join(temp, "first")
	second := filepath.Join(temp, "second")
	if err := releaseArtifacts(root, first, version, build); err != nil {
		return fmt.Errorf("first reproducibility build: %w", err)
	}
	if err := releaseArtifacts(root, second, version, build); err != nil {
		return fmt.Errorf("second reproducibility build: %w", err)
	}
	if err := compareReleaseDirs(first, second); err != nil {
		return err
	}
	fmt.Printf("PageSkein %s release artifacts are byte-reproducible\n", version)
	return nil
}

func compareReleaseDirs(first, second string) error {
	firstEntries, err := os.ReadDir(first)
	if err != nil {
		return err
	}
	secondEntries, err := os.ReadDir(second)
	if err != nil {
		return err
	}
	if len(firstEntries) != len(secondEntries) {
		return fmt.Errorf("release artifact count differs: first=%d second=%d", len(firstEntries), len(secondEntries))
	}
	for i, firstEntry := range firstEntries {
		secondEntry := secondEntries[i]
		if firstEntry.Name() != secondEntry.Name() {
			return fmt.Errorf("release artifact names differ: first=%s second=%s", firstEntry.Name(), secondEntry.Name())
		}
		if firstEntry.IsDir() || secondEntry.IsDir() {
			return fmt.Errorf("release artifact %s must be a file", firstEntry.Name())
		}
		firstSum, err := fileSHA256(filepath.Join(first, firstEntry.Name()))
		if err != nil {
			return err
		}
		secondSum, err := fileSHA256(filepath.Join(second, secondEntry.Name()))
		if err != nil {
			return err
		}
		if firstSum != secondSum {
			return fmt.Errorf("release artifact %s is not reproducible: first=%s second=%s", firstEntry.Name(), firstSum, secondSum)
		}
	}
	return nil
}

func goCommandEnvironment() []string {
	return processtree.CurrentMinimalEnvironment(
		"APPDATA", "CC", "CXX", "GOENV", "GOCACHE", "GOMODCACHE", "GONOPROXY",
		"GONOSUMDB", "GOPATH", "GOPRIVATE", "GOPROXY", "GOROOT", "GOSUMDB",
		"GOTOOLCHAIN", "HOME", "LOCALAPPDATA", "USERPROFILE", "XDG_CACHE_HOME",
	)
}

func releaseGoEnvironment(additional map[string]string) []string {
	overrides := map[string]string{
		"GOENV":       "off",
		"GOPROXY":     "off",
		"GOTOOLCHAIN": "local",
		"GOWORK":      "off",
	}
	for name, value := range additional {
		overrides[name] = value
	}
	return setEnvironment(goCommandEnvironment(), overrides)
}

func setEnvironment(base []string, overrides map[string]string) []string {
	result := make([]string, 0, len(base)+len(overrides))
	for _, pair := range base {
		name, _, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if _, overridden := overrides[strings.ToUpper(name)]; !overridden {
			result = append(result, pair)
		}
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, name+"="+overrides[name])
	}
	return result
}

func writeZip(sourceRoot, path, root, binary, binaryName string) (returnErr error) {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	writer := zip.NewWriter(file)
	defer func() {
		if err := writer.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	for _, entry := range archiveEntries(sourceRoot, binary, binaryName) {
		body, err := os.ReadFile(entry.path)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: root + "/" + entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		header.SetModTime(archiveTime)
		out, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := out.Write(body); err != nil {
			return err
		}
	}
	return nil
}

func writeTarGz(sourceRoot, path, root, binary, binaryName string) (returnErr error) {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	gz := gzip.NewWriter(file)
	gz.Header.ModTime = archiveTime
	gz.Header.OS = 255
	defer func() {
		if err := gz.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	tw := tar.NewWriter(gz)
	defer func() {
		if err := tw.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	for _, entry := range archiveEntries(sourceRoot, binary, binaryName) {
		info, err := os.Stat(entry.path)
		if err != nil {
			return err
		}
		header := &tar.Header{
			Name: root + "/" + entry.name, Mode: int64(entry.mode.Perm()), Size: info.Size(),
			ModTime: archiveTime, AccessTime: archiveTime, ChangeTime: archiveTime,
			Uid: 0, Gid: 0, Uname: "", Gname: "", Format: tar.FormatPAX,
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		input, err := os.Open(entry.path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
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
