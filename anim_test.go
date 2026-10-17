//go:build gui

package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kettek/apng"
)

// animWebP32 is a 3-frame 32x32 animated WebP (red/green/blue, 100ms each),
// encoded with nativewebp and embedded so the test needs no external files.
const animWebP32 = "UklGRpYEAABXRUJQVlA4WAoAAAACAAAAHwAAHwAAQU5JTQYAAAAAAAAAAABBTk1GdAEAAAAAAAAAAB8AAB8AAGQAAAFWUDhM" +
	"XAEAAC8fwAcATQAgIQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAACAAQ0f9UlwAiGQAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAIBgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQP8jsvUDAEFOTUZyAQAA" +
	"AAAAAAAAHwAAHwAAZAAAAVZQOExaAQAALx/ABwBNACAhAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAIABDR/1SX" +
	"ACIBAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAwAgGAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAABAZmTrB0FOTUZ0AQAAAAAAAAAAHwAAHwAAZAAAAVZQOExcAQAALx/ABwBNACAhAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAIABDR/1SXACIZAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAgGAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAABA9D+y9QMA"

var wantColors = []color.NRGBA{
	{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255},
}

func checkFrames(t *testing.T, frames []rawFrame) {
	t.Helper()
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	for i, fr := range frames {
		if fr.delay != 100*time.Millisecond {
			t.Errorf("frame %d delay = %v, want 100ms", i, fr.delay)
		}
		b := fr.img.Bounds()
		if b.Dx() != 32 || b.Dy() != 32 {
			t.Fatalf("frame %d size = %dx%d, want 32x32", i, b.Dx(), b.Dy())
		}
		got := color.NRGBAModel.Convert(fr.img.At(16, 16)).(color.NRGBA)
		if got != wantColors[i] {
			t.Errorf("frame %d centre = %+v, want %+v", i, got, wantColors[i])
		}
	}
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGIFAnim(t *testing.T) {
	var buf bytes.Buffer
	var imgs []*image.Paletted
	for _, c := range wantColors {
		img := image.NewPaletted(image.Rect(0, 0, 32, 32), color.Palette{color.Transparent, c})
		for i := range img.Pix {
			img.Pix[i] = 1
		}
		imgs = append(imgs, img)
	}
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image: imgs, Delay: []int{10, 10, 10},
		Disposal: []byte{gif.DisposalNone, gif.DisposalNone, gif.DisposalNone},
		Config:   image.Config{ColorModel: imgs[0].Palette, Width: 32, Height: 32},
	}); err != nil {
		t.Fatal(err)
	}
	checkFrames(t, decodeAnim(writeTemp(t, "a.gif", buf.Bytes())))
}

func TestGIFSubFrameDisposal(t *testing.T) {
	// frame 0 fills the canvas, frames 1-2 are small sub-rects with
	// DisposalBackground: the rest of the canvas must survive.
	var buf bytes.Buffer
	f0 := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.White})
	f1 := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black})
	f2 := image.NewPaletted(image.Rect(4, 4, 8, 8), color.Palette{color.Black})
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{f0, f1, f2}, Delay: []int{10, 10, 10},
		Disposal: []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalBackground},
		Config:   image.Config{ColorModel: f0.Palette, Width: 8, Height: 8},
	}); err != nil {
		t.Fatal(err)
	}
	frames := decodeAnim(writeTemp(t, "b.gif", buf.Bytes()))
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	// frame 1: black square at (0,0), white everywhere else
	if c := color.NRGBAModel.Convert(frames[1].img.At(0, 0)).(color.NRGBA); c.R != 0 {
		t.Errorf("frame1 (0,0) = %+v, want black", c)
	}
	if c := color.NRGBAModel.Convert(frames[1].img.At(7, 7)).(color.NRGBA); c.R != 255 {
		t.Errorf("frame1 (7,7) = %+v, want white (background kept)", c)
	}
	// frame 2: frame 1 was disposed back to the background colour (transparent),
	// so only the (4,4) square stays black
	if c := color.NRGBAModel.Convert(frames[2].img.At(0, 0)).(color.NRGBA); c.A != 0 {
		t.Errorf("frame2 (0,0) = %+v, want transparent (frame1 disposed to background)", c)
	}
	if c := color.NRGBAModel.Convert(frames[2].img.At(5, 5)).(color.NRGBA); c.R != 0 {
		t.Errorf("frame2 (5,5) = %+v, want black", c)
	}
}

func TestAPNGAnim(t *testing.T) {
	var buf bytes.Buffer
	a := apng.APNG{LoopCount: 0}
	for _, c := range wantColors {
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				img.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: 255})
			}
		}
		a.Frames = append(a.Frames, apng.Frame{
			Image: img, DelayNumerator: 1, DelayDenominator: 10,
			DisposeOp: 0, BlendOp: 0,
		})
	}
	if err := apng.Encode(&buf, a); err != nil {
		t.Fatal(err)
	}
	checkFrames(t, decodeAnim(writeTemp(t, "a.png", buf.Bytes())))
}

func TestWebPAnim(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(animWebP32)
	if err != nil {
		t.Fatal(err)
	}
	checkFrames(t, decodeAnim(writeTemp(t, "a.webp", raw)))
}

func TestStaticStaysStatic(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if frames := decodeAnim(writeTemp(t, "a.png", buf.Bytes())); frames != nil {
		t.Errorf("plain PNG decoded as animation: %d frames", len(frames))
	}
	if frames := decodeAnim(writeTemp(t, "a.txt", []byte("not an image"))); frames != nil {
		t.Errorf("non-image decoded as animation: %d frames", len(frames))
	}
}
