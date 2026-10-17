//go:build gui

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeIn(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDirImages: only image files directly inside the dir, sorted by name,
// extension matched case-insensitively, subdirectories skipped even when
// they are named like images.
func TestDirImages(t *testing.T) {
	dir := t.TempDir()
	writeIn(t, dir, "b.png")
	writeIn(t, dir, "a.jpg")
	writeIn(t, dir, "c.TXT")
	writeIn(t, dir, "d.jpeg")
	if err := os.Mkdir(filepath.Join(dir, "sub.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "a.jpg"),
		filepath.Join(dir, "b.png"),
		filepath.Join(dir, "d.jpeg"),
	}
	if got := dirImages(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("dirImages = %v, want %v", got, want)
	}
	if got := dirImages(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("dirImages(missing) = %v, want nil", got)
	}
}

// TestSiblingDirs: the PageUp/PageDown set is the subdirectories that
// directly contain images, in name order — hidden dirs count (hidden image
// files count too), empty dirs and image files in the parent do not.
func TestSiblingDirs(t *testing.T) {
	parent := t.TempDir()
	mkdir := func(name string) {
		t.Helper()
		if err := os.Mkdir(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(".hid")
	writeIn(t, filepath.Join(parent, ".hid"), "x.png")
	mkdir("a")
	writeIn(t, filepath.Join(parent, "a"), "1.jpg")
	mkdir("b") // no images: not part of the set
	mkdir("c")
	writeIn(t, filepath.Join(parent, "c"), "2.webp")
	writeIn(t, parent, "loose.png") // file in the parent: not a dir

	want := []string{
		filepath.Join(parent, ".hid"),
		filepath.Join(parent, "a"),
		filepath.Join(parent, "c"),
	}
	if got := siblingDirs(parent); !reflect.DeepEqual(got, want) {
		t.Errorf("siblingDirs = %v, want %v", got, want)
	}
	if got := siblingDirs(filepath.Join(parent, "missing")); got != nil {
		t.Errorf("siblingDirs(missing) = %v, want nil", got)
	}
}

// TestTooltip pins the notification API state: message stored, deadline
// inside (now, now+tooltipDur].
func TestTooltip(t *testing.T) {
	v := &viewer{}
	v.tooltip("[1/3] ~/photos/shots")
	if v.tipText != "[1/3] ~/photos/shots" {
		t.Errorf("tipText = %q", v.tipText)
	}
	if d := time.Until(v.tipUntil); d <= 0 || d > tooltipDur {
		t.Errorf("deadline %v from now, want within (0, %v]", d, tooltipDur)
	}
}

// TestFileURI: absolute paths become file:// URIs with spaces and
// non-ASCII names percent-escaped.
func TestFileURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/tester/a.png", "file:///home/tester/a.png"},
		{"/home/tester/my pic.png", "file:///home/tester/my%20pic.png"},
		{"/home/tester/café.png", "file:///home/tester/caf%C3%A9.png"},
	}
	for _, c := range cases {
		if got := fileURI(c.in); got != c.want {
			t.Errorf("fileURI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPrettyPath: paths under $HOME display with "~", everything else
// stays absolute — including a sibling whose name merely shares the home
// prefix without the separator.
func TestPrettyPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	cases := []struct{ in, want string }{
		{"/home/tester", "~"},
		{"/home/tester/photos/shots", "~/photos/shots"},
		{"/home/tester2/shots", "/home/tester2/shots"}, // prefix without separator
		{"/etc/hosts", "/etc/hosts"},
		{"relative/path", "relative/path"},
	}
	for _, c := range cases {
		if got := prettyPath(c.in); got != c.want {
			t.Errorf("prettyPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
