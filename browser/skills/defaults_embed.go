package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// `all:` prefix is required so the embed includes `defaults/_helpers/`
// — by default Go's embed skips files/dirs whose names start with
// underscore or dot. Without `all:` the helper auto-prepend has
// nothing to seed.
//
//go:embed all:defaults
var defaultsFS embed.FS

// EnsureSeeded copies any missing files from the embedded defaults
// tree into r.Path. Idempotent: never overwrites existing files, so
// user edits and added skills are preserved across upgrades.
//
// Called automatically by DefaultRoot. Tests that build a SkillsRoot
// against a temp dir can either call EnsureSeeded() to get the same
// starting state as a fresh user, or skip it for a fully synthetic
// layout.
func (r SkillsRoot) EnsureSeeded() error {
	return r.seedDefaults(false)
}

// RefreshDefaults overwrites files owned by the embedded defaults while
// preserving user-added domains and files. This is the explicit recovery path
// for installations whose seeded defaults predate extractor or documentation
// fixes; normal startup continues to use EnsureSeeded and never overwrites.
func (r SkillsRoot) RefreshDefaults() error {
	return r.seedDefaults(true)
}

func (r SkillsRoot) seedDefaults(overwrite bool) error {
	if err := os.MkdirAll(r.Path, 0o755); err != nil {
		return fmt.Errorf("mkdir skills root %q: %w", r.Path, err)
	}
	return fs.WalkDir(defaultsFS, "defaults", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "defaults")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return nil
		}
		dst := filepath.Join(r.Path, rel)
		if d.IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", dst, err)
			}
			return nil
		}
		_, statErr := os.Stat(dst)
		if statErr == nil {
			if !overwrite {
				return nil
			}
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat %q: %w", dst, statErr)
		}
		data, err := defaultsFS.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %q: %w", p, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write %q: %w", dst, err)
		}
		return nil
	})
}

// SeedDefaultsInto is a convenience that creates a SkillsRoot at path,
// runs EnsureSeeded, and returns it. Used by `snapref skills init`
// and as the public hook for tests that want a populated temp root.
func SeedDefaultsInto(path string) (SkillsRoot, error) {
	r := SkillsRoot{Path: path}
	if err := r.EnsureSeeded(); err != nil {
		return SkillsRoot{}, err
	}
	return r, nil
}
