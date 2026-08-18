package productshot

import "testing"

func TestParsePreset(t *testing.T) {
	got, err := ParsePreset("")
	if err != nil || got != PresetGitHubReadme {
		t.Fatalf("empty preset = %q, %v; want github-readme", got, err)
	}
	got, err = ParsePreset("Product-Hunt")
	if err != nil || got != PresetProductHunt {
		t.Fatalf("product-hunt parse = %q, %v", got, err)
	}
	if _, err := ParsePreset("tiktok"); err == nil {
		t.Fatal("expected unknown preset error")
	}
}

func TestExpandPresetsAll(t *testing.T) {
	got := ExpandPresets(PresetAll)
	if len(got) != 4 {
		t.Fatalf("all expanded to %d, want 4: %v", len(got), got)
	}
	got = ExpandPresets(PresetGitHubReadme, PresetGitHubReadme, PresetOG)
	if len(got) != 2 {
		t.Fatalf("dedupe = %v", got)
	}
}

func TestProductHuntFormatsIncludeThumbnailAndGallery(t *testing.T) {
	formats := StillFormats(PresetProductHunt, true)
	if len(formats) != 3 {
		t.Fatalf("formats = %d, want 3", len(formats))
	}
	if formats[0].Width != 1270 || formats[0].Height != 760 {
		t.Fatalf("gallery size = %dx%d", formats[0].Width, formats[0].Height)
	}
	if formats[1].Phone != true {
		t.Fatal("second gallery image should be the phone frame")
	}
	if formats[2].Width != 240 || formats[2].Height != 240 {
		t.Fatalf("thumb = %dx%d", formats[2].Width, formats[2].Height)
	}
	if n := len(StillFormats(PresetProductHunt, false)); n != 2 {
		t.Fatalf("without mobile want 2 formats, got %d", n)
	}
}

func TestCaptureFPSMatchesWallClock(t *testing.T) {
	got := CaptureFPS(24, 3000)
	if got < 7.9 || got > 8.1 {
		t.Fatalf("24 frames / 3s = %v, want ~8", got)
	}
	if CaptureFPS(0, 1000) <= 0 {
		t.Fatal("zero frames should fall back to a positive rate")
	}
}

func TestVideoSpecWithCapture(t *testing.T) {
	spec := VideoSpecs(PresetGitHubReadme)[0].WithCapture(16, 2000)
	if spec.InputFPS < 7.9 || spec.InputFPS > 8.1 {
		t.Fatalf("InputFPS = %v", spec.InputFPS)
	}
}

func TestVideoSpecsAllExpands(t *testing.T) {
	got := VideoSpecs(PresetAll)
	if len(got) != 2 {
		t.Fatalf("all video specs = %d, want 2: %+v", len(got), got)
	}
	if got[0].Name != "github-readme" || got[1].Name != "product-hunt" {
		t.Fatalf("names = %q %q", got[0].Name, got[1].Name)
	}
}

func TestWantsMobileDefaultsForProductHunt(t *testing.T) {
	opt := Options{}
	if !opt.WantsMobile([]Preset{PresetProductHunt}) {
		t.Fatal("product-hunt should capture mobile by default")
	}
	if opt.WantsMobile([]Preset{PresetGitHubReadme}) {
		t.Fatal("github-readme should not capture mobile by default")
	}
	opt.NoMobile = true
	if opt.WantsMobile([]Preset{PresetProductHunt}) {
		t.Fatal("--no-mobile must win")
	}
}
