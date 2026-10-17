//go:build gui

package main

import (
	"math"
	"testing"
)

// TestViewMatrix pins the draw matrix: every view state must land the image
// exactly in the rectangle (0,0)-(rotated w, rotated h) before zoom and pan
// are applied, so that fit, centering and the checkerboard — all sized from
// imgSize — agree with what is actually drawn.
func TestViewMatrix(t *testing.T) {
	const w, h = 4, 3
	cases := []struct {
		name         string
		rot          int
		fx, fy       bool
		zoom, ox, oy float64
		sx, sy       float64 // source pixel
		wantX, wantY float64
	}{
		// upright: identity plus zoom and position
		{"upright origin", 0, false, false, 1, 0, 0, 0, 0, 0, 0},
		{"upright far corner", 0, false, false, 1, 0, 0, w, h, 4, 3},
		{"zoom + pan", 0, false, false, 2, 10, 20, w, h, 10 + 2*4, 20 + 2*3},
		// 90° clockwise: (u,v) -> (h-v, u); the rect swaps to 3x4
		{"90cw origin", 1, false, false, 1, 0, 0, 0, 0, h, 0},
		{"90cw far corner", 1, false, false, 1, 0, 0, w, h, 0, w},
		{"90cw wraps five turns", 5, false, false, 1, 0, 0, 0, 0, h, 0},
		// 180°: (u,v) -> (w-u, h-v)
		{"180 origin", 2, false, false, 1, 0, 0, 0, 0, w, h},
		// 90° counter-clockwise: (u,v) -> (v, w-u)
		{"90ccw origin", 3, false, false, 1, 0, 0, 0, 0, 0, w},
		{"90ccw far corner", 3, false, false, 1, 0, 0, w, h, h, 0},
		// mirrors act in image space, before the rotation
		{"mirror H origin", 0, true, false, 1, 0, 0, 0, 0, w, 0},
		{"mirror V origin", 0, false, true, 1, 0, 0, 0, 0, 0, h},
		{"mirror HV origin", 0, true, true, 1, 0, 0, 0, 0, w, h},
		{"mirror H + 90cw", 1, true, false, 1, 0, 0, 0, 0, h, w},
	}
	for _, c := range cases {
		op := viewMatrix(w, h, c.rot, c.fx, c.fy, c.zoom, c.ox, c.oy)
		gx, gy := op.GeoM.Apply(c.sx, c.sy)
		if math.Abs(gx-c.wantX) > 1e-6 || math.Abs(gy-c.wantY) > 1e-6 {
			t.Errorf("%s: Apply(%v,%v) = (%v,%v), want (%v,%v)",
				c.name, c.sx, c.sy, gx, gy, c.wantX, c.wantY)
		}
	}
}
