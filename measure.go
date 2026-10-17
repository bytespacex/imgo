//go:build gui

// Measure mode: drag out a rectangle to read its size in source image pixels
// and in on-screen pixels, then copy the readout. It is a display-only overlay;
// the picture on disk is never touched. Kept in its own file so the feature is
// self-contained and easy to review or drop.
package main

import (
	"fmt"
	"image/color"
	"math"
	"os"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// toggleMeasure turns measure mode on or off. Measuring needs a visible,
// aimable cursor and a still picture, so entering it leaves hands-free panning
// and stops the slideshow, exactly like the colour picker's restrictions.
// Leaving the mode drops any selection.
func (v *viewer) toggleMeasure() {
	if v.measure {
		v.measure = false
		v.measDragging = false
		return
	}
	if v.panLock {
		v.setPanLock(false)
	}
	v.dragging = false
	v.slide = false
	v.measure = true
}

// updateMeasure drives the selection with the left button: a press anchors one
// corner, the drag moves the opposite one, the release ends it. The corners are
// kept in source image pixels, so the box stays glued to the picture as it is
// zoomed or panned mid-drag. On release the size is copied, announced with a
// tooltip, and the mode closes again, leaving the picture clean.
func (v *viewer) updateMeasure() {
	if v.img == nil {
		return
	}
	x, y := ebiten.CursorPosition()
	ix, iy, ok := v.screenToImage(x, y)
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) && ok {
		v.measDragging = true
		v.measAX, v.measAY = ix, iy
		v.measBX, v.measBY = ix, iy
	}
	if v.measDragging && ok {
		v.measBX, v.measBY = ix, iy
	}
	// Complete on release: finalise as soon as the button is up (not only on the
	// exact release tick, so a release the window never sees cannot wedge the
	// drag). A real box is copied and announced; then the mode ends and the
	// selection is dropped.
	if v.measDragging && !ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
		_, _, _, _, ok := v.measureRect()
		msg := ""
		if ok {
			msg = v.measureReadout()
		}
		v.measDragging = false
		v.measure = false
		if msg != "" {
			if err := setClipboardText(msg, ""); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			v.tooltip(msg)
		}
	}
}

// measureRect snaps the live selection to whole source pixels and returns the
// pixel box it covers, clamped to the picture. ok is false until the drag spans
// at least one pixel in both directions.
func (v *viewer) measureRect() (x0, y0, x1, y1 int, ok bool) {
	if v.img == nil || !v.measDragging {
		return 0, 0, 0, 0, false
	}
	b := v.img.Bounds()
	return selBox(v.measAX, v.measAY, v.measBX, v.measBY, b.Dx(), b.Dy())
}

// selBox snaps the two dragged corners (given in source image pixels) to whole
// pixels and clamps them to a w x h picture, returning the box they cover. ok is
// false until the selection spans at least one pixel each way. It is pure math
// so the rounding and clamping can be checked without a window.
func selBox(ax, ay, bx, by float64, w, h int) (x0, y0, x1, y1 int, ok bool) {
	x0 = int(math.Round(math.Min(ax, bx)))
	x1 = int(math.Round(math.Max(ax, bx)))
	y0 = int(math.Round(math.Min(ay, by)))
	y1 = int(math.Round(math.Max(ay, by)))
	x0, x1 = max(0, min(x0, w)), max(0, min(x1, w))
	y0, y1 = max(0, min(y0, h)), max(0, min(y1, h))
	return x0, y0, x1, y1, x1 > x0 && y1 > y0
}

// measureReadout formats the selection as the bottom label and the clipboard
// text: the size in source image pixels ("image", exact and independent of the
// display zoom), the size it occupies on screen ("screen"), and the current
// magnification. It is empty when there is nothing to report.
func (v *viewer) measureReadout() string {
	x0, y0, x1, y1, ok := v.measureRect()
	if !ok {
		return ""
	}
	iw, ih := x1-x0, y1-y0
	sx0, sy0 := v.imageToScreen(float64(x0), float64(y0))
	sx1, sy1 := v.imageToScreen(float64(x1), float64(y1))
	sw := int(math.Round(math.Abs(sx1 - sx0)))
	sh := int(math.Round(math.Abs(sy1 - sy0)))
	return fmt.Sprintf("image %dx%d   screen %dx%d   (%d%%)", iw, ih, sw, sh, int(math.Round(v.zoom*100)))
}

