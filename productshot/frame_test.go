package productshot

import (
	"image"
	"image/color"
	"testing"
)

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func TestFrameExactDimensions(t *testing.T) {
	src := solid(800, 500, nrgba(30, 90, 200, 255))
	for _, format := range []Format{
		{Name: "gh", Kind: KindHero, Width: 1280, Height: 720},
		{Name: "ph", Kind: KindGallery, Width: 1270, Height: 760},
		{Name: "thumb", Kind: KindThumbnail, Width: 240, Height: 240},
		{Name: "phone", Kind: KindGallery, Width: 1270, Height: 760, Phone: true},
	} {
		got := Frame(src, format, Options{Title: "Demo", Theme: ThemeLight})
		if got.Bounds().Dx() != format.Width || got.Bounds().Dy() != format.Height {
			t.Fatalf("%s bounds = %v, want %dx%d", format.Name, got.Bounds(), format.Width, format.Height)
		}
	}
}

func TestFrameHasChromeNotJustSource(t *testing.T) {
	src := solid(400, 300, nrgba(220, 20, 60, 255))
	got := Frame(src, Format{Name: "gh", Kind: KindHero, Width: 1280, Height: 720}, Options{
		Title: "Ndev Browser", Theme: ThemeLight,
	})
	// Corner of the canvas is the gradient, not the crimson source.
	corner := got.NRGBAAt(2, 2)
	if corner.R > 180 && corner.G < 80 && corner.B < 80 {
		t.Fatalf("corner looks like the source fill: %+v", corner)
	}
	// Traffic-light close button sits near the window's top-left.
	win, titleH, _ := windowGeometry(Format{Kind: KindHero, Width: 1280, Height: 720}, false)
	px := got.NRGBAAt(win.Min.X+16, win.Min.Y+titleH/2)
	if int(px.R) < 180 || int(px.G) > 140 {
		t.Fatalf("expected red traffic light at title-bar, got %+v (win=%v)", px, win)
	}
}

func TestFrameDarkThemeBackground(t *testing.T) {
	src := solid(200, 200, nrgba(255, 255, 255, 255))
	got := Frame(src, Format{Name: "og", Kind: KindOG, Width: 1200, Height: 630}, Options{Theme: ThemeDark})
	c := got.NRGBAAt(4, 4)
	if int(c.R)+int(c.G)+int(c.B) > 180 {
		t.Fatalf("dark theme corner should be dark, got %+v", c)
	}
}
