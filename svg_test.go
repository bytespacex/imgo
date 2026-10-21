package main

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/sfnt"
)

func writeSVG(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.svg")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSVGColors pins the geometry and colour of a basic render: shapes land at
// their viewBox coordinates, the background is visible around them, and the
// canvas matches the declared size.
func TestSVGColors(t *testing.T) {
	path := writeSVG(t, `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100" viewBox="0 0 200 100">
<rect width="200" height="100" fill="#202020"/>
<rect x="10" y="10" width="80" height="80" fill="#ff0000"/>
<circle cx="150" cy="50" r="30" fill="#00ff00"/>
</svg>`)
	img, err := svgFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 200 || b.Dy() != 100 {
		t.Fatalf("bounds = %v, want 200x100", b)
	}
	cases := []struct {
		x, y int
		want color.RGBA
	}{
		{50, 50, color.RGBA{0xff, 0, 0, 0xff}},     // inside the red square
		{150, 50, color.RGBA{0, 0xff, 0, 0xff}},    // circle centre
		{5, 5, color.RGBA{0x20, 0x20, 0x20, 0xff}}, // background
		{195, 95, color.RGBA{0x20, 0x20, 0x20, 0xff}},
	}
	for _, c := range cases {
		if got := color.RGBAModel.Convert(img.At(c.x, c.y)).(color.RGBA); got != c.want {
			t.Errorf("pixel (%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

// TestSVGStrokePadding pins that the per-path sub-image leaves room for the
// stroke around the path's centre line: a 20px-wide line must be fully drawn
// 9px away from it, not clipped where the geometry ends.
func TestSVGStrokePadding(t *testing.T) {
	path := writeSVG(t, `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100" viewBox="0 0 100 100">
<rect width="100" height="100" fill="#000000"/>
<line x1="20" y1="50" x2="80" y2="50" stroke="#ffffff" stroke-width="20"/>
</svg>`)
	img, err := svgFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := color.RGBAModel.Convert(img.At(50, 42)).(color.RGBA); got.R < 0xf0 {
		t.Errorf("stroke pixel (50,42) = %v, want white: stroke was clipped", got)
	}
	if got := color.RGBAModel.Convert(img.At(50, 25)).(color.RGBA); got.R > 0x20 {
		t.Errorf("pixel (50,25) = %v, want background: stroke painted too far", got)
	}
}

// TestSVGTextRenders pins that <text> is no longer dropped by the SVG
// pipeline. Only Latin is checked here: the bundled Go font guarantees those
// glyphs on any machine, while a CJK face is optional (see the next test).
func TestSVGTextRenders(t *testing.T) {
	path := writeSVG(t, `<svg xmlns="http://www.w3.org/2000/svg" width="240" height="80" viewBox="0 0 240 80">
<rect width="240" height="80" fill="#000000"/>
<text x="10" y="60" font-size="48" fill="#ffffff" font-family="sans-serif">imgo</text>
</svg>`)
	img, err := svgFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ink := 0
	for y := 5; y < 75; y++ {
		for x := 5; x < 200; x++ {
			if c := img.At(x, y); c.(color.RGBA).R > 0x80 {
				ink++
			}
		}
	}
	if ink < 50 {
		t.Errorf("only %d lit pixels in the label area; text was not drawn", ink)
	}
	if got := img.At(230, 10); got.(color.RGBA).R > 0x10 {
		t.Errorf("unexpected ink outside the text at (230,10): %v", got)
	}
}

// TestSVGTextChinese pins the original report: Chinese captions must render,
// with a fallback font when the requested family is unavailable. Machines
// without any CJK-capable font skip instead of failing.
func TestSVGTextChinese(t *testing.T) {
	var buf sfnt.Buffer
	cjk := false
	for _, f := range svgFontsFor(styleMap{"font-family": "sans-serif", "font-size": "40px"}) {
		if gi, err := f.GlyphIndex(&buf, '活'); err == nil && gi != 0 {
			cjk = true
			break
		}
	}
	if !cjk {
		t.Skip("no CJK-capable system font")
	}
	path := writeSVG(t, `<svg xmlns="http://www.w3.org/2000/svg" width="240" height="80" viewBox="0 0 240 80">
<rect width="240" height="80" fill="#000000"/>
<text x="10" y="58" font-size="40" fill="#ffffff" font-family="sans-serif">活动能量</text>
</svg>`)
	img, err := svgFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ink := 0
	for y := 10; y < 75; y++ {
		for x := 5; x < 200; x++ {
			if c := img.At(x, y); c.(color.RGBA).R > 0x80 {
				ink++
			}
		}
	}
	if ink < 100 {
		t.Errorf("only %d lit pixels in the Chinese label; text was not drawn", ink)
	}
}

// TestSVGManyShapesTime guards the per-path bounds optimisation. Without it
// every shape blends the whole canvas — 3600 cells at 800x800 is a minute of
// work — so a generous limit still catches a return to full-canvas draws.
func TestSVGManyShapesTime(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800" viewBox="0 0 800 800">`)
	b.WriteString(`<rect width="800" height="800" fill="#101010"/>`)
	for y := 0; y < 60; y++ {
		for x := 0; x < 60; x++ {
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="10" height="10" fill="#c94c67"/>`, x*13, y*13)
		}
	}
	b.WriteString(`</svg>`)
	path := writeSVG(t, b.String())

	start := time.Now()
	img, err := svgFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("rendering 3600 shapes took %v, want under 5s", d)
	}
	want := color.RGBA{0xc9, 0x4c, 0x67, 0xff}
	if got := color.RGBAModel.Convert(img.At(5, 5)).(color.RGBA); got != want {
		t.Errorf("cell pixel = %v, want %v", got, want)
	}
	if got := color.RGBAModel.Convert(img.At(11, 5)).(color.RGBA); got != (color.RGBA{0x10, 0x10, 0x10, 0xff}) {
		t.Errorf("gap pixel = %v, want background", got)
	}
}
