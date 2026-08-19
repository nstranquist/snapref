package productshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDirUsesSnapRefPrefix(t *testing.T) {
	dir := DefaultDir("https://example.com/app", []Preset{PresetGitHubReadme})
	if !strings.HasPrefix(dir, "snapref-shots"+string(os.PathSeparator)) && !strings.HasPrefix(dir, "snapref-shots/") {
		t.Fatalf("DefaultDir = %q, want snapref-shots/...", dir)
	}
	if strings.Contains(dir, "ndev-shots") {
		t.Fatalf("DefaultDir leaked factory path: %q", dir)
	}
}

func TestWritePackGitHubReadme(t *testing.T) {
	dir := t.TempDir()
	pack, err := WritePack(dir, Sources{
		Desktop: solid(640, 400, nrgba(20, 80, 180, 255)),
		URL:     "https://example.com/app",
	}, []Preset{PresetGitHubReadme}, Options{Title: "Example", Theme: ThemeLight})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"manifest.json",
		"README.md",
		filepath.Join("raw", "desktop.png"),
		filepath.Join("framed", "github-readme.png"),
	}
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	if pack.Manifest.Schema != ManifestSchema {
		t.Fatalf("schema = %q", pack.Manifest.Schema)
	}
	if !strings.Contains(pack.Manifest.Readme, "![Example](framed/github-readme.png)") {
		t.Fatalf("readme snippet missing hero embed:\n%s", pack.Manifest.Readme)
	}
	framed, err := DecodeImageFile(filepath.Join(dir, "framed", "github-readme.png"))
	if err != nil {
		t.Fatal(err)
	}
	if framed.Bounds().Dx() != 1280 || framed.Bounds().Dy() != 720 {
		t.Fatalf("framed size = %v", framed.Bounds())
	}
}

func TestWritePackProductHuntIncludesMobileAndThumb(t *testing.T) {
	dir := t.TempDir()
	_, err := WritePack(dir, Sources{
		Desktop: solid(640, 400, nrgba(20, 80, 180, 255)),
		Mobile:  solid(200, 400, nrgba(20, 160, 80, 255)),
		URL:     "https://example.com",
	}, []Preset{PresetProductHunt}, Options{Title: "Hunt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join("framed", "product-hunt-01.png"),
		filepath.Join("framed", "product-hunt-02.png"),
		filepath.Join("framed", "product-hunt-thumb.png"),
		filepath.Join("raw", "mobile.png"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	thumb, err := DecodeImageFile(filepath.Join(dir, "framed", "product-hunt-thumb.png"))
	if err != nil {
		t.Fatal(err)
	}
	if thumb.Bounds().Dx() != 240 || thumb.Bounds().Dy() != 240 {
		t.Fatalf("thumb = %v", thumb.Bounds())
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "Product Hunt upload") {
		t.Fatalf("readme missing PH checklist:\n%s", readme)
	}
	if !strings.Contains(string(readme), "240×240") {
		t.Fatalf("readme missing thumbnail hint:\n%s", readme)
	}
}

func TestInspectProductHuntReadyPack(t *testing.T) {
	dir := t.TempDir()
	if _, err := WritePack(dir, Sources{
		Desktop: solid(640, 400, nrgba(20, 80, 180, 255)),
		Mobile:  solid(200, 400, nrgba(20, 160, 80, 255)),
		URL:     "https://example.com",
	}, []Preset{PresetProductHunt}, Options{Title: "Hunt"}); err != nil {
		t.Fatal(err)
	}
	kit, err := InspectProductHunt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !kit.Ready {
		t.Fatalf("expected ready pack, errors=%v assets=%+v", kit.Errors, kit.Assets)
	}
	if kit.ComposerURL != "https://www.producthunt.com/posts/new" {
		t.Fatalf("composer = %q", kit.ComposerURL)
	}
}

func TestInspectProductHuntRejectsGitHubOnlyPack(t *testing.T) {
	dir := t.TempDir()
	if _, err := WritePack(dir, Sources{
		Desktop: solid(640, 400, nrgba(20, 80, 180, 255)),
		URL:     "https://example.com",
	}, []Preset{PresetGitHubReadme}, Options{Title: "GH"}); err != nil {
		t.Fatal(err)
	}
	kit, err := InspectProductHunt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if kit.Ready {
		t.Fatal("github-readme pack must not be PH-ready")
	}
	if len(kit.Errors) == 0 {
		t.Fatal("expected missing-gallery errors")
	}
}

func TestWritePackRequiresSource(t *testing.T) {
	_, err := WritePack(t.TempDir(), Sources{}, []Preset{PresetRaw}, Options{})
	if err == nil {
		t.Fatal("expected error")
	}
}
