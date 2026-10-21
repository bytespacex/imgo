// SVG rasterisation.
//
// okSVG parses the vector tree, rasterx strokes and fills it. rasterx's own
// ScannerGV blits the whole destination for every path, because x/image's
// vector rasterizer keeps its alpha mask at the image origin. An SVG made of
// thousands of small shapes — a heat map, an icon grid — therefore costs one
// full-canvas blend per shape; a file that should open at once takes a minute.
//
// svgScanner below buffers one path at a time and sizes both the mask and the
// destination sub-image to that path's own bounds, so only the touched pixels
// are blended. It reproduces ScannerGV's colour and clip handling; source
// coordinates stay absolute, which is what rasterx's gradients expect.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

const svgMaxDim = 2048

// svgFile rasterizes an SVG with okSVG. SVG is vector; render up to maxDim.
func svgFile(path string) (image.Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	icon, err := oksvg.ReadIconStream(bytes.NewReader(data), oksvg.IgnoreErrorMode)
	if err != nil {
		return nil, fmt.Errorf("svg: %w", err)
	}

	w, h := int(icon.ViewBox.W), int(icon.ViewBox.H)
	if w <= 0 || h <= 0 {
		w, h = 256, 256
	}
	if w > svgMaxDim || h > svgMaxDim {
		scale := float64(svgMaxDim) / float64(max(w, h))
		w, h = int(float64(w)*scale), int(float64(h)*scale)
	}
	icon.SetTarget(0, 0, float64(w), float64(h))

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sc := newSVGScanner(dst, w, h)
	dasher := rasterx.NewDasher(w, h, sc)

	// Strokes and their miter joins reach past the path's centre line. Without
	// room for them the bbox would cut thick corners off, so pad by the widest
	// the stroke can get in device pixels. LineWidth defaults to 2 even for
	// fill-only paths, and okSVG does not expose whether a stroke is set, so
	// the padding is harmless when there is none.
	scale := svgScale(icon, w, h)
	for i := range icon.SVGPaths {
		p := &icon.SVGPaths[i]
		sc.margin = 1 + p.LineWidth*scale*0.5*math.Max(1, p.MiterLimit)
		p.DrawTransformed(dasher, 1, icon.Transform)
	}
	// oksvg drops <text>; fill the labels in afterwards with system fonts.
	svgText(data, dst, w, h, icon.Transform)
	return dst, nil
}

// svgScale is the larger of the horizontal and vertical scale SetTarget
// applied, used to convert user-unit stroke widths to device pixels.
func svgScale(icon *oksvg.SvgIcon, w, h int) float64 {
	sx, sy := 1.0, 1.0
	if icon.ViewBox.W != 0 {
		sx = math.Abs(float64(w) / icon.ViewBox.W)
	}
	if icon.ViewBox.H != 0 {
		sy = math.Abs(float64(h) / icon.ViewBox.H)
	}
	scale := math.Max(sx, sy)
	if math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return 1
	}
	return scale
}

// svgPoint is one buffered path command: a move-to when move is true, a
// line-to otherwise. Fills and strokes are flattened to lines before reaching
// the scanner, so no curve commands are needed.
type svgPoint struct {
	p    fixed.Point26_6
	move bool
}

// svgScanner implements rasterx.Scanner. It collects the path and, on Draw,
// rasterizes it into dst.Bounds()-sized space shifted by the path's own pixel
// origin, then blends only that sub-image.
type svgScanner struct {
	dst                    *image.RGBA
	w, h                   int     // full canvas, for clipping the sub-image
	margin                 float64 // anti-aliasing and stroke padding, in pixels
	r                      vector.Rasterizer
	src                    image.Image
	clip                   image.Rectangle // empty: no clip
	pts                    []svgPoint
	minX, minY, maxX, maxY fixed.Int26_6
}

const svgExtentMax = fixed.Int26_6(math.MaxInt32)

func newSVGScanner(dst *image.RGBA, w, h int) *svgScanner {
	s := &svgScanner{
		dst:  dst,
		w:    w,
		h:    h,
		src:  &image.Uniform{C: color.NRGBA{255, 0, 0, 255}},
		clip: image.ZR,
	}
	s.resetExtent()
	return s
}

