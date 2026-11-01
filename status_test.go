//go:build gui

package main

import (
	"testing"
)

// TestStatusPrefix pins the top status line's "[index/total][size][WxH]" shape:
// the resolution is the last bracket group, right of the file size, as the user
// reads it — "[1/822][2.3M][123x123]".
func TestStatusPrefix(t *testing.T) {
	cases := []struct {
		idx, total int
		size       string
		w, h       int
		want       string
	}{
		{0, 822, "2.3M", 123, 123, "[1/822][2.3M][123x123]"},
		{41, 822, "2.3M", 1920, 1080, "[42/822][2.3M][1920x1080]"},
		{0, 3, "45K", 640, 480, "[1/3][45K][640x480]"},
		{0, 3, "98B", 1, 1, "[1/3][98B][1x1]"},
	}
	for _, c := range cases {
		if got := statusPrefix(c.idx, c.total, c.size, c.w, c.h, ""); got != c.want {
			t.Errorf("statusPrefix(%d, %d, %q, %d, %d, %q) = %q, want %q", c.idx, c.total, c.size, c.w, c.h, "", got, c.want)
		}
	}
}
