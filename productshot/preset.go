// Package productshot turns raw browser captures into publish-ready stills
// and recordings for GitHub READMEs, Product Hunt, and social cards.
package productshot

import (
	"fmt"
	"strings"
)

// Preset is a named publish target. Each preset expands to one or more
// exact-size artifacts plus a markdown snippet.
type Preset string

const (
	PresetGitHubReadme Preset = "github-readme"
	PresetProductHunt  Preset = "product-hunt"
	PresetOG           Preset = "og"
	PresetTwitter      Preset = "twitter"
	PresetRaw          Preset = "raw"
	PresetAll          Preset = "all"
)

// Theme paints the canvas behind the window chrome.
type Theme string

const (
	ThemeLight Theme = "light"
	ThemeDark  Theme = "dark"
)

// Kind classifies one framed artifact.
type Kind string

const (
	KindHero      Kind = "hero"
	KindGallery   Kind = "gallery"
	KindThumbnail Kind = "thumbnail"
	KindOG        Kind = "og"
	KindSocial    Kind = "social"
	KindRaw       Kind = "raw"
)

// Format is one exact output canvas.
type Format struct {
	Name   string
	Role   string
	Kind   Kind
	Width  int
	Height int
	// Phone uses a taller window and tighter side padding so a mobile
	// capture reads as a device shot instead of a letterboxed desktop.
	Phone bool
}

// VideoSpec is the encode target for a recording pack.
type VideoSpec struct {
	Name       string
	Role       string
	Width      int
	Height     int
	FPS        int
	InputFPS   float64 // capture rate; 0 means use FPS (output resampled)
	MaxSeconds int
	GIFWidth   int
	WantMP4    bool
	WantGIF    bool
}

// CaptureFPS is frames / duration. Falls back to the screenshot-loop rate
// (1000/120 ms) when the recorder did not report a usable duration.
func CaptureFPS(frames int, durationMS int64) float64 {
	if frames <= 0 {
		return 1000.0 / 120.0
	}
	if durationMS <= 0 {
		return 1000.0 / 120.0
	}
	return float64(frames) / (float64(durationMS) / 1000.0)
}

// WithCapture stamps the measured capture rate so encode duration matches
// wall-clock recording time instead of the output preset FPS.
func (s VideoSpec) WithCapture(frames int, durationMS int64) VideoSpec {
	s.InputFPS = CaptureFPS(frames, durationMS)
	return s
}

// Options control chrome, copy, and which source frames to include.
type Options struct {
	Title         string
	Caption       string
	URL           string
	Theme         Theme
	IncludeMobile bool
	NoMobile      bool
}

var desktopCapture = struct{ W, H int }{1440, 900}
var mobileCapture = struct{ W, H int }{390, 844}

// DesktopCapture is the viewport used for product stills (fold, not full page).
func DesktopCapture() (int, int) { return desktopCapture.W, desktopCapture.H }

// MobileCapture is the iPhone-class viewport used for a second gallery image.
func MobileCapture() (int, int) { return mobileCapture.W, mobileCapture.H }

// ParsePreset accepts a single preset name.
func ParsePreset(raw string) (Preset, error) {
	p := Preset(strings.ToLower(strings.TrimSpace(raw)))
	switch p {
	case PresetGitHubReadme, PresetProductHunt, PresetOG, PresetTwitter, PresetRaw, PresetAll:
		return p, nil
	case "":
		return PresetGitHubReadme, nil
	default:
		return "", fmt.Errorf("unknown preset %q (want github-readme, product-hunt, og, twitter, raw, or all)", raw)
	}
}

// ParseTheme accepts light or dark. Empty is light.
func ParseTheme(raw string) (Theme, error) {
	t := Theme(strings.ToLower(strings.TrimSpace(raw)))
	switch t {
	case "", ThemeLight:
		return ThemeLight, nil
	case ThemeDark:
		return ThemeDark, nil
	default:
		return "", fmt.Errorf("unknown theme %q (want light or dark)", raw)
	}
}

