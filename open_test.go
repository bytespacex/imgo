//go:build gui

package main

import (
	"reflect"
	"testing"
)

// TestParseOpenPrograms pins the IMGO_OPEN grammar: comma-separated entries,
// each optionally carrying fixed arguments, blank entries dropped. ~ and $VAR
// expand in every field, and the label is re-emitted with the home directory as
// "~", so the menu never shows an absolute /home/me path.
func TestParseOpenPrograms(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("IMGO_OPEN", "gimp.appimage, foot lf ,,~/pg/gimp/AppRun,$HOME/bin/gee,/home/tester/x/y")
	got := parseOpenPrograms()
	want := []openProgram{
		{label: "gimp.appimage", argv: []string{"gimp.appimage"}},
		{label: "foot lf", argv: []string{"foot", "lf"}},
		{label: "~/pg/gimp/AppRun", argv: []string{"/home/tester/pg/gimp/AppRun"}},
		{label: "~/bin/gee", argv: []string{"/home/tester/bin/gee"}},
		{label: "~/x/y", argv: []string{"/home/tester/x/y"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseOpenPrograms = %#v, want %#v", got, want)
	}

	t.Setenv("IMGO_OPEN", "  ,  ")
	if got := parseOpenPrograms(); len(got) != 0 {
		t.Errorf("blank IMGO_OPEN parsed to %#v, want empty", got)
	}
}

// TestExpandHome pins the home expansion: a leading ~ or ~/ resolves, a bare
// ~ is the home directory, and everything else (including ~ in the middle) is
// left alone.
func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	cases := []struct{ in, want string }{
		{"~", "/home/tester"},
		{"~/pg/gimp/AppRun", "/home/tester/pg/gimp/AppRun"},
		{"$HOME/x", "$HOME/x"}, // ExpandEnv's job, not expandHome's
		{"/abs/path", "/abs/path"},
		{"relative/x", "relative/x"},
		{"a~b", "a~b"},
	}
	for _, c := range cases {
		if got := expandHome(c.in); got != c.want {
			t.Errorf("expandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOpenMatches pins the first-letter locate: an empty filter lists
// everything; a letter locates a path segment or word, so "g" finds the gimp
// AppRun and "l" finds lf inside "foot lf"; no match is empty.
func TestOpenMatches(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("IMGO_OPEN", "gimp.appimage,geeqie,lf,mpv,~/pg/gimp/AppRun,foot lf")
	v := &viewer{}
	cases := []struct {
		filter string
		want   []string
	}{
		{"", []string{"gimp.appimage", "geeqie", "lf", "mpv", "~/pg/gimp/AppRun", "foot lf"}},
		{"g", []string{"gimp.appimage", "geeqie", "~/pg/gimp/AppRun"}},
		{"gi", []string{"gimp.appimage", "~/pg/gimp/AppRun"}},
		{"GE", []string{"geeqie"}}, // case-insensitive
		{"l", []string{"lf", "foot lf"}},
		{"lf", []string{"lf", "foot lf"}},
		{"m", []string{"mpv"}},
		{"x", nil},
	}
	for _, c := range cases {
		v.openFilter = c.filter
		got := v.openMatches()
		names := make([]string, len(got))
		for i, p := range got {
			names[i] = p.label
		}
		if len(names) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(names, c.want) {
			t.Errorf("openMatches(%q) = %v, want %v", c.filter, names, c.want)
		}
	}
}
