//go:build gui

// Tiling for pictures bigger than one GPU texture.
//
// Ebitengine backs every ebiten.Image with a texture and panics when either
// side exceeds the driver's limit: floorPowerOf2(GL_MAX_TEXTURE_SIZE), 16384
// on most GPUs and 4096 on older ones. A phone screenshot or a captured web
// page easily passes that in one dimension (1200x25541 is a typical WeChat
// long screenshot), so a picture is split into a grid of textures that fit and
// Draw replays the whole-picture transform once per tile.
package main

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/colorm"
)

// tileSize is the largest side of one texture. 4096 is the smallest limit
// reported by the drivers imgo targets, so a grid built with it fits
// everywhere; the extra tiles it costs are cheap to draw.
const tileSize = 4096

// pic is a picture ready to draw. An ordinary image is a single 1x1 tile; a
// picture too big for one texture is a grid of tiles in row-major order.
type pic struct {
	w, h  int
	tiles []*ebiten.Image
	cols  int
	rows  int
	tw    int // tile size in picture pixels; the last column/row may be smaller
	th    int
}

// newPic uploads src to the GPU, splitting it into tiles when it is larger
// than one texture can hold. Each tile is copied once, so the decoded source
// can be released as soon as newPic returns.
func newPic(src image.Image) *pic {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	p := &pic{w: w, h: h}
	p.cols, p.rows, p.tw, p.th = tileGrid(w, h)
	if p.cols == 1 && p.rows == 1 {
		p.tiles = []*ebiten.Image{ebiten.NewImageFromImage(src)}
		return p
	}
	p.tiles = make([]*ebiten.Image, 0, p.cols*p.rows)
	for row := 0; row < p.rows; row++ {
		for col := 0; col < p.cols; col++ {
			r := tileRect(w, h, col, row)
			tile := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
			draw.Draw(tile, tile.Bounds(), src, b.Min.Add(r.Min), draw.Src)
			p.tiles = append(p.tiles, ebiten.NewImageFromImage(tile))
		}
	}
	return p
}

// tileGrid is the grid a w x h picture is split into: columns, rows and the
// tile size. A picture that fits one texture stays one tile, which keeps the
// common case a single upload.
func tileGrid(w, h int) (cols, rows, tw, th int) {
	if w <= tileSize && h <= tileSize {
		return 1, 1, w, h
	}
	return (w + tileSize - 1) / tileSize, (h + tileSize - 1) / tileSize, tileSize, tileSize
}

// tileRect is the part of a w x h picture covered by the tile at (col, row).
// Every picture pixel belongs to exactly one tile.
func tileRect(w, h, col, row int) image.Rectangle {
	x0, y0 := col*tileSize, row*tileSize
	return image.Rect(x0, y0, min(x0+tileSize, w), min(y0+tileSize, h))
}

// tileOrigin is the picture position of the top-left corner of tile i.
func (p *pic) tileOrigin(i int) (int, int) {
	return (i % p.cols) * p.tw, (i / p.cols) * p.th
}

// Bounds reports the picture's size, matching what ebiten.Image.Bounds would
// have returned for the whole picture.
func (p *pic) Bounds() image.Rectangle {
	return image.Rect(0, 0, p.w, p.h)
}

// Draw blits the picture with op. Each tile gets the same transform with its
// own origin prepended, so rotation, mirroring, zoom and pan apply to the
// picture as a whole rather than to each tile.
func (p *pic) Draw(dst *ebiten.Image, op *ebiten.DrawImageOptions) {
	if op == nil {
		op = &ebiten.DrawImageOptions{}
	}
	for i, tile := range p.tiles {
		tx, ty := p.tileOrigin(i)
		op2 := *op
		op2.GeoM = tileGeoM(op.GeoM, tx, ty)
		dst.DrawImage(tile, &op2)
	}
}

// DrawColorM is Draw through a colour matrix, used for the negative and the
// brightness adjustment.
func (p *pic) DrawColorM(dst *ebiten.Image, cm colorm.ColorM, op *colorm.DrawImageOptions) {
	if op == nil {
		op = &colorm.DrawImageOptions{}
	}
	for i, tile := range p.tiles {
		tx, ty := p.tileOrigin(i)
		op2 := *op
		op2.GeoM = tileGeoM(op.GeoM, tx, ty)
		colorm.DrawImage(dst, tile, cm, &op2)
	}
}

// tileGeoM maps a tile's own pixels to the screen: translate the tile to its
// place in the picture, then apply the picture-wide view transform. ebiten's
// GeoM.Concat multiplies on the left, so this is view * translate.
func tileGeoM(view ebiten.GeoM, tx, ty int) ebiten.GeoM {
	gm := ebiten.GeoM{}
	gm.Translate(float64(tx), float64(ty))
	gm.Concat(view)
	return gm
}

// At returns the pixel at (x, y) in picture coordinates, reading back from the
// tile that holds it. Like ebiten.Image.At it reports premultiplied alpha.
func (p *pic) At(x, y int) color.Color {
	if x < 0 || y < 0 || x >= p.w || y >= p.h {
		return color.RGBA{}
	}
	col, row := x/p.tw, y/p.th
	return p.tiles[row*p.cols+col].At(x-col*p.tw, y-row*p.th)
}