// ExpandPresets flattens "all" and de-duplicates.
func ExpandPresets(presets ...Preset) []Preset {
	seen := map[Preset]bool{}
	out := make([]Preset, 0, 4)
	add := func(p Preset) {
		if p == PresetAll {
			for _, one := range []Preset{PresetGitHubReadme, PresetProductHunt, PresetOG, PresetTwitter} {
				if !seen[one] {
					seen[one] = true
					out = append(out, one)
				}
			}
			return
		}
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(presets) == 0 {
		add(PresetGitHubReadme)
		return out
	}
	for _, p := range presets {
		add(p)
	}
	return out
}

// StillFormats returns the framed canvases for a preset. Product Hunt always
// wants at least two gallery images when a mobile source is available.
func StillFormats(preset Preset, includeMobile bool) []Format {
	switch preset {
	case PresetGitHubReadme:
		return []Format{{
			Name: "github-readme", Role: "github-readme.hero", Kind: KindHero,
			Width: 1280, Height: 720,
		}}
	case PresetProductHunt:
		out := []Format{{
			Name: "product-hunt-01", Role: "product-hunt.gallery", Kind: KindGallery,
			Width: 1270, Height: 760,
		}}
		if includeMobile {
			out = append(out, Format{
				Name: "product-hunt-02", Role: "product-hunt.gallery", Kind: KindGallery,
				Width: 1270, Height: 760, Phone: true,
			})
		}
		out = append(out, Format{
			Name: "product-hunt-thumb", Role: "product-hunt.thumbnail", Kind: KindThumbnail,
			Width: 240, Height: 240,
		})
		return out
	case PresetOG:
		return []Format{{
			Name: "og", Role: "og.card", Kind: KindOG,
			Width: 1200, Height: 630,
		}}
	case PresetTwitter:
		return []Format{{
			Name: "twitter", Role: "twitter.card", Kind: KindSocial,
			Width: 1600, Height: 900,
		}}
	case PresetRaw:
		return nil
	default:
		return nil
	}
}

// VideoSpecs returns encode targets for a recording pack.
// PresetAll expands to the github-readme and product-hunt clips.
func VideoSpecs(preset Preset) []VideoSpec {
	switch preset {
	case PresetAll:
		var out []VideoSpec
		out = append(out, VideoSpecs(PresetGitHubReadme)...)
		out = append(out, VideoSpecs(PresetProductHunt)...)
		return out
	case PresetGitHubReadme:
		return []VideoSpec{{
			Name: "github-readme", Role: "github-readme.demo",
			Width: 1280, Height: 720, FPS: 12, MaxSeconds: 12,
			GIFWidth: 800, WantMP4: true, WantGIF: true,
		}}
	case PresetProductHunt:
		return []VideoSpec{{
			Name: "product-hunt", Role: "product-hunt.demo",
			Width: 1280, Height: 720, FPS: 16, MaxSeconds: 30,
			GIFWidth: 800, WantMP4: true, WantGIF: true,
		}}
	case PresetOG, PresetTwitter:
		return []VideoSpec{{
			Name: string(preset), Role: string(preset) + ".demo",
			Width: 1280, Height: 720, FPS: 12, MaxSeconds: 15,
			WantMP4: true,
		}}
	default:
		return nil
	}
}

// WantsMobile reports whether this option set should capture a phone viewport.
func (o Options) WantsMobile(presets []Preset) bool {
	if o.NoMobile {
		return false
	}
	if o.IncludeMobile {
		return true
	}
	for _, p := range ExpandPresets(presets...) {
		if p == PresetProductHunt {
			return true
		}
	}
	return false
}

// KnownPresets is the operator-facing list.
func KnownPresets() []string {
	return []string{
		string(PresetGitHubReadme),
		string(PresetProductHunt),
		string(PresetOG),
		string(PresetTwitter),
		string(PresetRaw),
		string(PresetAll),
	}
}
