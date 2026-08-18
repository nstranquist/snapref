package productshot

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
)

const (
	productHuntThumbW   = 240
	productHuntThumbH   = 240
	productHuntGalleryW = 1270
	productHuntGalleryH = 760
	productHuntMaxBytes = 5 << 20
)

// Slot is one Product Hunt upload field.
type Slot string

const (
	SlotThumbnail Slot = "thumbnail"
	SlotGallery   Slot = "gallery"
	SlotVideo     Slot = "video"
	SlotGIF       Slot = "gif"
)

// CheckedAsset is one pack file mapped onto a PH slot, with size proof.
type CheckedAsset struct {
	Slot   Slot   `json:"slot"`
	Role   string `json:"role"`
	Path   string `json:"path"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// ProductHuntKit is the fail-closed upload checklist for a shot pack.
type ProductHuntKit struct {
	PackDir     string          `json:"pack_dir"`
	Schema      string          `json:"schema,omitempty"`
	Title       string          `json:"title,omitempty"`
	SourceURL   string          `json:"source_url,omitempty"`
	ComposerURL string          `json:"composer_url"`
	Assets      []CheckedAsset  `json:"assets"`
	Ready       bool            `json:"ready"`
	Errors      []string        `json:"errors,omitempty"`
}

// LoadPack reads a shot-pack directory written by WritePack.
func LoadPack(dir string) (Pack, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return Pack{}, fmt.Errorf("pack directory is required")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Pack{}, fmt.Errorf("read pack manifest: %w", err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Pack{}, fmt.Errorf("decode pack manifest: %w", err)
	}
	if man.Schema != "" && man.Schema != ManifestSchema {
		return Pack{}, fmt.Errorf("unsupported pack schema %q (want %s)", man.Schema, ManifestSchema)
	}
	return Pack{Dir: dir, Manifest: man}, nil
}

// InspectProductHunt maps a pack onto Product Hunt's thumbnail + gallery
// (1270×760, two required) + optional video. The public PH GraphQL token
// cannot upload; this kit is what you hand to https://www.producthunt.com/posts/new.
func InspectProductHunt(dir string) (ProductHuntKit, error) {
	pack, err := LoadPack(dir)
	if err != nil {
		return ProductHuntKit{}, err
	}
	kit := ProductHuntKit{
		PackDir:     pack.Dir,
		Schema:      pack.Manifest.Schema,
		Title:       pack.Manifest.Title,
		SourceURL:   pack.Manifest.SourceURL,
		ComposerURL: "https://www.producthunt.com/posts/new",
	}
	abs := func(rel string) string {
		if filepath.IsAbs(rel) {
			return rel
		}
		return filepath.Join(pack.Dir, rel)
	}
	var galleries []CheckedAsset
	for _, a := range pack.Manifest.Artifacts {
		path := abs(a.Path)
		switch {
		case strings.Contains(a.Role, "thumbnail"):
			kit.Assets = append(kit.Assets, checkStill(SlotThumbnail, a.Role, path, productHuntThumbW, productHuntThumbH))
		case strings.Contains(a.Role, "gallery"):
			galleries = append(galleries, checkStill(SlotGallery, a.Role, path, productHuntGalleryW, productHuntGalleryH))
		case strings.HasSuffix(a.Path, ".mp4") || strings.HasSuffix(a.Role, ".mp4"):
			kit.Assets = append(kit.Assets, checkFile(SlotVideo, a.Role, path))
		case strings.HasSuffix(a.Path, ".gif") || strings.HasSuffix(a.Role, ".gif"):
			kit.Assets = append(kit.Assets, checkFile(SlotGIF, a.Role, path))
		}
	}
	kit.Assets = append(kit.Assets, galleries...)
	var thumbs, okGallery int
	for _, a := range kit.Assets {
		if !a.OK && a.Error != "" {
			kit.Errors = append(kit.Errors, a.Error)
		}
		if a.Slot == SlotThumbnail && a.OK {
			thumbs++
		}
		if a.Slot == SlotGallery && a.OK {
			okGallery++
		}
	}
	if thumbs == 0 {
		kit.Errors = append(kit.Errors, "missing 240×240 thumbnail (shot --preset product-hunt)")
	}
	if okGallery < 2 {
		kit.Errors = append(kit.Errors, fmt.Sprintf("Product Hunt gallery needs 2+ images at 1270×760 (have %d)", okGallery))
	}
	kit.Ready = len(kit.Errors) == 0
	return kit, nil
}

func checkStill(slot Slot, role, path string, wantW, wantH int) CheckedAsset {
	a := checkFile(slot, role, path)
	if !a.OK {
		return a
	}
	f, err := os.Open(path)
	if err != nil {
		a.OK = false
		a.Error = err.Error()
		return a
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		a.OK = false
		a.Error = fmt.Sprintf("%s: decode: %v", path, err)
		return a
	}
	a.Width, a.Height = cfg.Width, cfg.Height
	if cfg.Width != wantW || cfg.Height != wantH {
		a.OK = false
		a.Error = fmt.Sprintf("%s: got %dx%d, want %dx%d", path, cfg.Width, cfg.Height, wantW, wantH)
	}
	if a.Bytes > productHuntMaxBytes {
		a.OK = false
		a.Error = fmt.Sprintf("%s: %d bytes exceeds 5MB gallery cap", path, a.Bytes)
	}
	return a
}

func checkFile(slot Slot, role, path string) CheckedAsset {
	a := CheckedAsset{Slot: slot, Role: role, Path: path, OK: true}
	info, err := os.Stat(path)
	if err != nil {
		a.OK = false
		a.Error = fmt.Sprintf("%s: %v", path, err)
		return a
	}
	a.Bytes = int(info.Size())
	if a.Bytes == 0 {
		a.OK = false
		a.Error = fmt.Sprintf("%s: empty file", path)
	}
	return a
}
