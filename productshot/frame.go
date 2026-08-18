package productshot

import (
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

type themeColors struct {
	bgTop, bgBot       color.NRGBA
	window             color.NRGBA
	titleBar           color.NRGBA
	border             color.NRGBA
	title              color.NRGBA
	caption            color.NRGBA
	shadow             color.NRGBA
}

func colorsFor(theme Theme) themeColors {
	if theme == ThemeDark {
		return themeColors{
			bgTop:    nrgba(11, 18, 32, 255),
			bgBot:    nrgba(30, 41, 59, 255),
			window:   nrgba(15, 23, 42, 255),
			titleBar: nrgba(17, 24, 39, 255),
			border:   nrgba(51, 65, 85, 255),
			title:    nrgba(226, 232, 240, 255),
			caption:  nrgba(148, 163, 184, 255),
			shadow:   nrgba(0, 0, 0, 70),
		}
	}
	return themeColors{
		bgTop:    nrgba(238, 242, 255, 255),
		bgBot:    nrgba(226, 232, 240, 255),
		window:   nrgba(255, 255, 255, 255),
		titleBar: nrgba(248, 250, 252, 255),
		border:   nrgba(226, 232, 240, 255),
		title:    nrgba(51, 65, 85, 255),
		caption:  nrgba(71, 85, 105, 255),
		shadow:   nrgba(15, 23, 42, 46),
	}
}

var (
	trafficClose = nrgba(255, 95, 87, 255)
	trafficMin   = nrgba(254, 188, 46, 255)
	trafficMax   = nrgba(40, 200, 64, 255)
)

func nrgba(r, g, b, a uint8) color.NRGBA { return color.NRGBA{R: r, G: g, B: b, A: a} }

// Frame paints src into a product canvas: gradient, window chrome, optional
// caption. Product shots use scale-to-fill so the window never letterboxes.
func Frame(src image.Image, format Format, opt Options) *image.NRGBA {
	if src == nil {
		src = image.NewNRGBA(image.Rect(0, 0, 4, 4))
	}
	if opt.Theme == "" {
		opt.Theme = ThemeLight
	}
	dst := image.NewNRGBA(image.Rect(0, 0, format.Width, format.Height))
	pal := colorsFor(opt.Theme)
	fillGradient(dst, pal.bgTop, pal.bgBot)

	if format.Kind == KindRaw {
		scaleToFill(dst, dst.Bounds(), src)
		return dst
	}

	win, titleH, radius := windowGeometry(format, opt.Caption != "")
	shadow := win.Inset(-1)
	shadow.Min.Y += 10
	shadow.Max.Y += 14
	shadow.Min.X += 2
	shadow.Max.X += 2
	fillRounded(dst, shadow, radius+2, pal.shadow)
	// A second, softer drop for depth.
	shadow2 := win
	shadow2.Min.Y += 18
	shadow2.Max.Y += 22
	fillRounded(dst, shadow2, radius+4, nrgba(pal.shadow.R, pal.shadow.G, pal.shadow.B, uint8(float64(pal.shadow.A)*0.45)))

	fillRounded(dst, win, radius, pal.window)
	strokeRounded(dst, win, radius, pal.border, 1.2)

	titleBar := image.Rect(win.Min.X, win.Min.Y, win.Max.X, win.Min.Y+titleH)
	fillRoundedTop(dst, titleBar, radius, pal.titleBar)
	// Hairline under the title bar.
	for x := win.Min.X + 1; x < win.Max.X-1; x++ {
		blend(dst, x, titleBar.Max.Y-1, pal.border, 0.85)
	}

	cx := win.Min.X + 16
	cy := win.Min.Y + titleH/2
	fillCircle(dst, cx, cy, 6, trafficClose)
	fillCircle(dst, cx+20, cy, 6, trafficMin)
	fillCircle(dst, cx+40, cy, 6, trafficMax)

	label := chromeLabel(opt)
	if label != "" && format.Kind != KindThumbnail {
		face := fontFace(math.Max(11, float64(titleH)*0.42))
		maxW := win.Dx() - 78
		label = truncateToWidth(face, label, maxW)
		drawString(dst, face, label, win.Min.X+70, cy+int(face.Metrics().Ascent.Round()/2)-2, pal.title)
	}

	content := image.Rect(win.Min.X+1, titleBar.Max.Y, win.Max.X-1, win.Max.Y-1)
	if content.Dx() > 2 && content.Dy() > 2 {
		clip := image.NewNRGBA(content)
		scaleToFill(clip, clip.Bounds(), src)
		draw.Draw(dst, content, clip, clip.Bounds().Min, draw.Over)
	}

	if opt.Caption != "" && format.Kind != KindThumbnail {
		face := fontFace(math.Max(12, float64(format.Height)*0.028))
		text := truncateToWidth(face, opt.Caption, format.Width-48)
		tw := font.MeasureString(face, text).Ceil()
		x := (format.Width - tw) / 2
		y := win.Max.Y + int(math.Max(22, float64(format.Height)*0.04))
		drawString(dst, face, text, x, y, pal.caption)
	}
	return dst
}

func windowGeometry(format Format, hasCaption bool) (image.Rectangle, int, int) {
	w, h := format.Width, format.Height
	padX := int(math.Round(float64(w) * 0.07))
	padTop := int(math.Round(float64(h) * 0.09))
	padBot := int(math.Round(float64(h) * 0.10))
	if hasCaption {
		padBot = int(math.Round(float64(h) * 0.14))
	}
	if format.Kind == KindThumbnail {
		padX, padTop, padBot = 14, 14, 14
	}
	if format.Phone {
		// Tall window, more side breathing room.
		padX = int(math.Round(float64(w) * 0.32))
		padTop = int(math.Round(float64(h) * 0.06))
		padBot = int(math.Round(float64(h) * 0.07))
		if hasCaption {
			padBot = int(math.Round(float64(h) * 0.12))
		}
	}
	if padX < 12 {
		padX = 12
	}
	if padTop < 12 {
		padTop = 12
	}
	if padBot < 12 {
		padBot = 12
	}
	win := image.Rect(padX, padTop, w-padX, h-padBot)
	titleH := int(math.Max(28, math.Min(40, float64(win.Dy())*0.08)))
	if format.Kind == KindThumbnail {
		titleH = 22
	}
	radius := int(math.Max(10, math.Min(18, float64(win.Dx())*0.018)))
	if format.Phone {
		radius = 22
	}
	return win, titleH, radius
}

func chromeLabel(opt Options) string {
	if strings.TrimSpace(opt.Title) != "" {
		return strings.TrimSpace(opt.Title)
	}
	return strings.TrimSpace(opt.URL)
}

func scaleToFill(dst draw.Image, r image.Rectangle, src image.Image) {
	sb := src.Bounds()
	if sb.Empty() || r.Empty() {
		return
	}
	sw, sh := sb.Dx(), sb.Dy()
	dw, dh := r.Dx(), r.Dy()
	scale := math.Max(float64(dw)/float64(sw), float64(dh)/float64(sh))
	tw := int(math.Ceil(float64(sw) * scale))
	th := int(math.Ceil(float64(sh) * scale))
	tmp := image.NewNRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(tmp, tmp.Bounds(), src, sb, draw.Src, nil)
	ox := (tw - dw) / 2
	oy := (th - dh) / 2
	draw.Draw(dst, r, tmp, image.Pt(ox, oy), draw.Src)
}

func fillGradient(img *image.NRGBA, top, bot color.NRGBA) {
	b := img.Bounds()
	h := b.Dy()
	if h <= 1 {
		fillRect(img, b, top)
		return
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		t := float64(y-b.Min.Y) / float64(h-1)
		c := lerp(top, bot, t)
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func fillRect(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func fillRounded(img *image.NRGBA, r image.Rectangle, radius int, c color.NRGBA) {
	if r.Empty() {
		return
	}
	if radius < 1 {
		fillRect(img, r, c)
		return
	}
	minX := float64(r.Min.X) + 0.5
	minY := float64(r.Min.Y) + 0.5
	maxX := float64(r.Max.X) - 0.5
	maxY := float64(r.Max.Y) - 0.5
	rad := float64(radius)
	pad := radius + 2
	for y := r.Min.Y - 1; y < r.Max.Y+1; y++ {
		for x := r.Min.X - pad; x < r.Max.X+pad; x++ {
			if !image.Pt(x, y).In(img.Bounds()) {
				continue
			}
			a := coverage(sdRoundRect(float64(x)+0.5, float64(y)+0.5, minX, minY, maxX, maxY, rad))
			if a <= 0 {
				continue
			}
			blend(img, x, y, c, a)
		}
	}
}

func fillRoundedTop(img *image.NRGBA, r image.Rectangle, radius int, c color.NRGBA) {
	// Round only the top corners; square off the bottom so it meets the content.
	fillRounded(img, r, radius, c)
	bottom := image.Rect(r.Min.X, r.Max.Y-radius, r.Max.X, r.Max.Y)
	fillRect(img, bottom.Intersect(img.Bounds()), c)
}

func strokeRounded(img *image.NRGBA, r image.Rectangle, radius int, c color.NRGBA, width float64) {
	minX := float64(r.Min.X) + 0.5
	minY := float64(r.Min.Y) + 0.5
	maxX := float64(r.Max.X) - 0.5
	maxY := float64(r.Max.Y) - 0.5
	rad := float64(radius)
	pad := radius + 3
	for y := r.Min.Y - 2; y < r.Max.Y+2; y++ {
		for x := r.Min.X - pad; x < r.Max.X+pad; x++ {
			if !image.Pt(x, y).In(img.Bounds()) {
				continue
			}
			d := math.Abs(sdRoundRect(float64(x)+0.5, float64(y)+0.5, minX, minY, maxX, maxY, rad))
			a := coverage(d - width)
			if a <= 0 {
				continue
			}
			// Only stroke the ring (outside the fill).
			inner := coverage(sdRoundRect(float64(x)+0.5, float64(y)+0.5, minX, minY, maxX, maxY, rad))
			if inner > 0.85 {
				continue
			}
			blend(img, x, y, c, a)
		}
	}
}

func fillCircle(img *image.NRGBA, cx, cy, r int, c color.NRGBA) {
	rf := float64(r)
	for y := cy - r - 1; y <= cy+r+1; y++ {
		for x := cx - r - 1; x <= cx+r+1; x++ {
			if !image.Pt(x, y).In(img.Bounds()) {
				continue
			}
			dx := float64(x-cx) + 0
			dy := float64(y-cy) + 0
			a := coverage(math.Hypot(dx, dy) - rf)
			if a <= 0 {
				continue
			}
			blend(img, x, y, c, a)
		}
	}
}

func sdRoundRect(px, py, minX, minY, maxX, maxY, r float64) float64 {
	cx := (minX + maxX) / 2
	cy := (minY + maxY) / 2
	hw := (maxX - minX) / 2
	hh := (maxY - minY) / 2
	if r > hw {
		r = hw
	}
	if r > hh {
		r = hh
	}
	dx := math.Abs(px-cx) - (hw - r)
	dy := math.Abs(py-cy) - (hh - r)
	ax := math.Max(dx, 0)
	ay := math.Max(dy, 0)
	return math.Hypot(ax, ay) + math.Min(math.Max(dx, dy), 0) - r
}

func coverage(sd float64) float64 {
	a := 0.5 - sd
	if a <= 0 {
		return 0
	}
	if a >= 1 {
		return 1
	}
	return a
}

func blend(img *image.NRGBA, x, y int, c color.NRGBA, a float64) {
	if a <= 0 {
		return
	}
	if a > 1 {
		a = 1
	}
	srcA := float64(c.A) / 255 * a
	if srcA <= 0 {
		return
	}
	dst := img.NRGBAAt(x, y)
	outA := srcA + float64(dst.A)/255*(1-srcA)
	if outA <= 0 {
		return
	}
	img.SetNRGBA(x, y, color.NRGBA{
		R: uint8(math.Round((float64(c.R)*srcA + float64(dst.R)*float64(dst.A)/255*(1-srcA)) / outA)),
		G: uint8(math.Round((float64(c.G)*srcA + float64(dst.G)*float64(dst.A)/255*(1-srcA)) / outA)),
		B: uint8(math.Round((float64(c.B)*srcA + float64(dst.B)*float64(dst.A)/255*(1-srcA)) / outA)),
		A: uint8(math.Round(outA * 255)),
	})
}

func lerp(a, b color.NRGBA, t float64) color.NRGBA {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return color.NRGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: uint8(float64(a.A) + (float64(b.A)-float64(a.A))*t),
	}
}

var (
	parsedFont     *opentype.Font
	parsedFontOnce sync.Once
	parsedFontErr  error
	faceCache      sync.Map
)

func fontFace(size float64) font.Face {
	parsedFontOnce.Do(func() {
		parsedFont, parsedFontErr = opentype.Parse(goregular.TTF)
	})
	if parsedFontErr != nil || parsedFont == nil {
		return nil
	}
	key := int(size * 10)
	if got, ok := faceCache.Load(key); ok {
		face, ok := got.(font.Face)
		if !ok {
			return nil
		}
		return face
	}
	face, err := opentype.NewFace(parsedFont, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	faceCache.Store(key, face)
	return face
}

func drawString(dst *image.NRGBA, face font.Face, s string, x, y int, c color.NRGBA) {
	if face == nil || s == "" {
		return
	}
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

func truncateToWidth(face font.Face, s string, maxPx int) string {
	if face == nil || maxPx <= 0 {
		return s
	}
	if font.MeasureString(face, s).Ceil() <= maxPx {
		return s
	}
	runes := []rune(s)
	for n := len(runes) - 1; n > 0; n-- {
		cand := string(runes[:n]) + "…"
		if font.MeasureString(face, cand).Ceil() <= maxPx {
			return cand
		}
	}
	return "…"
}
