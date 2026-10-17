//go:build gui

package main

import (
	"image/color"
	"testing"
)

// TestCheckerPattern pins the checkerboard grid math: light at the origin,
// alternating on the 16px cell grid, period restarting on both axes, and a
// tile size that guarantees seamless blitting.
func TestCheckerPattern(t *testing.T) {
	if checkerCell != 16 {
		t.Fatalf("checkerCell = %d, want 16", checkerCell)
	}
	if (checkerTileN/checkerCell)%2 != 0 {
		t.Fatalf("checkerTileN=%d is not a multiple of 2*checkerCell: grid would seam", checkerTileN)
	}
	p := checkerPattern()
	if b := p.Bounds(); b.Dx() != checkerTileN || b.Dy() != checkerTileN {
		t.Fatalf("pattern bounds = %v, want %dx%d", b, checkerTileN, checkerTileN)
	}

	light := color.RGBA{255, 255, 255, 255}
	dark := color.RGBA{204, 204, 204, 255}
	cases := []struct {
		x, y int
		want color.RGBA
	}{
		{0, 0, light},     // origin: light square
		{15, 15, light},   // end of first cell
		{16, 0, dark},     // next cell over
		{0, 16, dark},     // next cell down
		{16, 16, light},   // diagonal parity: back to light
		{32, 0, light},    // period restart on x
		{0, 32, light},    // period restart on y
		{31, 1, dark},     // interior of the (1,0) dark cell
		{240, 0, dark},    // last cell column of the tile (240/16 = 15, odd)
		{255, 255, light}, // 15+15 = 30, even
	}
	for _, c := range cases {
		if got := p.RGBAAt(c.x, c.y); got != c.want {
			t.Errorf("RGBAAt(%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}
