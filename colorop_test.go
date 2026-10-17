//go:build gui

package main

import (
	"image/color"
	"math"
	"testing"
)

// closeEnough compares two NRGBA64 channels within a small tolerance: colorm
// normalizes to float32 (un-premultiply, matrix, re-premultiply) and rounds, so
// the last unit or two can differ from the exact integer expectation.
func closeEnough(got, want color.NRGBA64) bool {
	const eps = 4
	return math.Abs(float64(got.R)-float64(want.R)) <= eps &&
		math.Abs(float64(got.G)-float64(want.G)) <= eps &&
		math.Abs(float64(got.B)-float64(want.B)) <= eps &&
		math.Abs(float64(got.A)-float64(want.A)) <= eps
}

// TestColorMInvert pins the negative: RGB flips around mid-grey (out = 1-in)
// and alpha is passed through untouched. The matrix is applied to straight
// alpha, so the expectation is the plain 255-c on each channel.
func TestColorMInvert(t *testing.T) {
	v := &viewer{invert: true, bright: brightNeutral}
	cm := v.viewColorM()
	got := cm.Apply(color.NRGBA{R: 0x33, G: 0x80, B: 0xFF, A: 0x40})
	n := got.(color.NRGBA64)
	want := color.NRGBA64{
		R: (255 - 0x33) * 0x101,
		G: (255 - 0x80) * 0x101,
		B: (255 - 0xFF) * 0x101,
		A: 0x40 * 0x101,
	}
	if !closeEnough(n, want) {
		t.Errorf("invert Apply = %v, want %v", n, want)
	}
}

// TestColorMBrightness pins the brightness scale: RGB is multiplied by the
// factor, alpha is left alone. 2.0 doubles and clamps at white.
func TestColorMBrightness(t *testing.T) {
	v := &viewer{invert: false, bright: 2.0}
	cm := v.viewColorM()
	got := cm.Apply(color.NRGBA{R: 0x40, G: 0x80, B: 0xFF, A: 0x99})
	n := got.(color.NRGBA64)
	want := color.NRGBA64{
		R: 0x80 * 0x101,
		G: 0xFF * 0x101, // 0x80 * 2 clamps to white
		B: 0xFF * 0x101, // 0xFF * 2 clamps to white
		A: 0x99 * 0x101,
	}
	if !closeEnough(n, want) {
		t.Errorf("brightness Apply = %v, want %v", n, want)
	}
}

// TestColorAltered pins the gate that picks the fast path: altered only when
// the negative is on or brightness has actually moved off neutral. A zero-value
// viewer (bright unset) must read as neutral so Draw uses the fast path.
func TestColorAltered(t *testing.T) {
	cases := []struct {
		name   string
		invert bool
		bright float64
		want   bool
	}{
		{"plain", false, brightNeutral, false},
		{"zero-value viewer", false, 0, false},
		{"negative", true, brightNeutral, true},
		{"brighter", false, 1.5, true},
		{"darker", false, 0.5, true},
		{"negative + bright", true, 1.5, true},
	}
	for _, c := range cases {
		v := &viewer{invert: c.invert, bright: c.bright}
		if got := v.colorAltered(); got != c.want {
			t.Errorf("%s: colorAltered = %v, want %v", c.name, got, c.want)
		}
	}
}
