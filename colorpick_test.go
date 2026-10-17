//go:build gui

package main

import (
	"math"
	"testing"
)

// TestStraightAlpha pins the un-premultiply used by the colour picker: an
// opaque channel is unchanged, a translucent one is scaled back up (ebiten
// stores premultiplied alpha), the result is clamped, and zero alpha is zero.
func TestStraightAlpha(t *testing.T) {
	cases := []struct{ v, a, want uint8 }{
		{200, 255, 200}, // opaque: untouched
		{128, 128, 255}, // premultiplied by 1/2: back to full
		{64, 64, 255},   // premultiplied by 1/4
		{50, 100, 127},  // 50*255/100 = 127
		{0, 0, 0},       // fully transparent
		{255, 1, 255},   // would overflow: clamped
	}
	for _, c := range cases {
		if got := straight(c.v, c.a); got != c.want {
			t.Errorf("straight(%d, %d) = %d, want %d", c.v, c.a, got, c.want)
		}
	}
}

// TestViewMatrixInverse pins the screen->image mapping the colour picker relies
// on: for any rotation / mirror / zoom / pan, inverting the view matrix and
// applying a projected point returns the source pixel it came from. If this
// drifted, the picker would sample the wrong pixel.
func TestViewMatrixInverse(t *testing.T) {
	const w, h = 40, 20
	src := [][2]float64{{0, 0}, {w, 0}, {0, h}, {w, h}, {10, 7}, {39, 19}}
	for rot := 0; rot < 4; rot++ {
		for _, fx := range []bool{false, true} {
			for _, fy := range []bool{false, true} {
				op := viewMatrix(w, h, rot, fx, fy, 1.7, 13, -5)
				for _, p := range src {
					sx, sy := op.GeoM.Apply(p[0], p[1])
					inv := op.GeoM
					inv.Invert()
					u, v := inv.Apply(sx, sy)
					if math.Abs(u-p[0]) > 1e-6 || math.Abs(v-p[1]) > 1e-6 {
						t.Fatalf("rot=%d fx=%v fy=%v src=(%v,%v): round-trip to (%v,%v)",
							rot, fx, fy, p[0], p[1], u, v)
					}
				}
			}
		}
	}
}