// drawMeasureRect paints the selection over the image: a faint fill and a solid
// outline, snapped to whole source pixels so the outline sits on pixel
// boundaries.
func (v *viewer) drawMeasureRect(screen *ebiten.Image) {
	if !v.measure {
		return
	}
	x0, y0, x1, y1, ok := v.measureRect()
	if !ok {
		return
	}
	sx0, sy0 := v.imageToScreen(float64(x0), float64(y0))
	sx1, sy1 := v.imageToScreen(float64(x1), float64(y1))
	left, top := math.Min(sx0, sx1), math.Min(sy0, sy1)
	rw, rh := math.Abs(sx1-sx0), math.Abs(sy1-sy0)
	fill := color.RGBA{A: 128} // black, 50%: dims the selection without glare
	line := color.RGBA{R: 125, G: 190, B: 255, A: 230}
	vector.FillRect(screen, float32(left), float32(top), float32(rw), float32(rh), fill, false)
	vector.StrokeRect(screen, float32(left), float32(top), float32(rw), float32(rh), 1, line, false)
}

// drawMeasureReadout paints the measure hint at the bottom centre, the same
// spot as a tooltip. The finished readout is not kept here: it is announced by
// tooltip on release, right as the mode closes.
func (v *viewer) drawMeasureReadout(screen *ebiten.Image) {
	if !v.measure {
		return
	}
	// while the finger is down the box speaks for itself; otherwise the hint
	if v.measDragging {
		return
	}
	text := "measure: drag a rectangle"
	w, h := v.viewSize(screen.Bounds().Dx(), screen.Bounds().Dy())
	key := fmt.Sprintf("%d|%d|%s", helpBodySize(h), w, text)
	if v.measImg == nil || v.measKey != key {
		if img := renderTooltip(helpBodySize(h), text, w-16, nil); img != nil {
			if v.measImg != nil {
				v.measImg.Dispose() // the old panel's texture, not the frame's
			}
			v.measImg = ebiten.NewImageFromImage(img)
			v.measKey = key
		}
	}
	if v.measImg == nil {
		ebitenutil.DebugPrintAt(screen, text, 8, h-16) // no system font: last resort
		return
	}
	op := &ebiten.DrawImageOptions{}
	b := v.measImg.Bounds()
	op.GeoM.Translate(float64((w-b.Dx())/2), float64(h-b.Dy()-8))
	screen.DrawImage(v.measImg, op)
}

// screenToImage maps a window point to source image pixel coordinates with the
// origin at the image's top-left, the inverse of the view transform. ok is
// false with no image or a degenerate transform.
func (v *viewer) screenToImage(x, y int) (float64, float64, bool) {
	if v.img == nil {
		return 0, 0, false
	}
	b := v.img.Bounds()
	ix, iy, ok := screenToImagePt(v.imageOp().GeoM, float64(x), float64(y))
	if !ok {
		return 0, 0, false
	}
	return ix - float64(b.Min.X), iy - float64(b.Min.Y), true
}

// screenToImagePt inverts a view transform: a window point to a source image
// pixel. It is pure math, so the inverse — the heart of the measurement — can be
// unit-tested against viewMatrix without a window or an image.
func screenToImagePt(gm ebiten.GeoM, x, y float64) (float64, float64, bool) {
	if !gm.IsInvertible() {
		return 0, 0, false
	}
	gm.Invert()
	ix, iy := gm.Apply(x, y)
	return ix, iy, true
}

// imageToScreen maps source image pixel coordinates back to the window, the
// forward view transform.
func (v *viewer) imageToScreen(ix, iy float64) (float64, float64) {
	b := v.img.Bounds()
	return v.imageOp().GeoM.Apply(ix+float64(b.Min.X), iy+float64(b.Min.Y))
}