func (s *svgScanner) resetExtent() {
	s.minX, s.minY, s.maxX, s.maxY = svgExtentMax, svgExtentMax, -svgExtentMax, -svgExtentMax
}

func (s *svgScanner) see(p fixed.Point26_6) {
	if p.X < s.minX {
		s.minX = p.X
	}
	if p.Y < s.minY {
		s.minY = p.Y
	}
	if p.X > s.maxX {
		s.maxX = p.X
	}
	if p.Y > s.maxY {
		s.maxY = p.Y
	}
}

func (s *svgScanner) Start(p fixed.Point26_6) {
	s.pts = append(s.pts, svgPoint{p, true})
	s.see(p)
}

func (s *svgScanner) Line(p fixed.Point26_6) {
	s.pts = append(s.pts, svgPoint{p, false})
	s.see(p)
}

func (s *svgScanner) GetPathExtent() fixed.Rectangle26_6 {
	return fixed.Rectangle26_6{
		Min: fixed.Point26_6{X: s.minX, Y: s.minY},
		Max: fixed.Point26_6{X: s.maxX, Y: s.maxY},
	}
}

func (s *svgScanner) SetBounds(w, h int) { s.w, s.h = w, h }

// SetWinding is a no-op: x/image's rasterizer only fills non-zero, just as it
// does behind rasterx's own ScannerGV.
func (s *svgScanner) SetWinding(bool) {}

func (s *svgScanner) SetClip(r image.Rectangle) { s.clip = r }

func (s *svgScanner) Clear() {
	s.pts = s.pts[:0]
	s.resetExtent()
}

func (s *svgScanner) SetColor(c interface{}) {
	switch c := c.(type) {
	case color.Color:
		s.src = &image.Uniform{C: c}
	case rasterx.ColorFunc:
		s.src = svgColorFunc(c)
	}
}

// svgColorFunc adapts rasterx's gradient function to image.Image. At receives
// absolute pixel coordinates because Draw is called with the sub-image origin.
type svgColorFunc rasterx.ColorFunc

func (f svgColorFunc) ColorModel() color.Model { return color.RGBAModel }
func (f svgColorFunc) Bounds() image.Rectangle {
	const inf = 1 << 24
	return image.Rect(-inf, -inf, inf, inf)
}
func (f svgColorFunc) At(x, y int) color.Color { return rasterx.ColorFunc(f)(x, y) }

// Draw rasterizes the buffered path into the smallest sub-image that can hold
// it. The vector mask is indexed from the origin, so path coordinates are
// shifted by the sub-image origin and the source is told the same origin, which
// keeps gradients and clips in absolute coordinates.
func (s *svgScanner) Draw() {
	if len(s.pts) == 0 || s.minX > s.maxX {
		return
	}
	x0 := int(math.Floor(float64(s.minX)/64 - s.margin))
	y0 := int(math.Floor(float64(s.minY)/64 - s.margin))
	x1 := int(math.Ceil(float64(s.maxX)/64 + s.margin))
	y1 := int(math.Ceil(float64(s.maxY)/64 + s.margin))
	r := image.Rect(x0, y0, x1, y1).Intersect(image.Rect(0, 0, s.w, s.h))
	if !s.clip.Empty() {
		r = r.Intersect(s.clip)
	}
	if r.Empty() {
		s.pts = s.pts[:0]
		return
	}

	s.r.Reset(r.Dx(), r.Dy())
	shiftX := fixed.Int26_6(r.Min.X * 64)
	shiftY := fixed.Int26_6(r.Min.Y * 64)
	for _, pt := range s.pts {
		x := float32(pt.p.X-shiftX) / 64
		y := float32(pt.p.Y-shiftY) / 64
		if pt.move {
			s.r.MoveTo(x, y)
		} else {
			s.r.LineTo(x, y)
		}
	}
	sub := s.dst.SubImage(r).(*image.RGBA)
	s.r.Draw(sub, r, s.src, r.Min)
	s.pts = s.pts[:0]
}
