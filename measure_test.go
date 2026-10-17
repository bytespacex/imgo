//go:build gui

package main

import (
	"math"
	"testing"
)

// TestSelBox checks the snapping and clamping behind the measurement: fractional
// drag corners round to whole pixels, the box is clamped to the picture, and an
// empty selection is reported as unusable.
func TestSelBox(t *testing.T) {
	if x0, y0, x1, y1, ok := selBox(10.4, 20.6, 110.4, 120.4, 1000, 1000); !ok || x0 != 10 || y0 != 21 || x1 != 110 || y1 != 120 {
		t.Errorf("snap: got (%d,%d,%d,%d,%v), want (10,21,110,120,true)", x0, y0, x1, y1, ok)
	}
	// corners may be dragged in any order: min/max must normalize them
	if x0, y0, x1, y1, ok := selBox(110, 120, 10, 20, 1000, 1000); !ok || x0 != 10 || y0 != 20 || x1 != 110 || y1 != 120 {
		t.Errorf("reverse: got (%d,%d,%d,%d,%v), want (10,20,110,120,true)", x0, y0, x1, y1, ok)
	}
	// a drag past the edge clamps to the picture
	if x0, y0, x1, y1, ok := selBox(-50, -50, 2000, 2000, 100, 50); !ok || x0 != 0 || y0 != 0 || x1 != 100 || y1 != 50 {
		t.Errorf("clamp: got (%d,%d,%d,%d,%v), want (0,0,100,50,true)", x0, y0, x1, y1, ok)
	}
	// a click without a drag spans nothing
	if _, _, _, _, ok := selBox(5, 5, 5, 5, 100, 100); ok {
		t.Error("zero-size selection reported usable")
	}
}

// TestScreenToImage checks the inverse view transform the measurement relies on:
// a known view maps back to the expected source pixel, including zoom, pan and a
// rotation (which makes the mapping anything but obvious).
func TestScreenToImage(t *testing.T) {
	// zoom 2: screen == image * 2, so screen (50,100) is image (25,50)
	if x, y, ok := screenToImagePt(viewMatrix(200, 100, 0, false, false, 2, 0, 0).GeoM, 50, 100); !ok ||
		x != 25 || y != 50 {
		t.Errorf("zoom: got (%v,%v,%v), want (25,50,true)", x, y, ok)
	}
	// pan (10,20): that offset pixel is the image origin
	if x, y, ok := screenToImagePt(viewMatrix(200, 100, 0, false, false, 1, 10, 20).GeoM, 10, 20); !ok ||
		math.Abs(x) > 1e-9 || math.Abs(y) > 1e-9 {
		t.Errorf("pan: got (%v,%v,%v), want (0,0,true)", x, y, ok)
	}
	// 90 degrees clockwise maps image (u,v) -> (100-v, u), so the image origin
	// sits at screen (100,0)
	if x, y, ok := screenToImagePt(viewMatrix(200, 100, 1, false, false, 1, 0, 0).GeoM, 100, 0); !ok ||
		math.Abs(x) > 1e-9 || math.Abs(y) > 1e-9 {
		t.Errorf("rot90: got (%v,%v,%v), want (0,0,true)", x, y, ok)
	}
}
