//go:build gui

package main

import (
	"image"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

// A picture that fits one texture stays one tile; anything larger is split
// on the dimension that overflows, at the smallest driver limit (4096).
func TestTileGrid(t *testing.T) {
	cases := []struct {
		w, h               int
		cols, rows, tw, th int
	}{
		{1200, 800, 1, 1, 1200, 800},
		{4096, 4096, 1, 1, 4096, 4096},
		{4097, 1, 2, 1, 4096, 4096},
		{1200, 25541, 1, 7, 4096, 4096},  // the WeChat long screenshot
		{44000, 2000, 11, 1, 4096, 4096}, // a panorama
		{44000, 25541, 11, 7, 4096, 4096},
	}
	for _, c := range cases {
		cols, rows, tw, th := tileGrid(c.w, c.h)
		if cols != c.cols || rows != c.rows || tw != c.tw || th != c.th {
			t.Errorf("tileGrid(%d, %d) = %d cols, %d rows, %dx%d; want %d, %d, %dx%d",
				c.w, c.h, cols, rows, tw, th, c.cols, c.rows, c.tw, c.th)
		}
	}
}

// The tiles must cover the picture exactly: every rect inside the bounds, no
// gap and no overlap, so no pixel is dropped or drawn twice.
func TestTileRectsCoverPicture(t *testing.T) {
	sizes := []image.Point{{1200, 25541}, {44000, 2000}, {4097, 4097}, {1, 1}}
	for _, size := range sizes {
		w, h := size.X, size.Y
		cols, rows, _, _ := tileGrid(w, h)
		picture := image.Rect(0, 0, w, h)
		area := 0
		for row := 0; row < rows; row++ {
			for col := 0; col < cols; col++ {
				r := tileRect(w, h, col, row)
				if !r.In(picture) {
					t.Fatalf("%dx%d tile (%d,%d) = %v is outside %v", w, h, col, row, r, picture)
				}
				if r.Empty() {
					t.Fatalf("%dx%d tile (%d,%d) is empty", w, h, col, row)
				}
				area += r.Dx() * r.Dy()
				if col > 0 {
					left := tileRect(w, h, col-1, row)
					if left.Max.X != r.Min.X {
						t.Fatalf("%dx%d tiles %d and %d do not abut horizontally", w, h, col-1, col)
					}
				}
				if row > 0 {
					up := tileRect(w, h, col, row-1)
					if up.Max.Y != r.Min.Y {
						t.Fatalf("%dx%d tiles %d and %d do not abut vertically", w, h, row-1, row)
					}
				}
			}
		}
		if area != w*h {
			t.Fatalf("%dx%d tiles cover %d pixels, want %d", w, h, area, w*h)
		}
	}
}

// tileOrigin is the rect origin of the tile, which is what Draw relies on to
// place each tile in the picture.
func TestTileOriginMatchesRect(t *testing.T) {
	const w, h = 44000, 25541
	p := &pic{w: w, h: h}
	p.cols, p.rows, p.tw, p.th = tileGrid(w, h)
	for i := 0; i < p.cols*p.rows; i++ {
		tx, ty := p.tileOrigin(i)
		r := tileRect(w, h, i%p.cols, i/p.cols)
		if tx != r.Min.X || ty != r.Min.Y {
			t.Fatalf("tile %d origin = (%d, %d), want (%d, %d)", i, tx, ty, r.Min.X, r.Min.Y)
		}
	}
}

// The tile transform must translate the tile into the picture first and apply
// the view transform after, otherwise zoom, rotation and pan would drift by
// the tile offset.
func TestTileGeoM(t *testing.T) {
	var view ebiten.GeoM
	view.Scale(2, 3)
	view.Translate(5, 7)

	gm := tileGeoM(view, 10, 20)
	gotX, gotY := gm.Apply(1, 2)
	// picture point = (11, 22) -> scaled (22, 66) -> translated (27, 73)
	if gotX != 27 || gotY != 73 {
		t.Fatalf("Apply(1, 2) = (%v, %v), want (27, 73)", gotX, gotY)
	}
}

func TestPicBounds(t *testing.T) {
	p := &pic{w: 1200, h: 25541}
	if got, want := p.Bounds(), image.Rect(0, 0, 1200, 25541); got != want {
		t.Fatalf("Bounds = %v, want %v", got, want)
	}
}

// newPic must split a picture taller than one texture into stacked tiles and
// keep every tile within the safe limit. This builds real ebiten images (the
// upload is deferred, so no window is needed) to cover the copy path too.
func TestNewPicSplitsTallImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 9000))
	for i := range src.Pix {
		src.Pix[i] = byte(i)
	}
	p := newPic(src)
	if got, want := p.Bounds(), image.Rect(0, 0, 3, 9000); got != want {
		t.Fatalf("Bounds = %v, want %v", got, want)
	}
	if p.cols != 1 || p.rows != 3 {
		t.Fatalf("grid = %dx%d, want 1x3", p.cols, p.rows)
	}
	if len(p.tiles) != 3 {
		t.Fatalf("tiles = %d, want 3", len(p.tiles))
	}
	for i, tile := range p.tiles {
		if b := tile.Bounds(); b.Dx() > tileSize || b.Dy() > tileSize {
			t.Fatalf("tile %d is %v, larger than %d", i, b, tileSize)
		}
	}
}
