package productshot

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const ManifestSchema = "nicos.browser.shot-pack.v1"

// Artifact is one file in a pack.
type Artifact struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

// Manifest is the machine-readable pack receipt.
type Manifest struct {
	Schema     string     `json:"schema"`
	SourceURL  string     `json:"source_url,omitempty"`
	Title      string     `json:"title,omitempty"`
	Caption    string     `json:"caption,omitempty"`
	Theme      string     `json:"theme,omitempty"`
	Presets    []string   `json:"presets"`
	CapturedAt string     `json:"captured_at"`
	Artifacts  []Artifact `json:"artifacts"`
	Readme     string     `json:"readme_snippet"`
}

// Pack is the on-disk result of a shot or record.
type Pack struct {
	Dir      string
	Manifest Manifest
}

// Sources are the raw captures to frame.
type Sources struct {
	Desktop image.Image
	Mobile  image.Image
	URL     string
}

// WritePack frames sources into every requested preset and writes a directory
// with raw PNGs, framed PNGs, manifest.json, and README.md.
func WritePack(dir string, src Sources, presets []Preset, opt Options) (Pack, error) {
	if strings.TrimSpace(dir) == "" {
		return Pack{}, fmt.Errorf("output directory is required")
	}
	if src.Desktop == nil && src.Mobile == nil {
		return Pack{}, fmt.Errorf("at least one source image is required")
	}
	presets = ExpandPresets(presets...)
	if opt.URL == "" {
		opt.URL = src.URL
	}
	if err := os.MkdirAll(filepath.Join(dir, "raw"), 0o755); err != nil {
		return Pack{}, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "framed"), 0o755); err != nil {
		return Pack{}, err
	}

	man := Manifest{
		Schema:     ManifestSchema,
		SourceURL:  src.URL,
		Title:      opt.Title,
		Caption:    opt.Caption,
		Theme:      string(opt.Theme),
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, p := range presets {
		man.Presets = append(man.Presets, string(p))
	}

	if src.Desktop != nil {
		rel := filepath.Join("raw", "desktop.png")
		art, err := writePNG(filepath.Join(dir, rel), src.Desktop, "raw.desktop", KindRaw)
		if err != nil {
			return Pack{}, err
		}
		art.Path = rel
		man.Artifacts = append(man.Artifacts, art)
	}
	if src.Mobile != nil {
		rel := filepath.Join("raw", "mobile.png")
		art, err := writePNG(filepath.Join(dir, rel), src.Mobile, "raw.mobile", KindRaw)
		if err != nil {
			return Pack{}, err
		}
		art.Path = rel
		man.Artifacts = append(man.Artifacts, art)
	}

	for _, preset := range presets {
		if preset == PresetRaw {
			continue
		}
		for _, format := range StillFormats(preset, src.Mobile != nil) {
			srcImg := src.Desktop
			if format.Phone && src.Mobile != nil {
				srcImg = src.Mobile
			}
			if srcImg == nil {
				srcImg = src.Mobile
			}
			if srcImg == nil {
				continue
			}
			framed := Frame(srcImg, format, opt)
			rel := filepath.Join("framed", format.Name+".png")
			art, err := writePNG(filepath.Join(dir, rel), framed, format.Role, format.Kind)
			if err != nil {
				return Pack{}, err
			}
			art.Path = rel
			man.Artifacts = append(man.Artifacts, art)
		}
	}

	man.Readme = ReadmeSnippet(man)
	if err := writeJSON(filepath.Join(dir, "manifest.json"), man); err != nil {
		return Pack{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(man.Readme), 0o644); err != nil {
		return Pack{}, err
	}
	return Pack{Dir: dir, Manifest: man}, nil
}

// AppendVideo records encoded video files on an existing pack and rewrites
// the manifest + README so the snippet includes the motion files.
func AppendVideo(dir string, artifacts []Artifact) (Pack, error) {
	man, err := readManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Pack{}, err
	}
	man.Artifacts = append(man.Artifacts, artifacts...)
	man.Readme = ReadmeSnippet(man)
	if err := writeJSON(filepath.Join(dir, "manifest.json"), man); err != nil {
		return Pack{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(man.Readme), 0o644); err != nil {
		return Pack{}, err
	}
	return Pack{Dir: dir, Manifest: man}, nil
}

func writePNG(path string, img image.Image, role string, kind Kind) (Artifact, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-shot-*.png")
	if err != nil {
		return Artifact{}, err
	}
	tmpName := tmp.Name()
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(tmp, img); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return Artifact{}, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return Artifact{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return Artifact{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Artifact{}, err
	}
	b := img.Bounds()
	return Artifact{
		Role:   role,
		Path:   path,
		Width:  b.Dx(),
		Height: b.Dy(),
		Bytes:  int(info.Size()),
		Kind:   string(kind),
	}, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func readManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var man Manifest
	if err := json.Unmarshal(data, &man); err != nil {
		return Manifest{}, err
	}
	return man, nil
}

// DefaultDir names a cwd-relative pack directory from the source host and preset.
func DefaultDir(sourceURL string, presets []Preset) string {
	host := "shot"
	if sourceURL != "" {
		host = sanitizeName(hostFromURL(sourceURL))
	}
	name := "github-readme"
	expanded := ExpandPresets(presets...)
	if len(expanded) == 1 {
		name = string(expanded[0])
	} else if len(expanded) > 1 {
		name = "all"
	}
	return filepath.Join("ndev-shots", fmt.Sprintf("%s-%s-%s", host, name, time.Now().UTC().Format("20060102-150405")))
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	host, _, _ := strings.Cut(raw, "/")
	host, _, _ = strings.Cut(host, ":")
	if host == "" {
		return "local"
	}
	return host
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "shot"
	}
	return out
}
