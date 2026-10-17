// Scaling and output (Sixel / Unicode half-block chars).
package main

import (
	"image"
	"image/color"
	"io"

	sixel "github.com/mattn/go-sixel"
	xdraw "golang.org/x/image/draw"
)

// scaleImg resamples proportionally to the target size. CatmullRom quality, pure Go.
func scaleImg(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return img
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	// ApproxBiLinear for fast pre-shrink, then CatmullRom refine (big images stay fast)
	if b.Dx() > w*3 && b.Dy() > h*3 {
		mid := image.NewNRGBA(image.Rect(0, 0, w*2, h*2))
		xdraw.ApproxBiLinear.Scale(mid, mid.Bounds(), img, b, xdraw.Src, nil)
		img, b = mid, mid.Bounds()
	}
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst
}

// outputSixel writes a DECSIXEL stream. Pixels are square; size must be cell-converted beforehand.
func outputSixel(w io.Writer, img image.Image, dither bool, colors int) error {
	enc := sixel.NewEncoder(w)
	enc.Dither = dither
	enc.Colors = colors
	enc.Transparent = true // transparent pixels show terminal background (P2=1)
	return enc.Encode(img)
}

// outputSymbols writes Unicode half-block chars (▀), two stacked pixels per cell, 24bit true color.
// For terminals without Sixel. img should be w x (rows*2).
func outputSymbols(w io.Writer, img image.Image) {
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*(b.Dy()+1)*24)
	for y := 0; y+1 < b.Dy(); y += 2 {
		for x := 0; x < b.Dx(); x++ {
			t := nrgbaAt(img, b.Min.X+x, b.Min.Y+y)
			u := nrgbaAt(img, b.Min.X+x, b.Min.Y+y+1)
			// top half = fg (color of ▀), bottom half = bg
			out = append(out, "\x1b[38;2;"...)
			out = appendInts(out, t.R, t.G, t.B)
			out = append(out, "m\x1b[48;2;"...)
			out = appendInts(out, u.R, u.G, u.B)
			out = append(out, "m▀"...)
		}
		out = append(out, "\x1b[0m\n"...)
	}
	_, _ = w.Write(out)
}

func nrgbaAt(img image.Image, x, y int) color.NRGBA {
	r, g, b, _ := img.At(x, y).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
}

func appendInts(dst []byte, vals ...uint8) []byte {
	for i, v := range vals {
		if i > 0 {
			dst = append(dst, ';')
		}
		dst = append(dst, []byte(itoa(int(v)))...)
	}
	return dst
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
