//go:build gui

// Animated image decoding: GIF, APNG and animated WebP.
//
// All three formats are decoded into the same shape — a full-canvas frame
// plus a delay — so the viewer plays them through one code path. Decoding is
// pure Go and never touches ebiten, which keeps it testable without a window.
//
// Frame composition follows each format's own rules:
//
//	GIF   DisposalNone/Background/Previous, frames drawn with Over
//	APNG  fcTL BlendOp (source/over) + DisposeOp (none/background/previous)
//	WebP  ANMF flags: blending (alpha/overwrite) + disposal (none/background)
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/gif"
	"image/png"
	"io"
	"os"
	"time"

	"github.com/kettek/apng"
	"golang.org/x/image/webp"
)

// animBudgetPixels caps decoded animation memory (frames x width x height).
// Bigger files are shown as a still image instead of eating gigabytes of RAM:
// a 1920x1080 300-frame GIF would otherwise be ~2.4GB.
const animBudgetPixels = 64 << 20 // 64 megapixels ~ 256MB of RGBA

var pngSig = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// rawFrame is one animation frame, composited to the full canvas size.
type rawFrame struct {
	img   image.Image
	delay time.Duration
}

// decodeAnim decodes path into animation frames. It returns nil when the file
// is not animated, is not a supported animation format, or is over the memory
// budget; the caller then falls back to the static first frame.
func decodeAnim(path string) []rawFrame {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	switch {
	case n >= 6 && string(head[:6]) == "GIF87a", n >= 6 && string(head[:6]) == "GIF89a":
		return gifFrames(f)
	case n >= 8 && bytes.Equal(head[:8], pngSig):
		return apngFrames(f)
	case n >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return webpAnimFrames(f)
	}
	return nil
}

// ---------- GIF ----------

func gifFrames(r io.Reader) []rawFrame {
	g, err := gif.DecodeAll(r)
	if err != nil || len(g.Image) < 2 {
		return nil
	}
	w, h := g.Config.Width, g.Config.Height
	if w <= 0 || h <= 0 || overBudget(w, h, len(g.Image)) {
		return nil
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, w, h))
	var out []rawFrame
	var saved *image.NRGBA // canvas before a DisposalPrevious frame
	for i, pf := range g.Image {
		if i < len(g.Disposal) && g.Disposal[i] == gif.DisposalPrevious {
			saved = cloneNRGBA(canvas)
		}
		r := pf.Bounds().Intersect(canvas.Bounds())
		if !r.Empty() {
			draw.Draw(canvas, r, pf, r.Min, draw.Over)
		}
		out = append(out, rawFrame{img: cloneNRGBA(canvas), delay: gifDelay(g.Delay, i)})
		if i >= len(g.Disposal) {
			continue
		}
		switch g.Disposal[i] {
		case gif.DisposalBackground:
			draw.Draw(canvas, r, image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			if saved != nil {
				copy(canvas.Pix, saved.Pix)
			}
		}
	}
	return out
}

// gifDelay reads Delay in hundredths of a second. Like browsers, 0 and 1 are
// treated as 100ms, otherwise GIFs blink unreadably.
func gifDelay(cs []int, i int) time.Duration {
	if i < len(cs) && cs[i] > 1 {
		return time.Duration(cs[i]) * 10 * time.Millisecond
	}
	return 100 * time.Millisecond
}

// ---------- APNG ----------

