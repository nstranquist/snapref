package browser

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestPNGToJPEGAndScale(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 20, G: 80, B: 180, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	jpg, err := pngToJPEG(buf.Bytes(), 70)
	if err != nil {
		t.Fatal(err)
	}
	if len(jpg) == 0 {
		t.Fatal("empty jpeg")
	}
	scaled, err := scaleJPEG(jpg, 20, 10, 70)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := image.Decode(bytes.NewReader(scaled))
	if err != nil {
		t.Fatal(err)
	}
	if out.Bounds().Dx() != 20 || out.Bounds().Dy() != 10 {
		t.Fatalf("scaled = %v", out.Bounds())
	}
}