func apngFrames(r io.ReadSeeker) []rawFrame {
	// The canvas size is in IHDR; kettek/apng only exposes per-frame rects.
	cfg, err := png.DecodeConfig(r)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	a, err := apng.DecodeAll(r)
	if err != nil {
		return nil
	}
	frames := a.Frames
	if len(frames) > 0 && frames[0].IsDefault {
		frames = frames[1:] // the static preview image, not part of the animation
	}
	if len(frames) < 2 || overBudget(cfg.Width, cfg.Height, len(frames)) {
		return nil
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	var out []rawFrame
	var saved *image.NRGBA
	for _, fr := range frames {
		if fr.Image == nil {
			continue
		}
		if fr.DisposeOp == 2 { // APNG_DISPOSE_OP_PREVIOUS
			saved = cloneNRGBA(canvas)
		}
		b := fr.Image.Bounds()
		dst := image.Rect(fr.XOffset, fr.YOffset, fr.XOffset+b.Dx(), fr.YOffset+b.Dy()).
			Intersect(canvas.Bounds())
		if !dst.Empty() {
			op := draw.Over // APNG_BLEND_OP_OVER
			if fr.BlendOp == 0 {
				op = draw.Src // APNG_BLEND_OP_SOURCE
			}
			draw.Draw(canvas, dst, fr.Image, dst.Min.Sub(image.Pt(fr.XOffset, fr.YOffset)), op)
		}
		out = append(out, rawFrame{img: cloneNRGBA(canvas), delay: apngDelay(fr.DelayNumerator, fr.DelayDenominator)})
		switch fr.DisposeOp {
		case 1: // APNG_DISPOSE_OP_BACKGROUND
			draw.Draw(canvas, dst, image.Transparent, image.Point{}, draw.Src)
		case 2:
			if saved != nil {
				copy(canvas.Pix, saved.Pix)
			}
		}
	}
	return out
}

func apngDelay(num, den uint16) time.Duration {
	if num == 0 {
		return 100 * time.Millisecond
	}
	if den == 0 {
		den = 100
	}
	d := time.Duration(num) * time.Second / time.Duration(den)
	if d <= 0 {
		return 100 * time.Millisecond
	}
	return d
}

// ---------- animated WebP ----------

// webpAnimFrames walks the RIFF container and decodes each ANMF sub-frame.
// x/image/webp only understands static WebP, so every frame payload is
// wrapped back into a (VP8X) RIFF container before decoding.
func webpAnimFrames(r io.Reader) []rawFrame {
	data, err := io.ReadAll(r)
	if err != nil || len(data) < 16 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil
	}
	var cw, ch int // canvas size from VP8X
	var out []rawFrame
	var canvas *image.NRGBA
	off := 12
	for off+8 <= len(data) {
		id := string(data[off : off+4])
		sz := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := data[off+8:]
		if sz < 0 || sz > len(body) {
			break
		}
		body = body[:sz]
		off += 8 + sz
		if off%2 == 1 {
			off++ // chunks are padded to an even size
		}
		switch id {
		case "VP8X":
			if len(body) < 10 {
				return nil
			}
			cw = 1 + le24(body[4:7])
			ch = 1 + le24(body[7:10])
		case "ANMF":
			if len(body) < 16 {
				return nil
			}
			// Frame X/Y are stored halved; W/H as minus one; all 24-bit LE.
			x := 2 * le24(body[0:3])
			y := 2 * le24(body[3:6])
			w := 1 + le24(body[6:9])
			h := 1 + le24(body[9:12])
			dur := le24(body[12:15])
			flags := body[15] // bit0: blending (0=alpha, 1=overwrite), bit1: dispose to background
			img := decodeWebPFrame(body[16:], cw, ch)
			if img == nil {
				continue
			}
			if canvas == nil {
				b := img.Bounds()
				ww, hh := cw, ch
				if ww <= 0 || hh <= 0 {
					ww, hh = x+w, y+h
				}
				if ww <= 0 || hh <= 0 || b.Dx() <= 0 || b.Dy() <= 0 {
					return nil
				}
				canvas = image.NewNRGBA(image.Rect(0, 0, ww, hh))
			}
			dst := image.Rect(x, y, x+w, y+h).Intersect(canvas.Bounds())
			if !dst.Empty() {
				op := draw.Over // blending: alpha
				if flags&0x01 != 0 {
					op = draw.Src // blending: overwrite
				}
				draw.Draw(canvas, dst, img, dst.Min.Sub(image.Pt(x, y)), op)
			}
			out = append(out, rawFrame{img: cloneNRGBA(canvas), delay: time.Duration(dur) * time.Millisecond})
			if flags&0x02 != 0 { // dispose: back to the background color
				draw.Draw(canvas, dst, image.Transparent, image.Point{}, draw.Src)
			}
		}
	}
	if len(out) < 2 {
		return nil
	}
	if overBudget(cw, ch, len(out)) {
		return nil
	}
	return out
}

// decodeWebPFrame decodes one ANMF payload (an optional ALPH chunk followed by
// a VP8/VP8L chunk). x/image/webp accepts ALPH only inside a VP8X container,
// so a payload starting with ALPH gets one synthesized.
func decodeWebPFrame(payload []byte, cw, ch int) image.Image {
	if len(payload) < 8 {
		return nil
	}
	buf := make([]byte, 0, len(payload)+32)
	if string(payload[:4]) == "ALPH" {
		head := []byte{'V', 'P', '8', 'X', 10, 0, 0, 0, 0x10, 0, 0, 0} // flags: alpha
		head = append24(head, cw)
		head = append24(head, ch)
		buf = append(buf, head...)
	}
	buf = append(buf, payload...)
	riff := make([]byte, 0, len(buf)+12)
	riff = append(riff, "RIFF"...)
	var le [4]byte
	binary.LittleEndian.PutUint32(le[:], uint32(4+len(buf)))
	riff = append(riff, le[:]...)
	riff = append(riff, "WEBP"...)
	riff = append(riff, buf...)
	img, err := webp.Decode(bytes.NewReader(riff))
	if err != nil {
		return nil
	}
	return img
}

// le24 reads a 24-bit little-endian value.
func le24(b []byte) int {
	return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
}

// append24 appends v as a 24-bit little-endian value.
func append24(dst []byte, v int) []byte {
	return append(dst, byte(v), byte(v>>8), byte(v>>16))
}

// ---------- shared helpers ----------

func overBudget(w, h, frames int) bool {
	if w <= 0 || h <= 0 {
		return true
	}
	return frames > 0 && int64(w)*int64(h)*int64(frames) > animBudgetPixels
}

func cloneNRGBA(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}
