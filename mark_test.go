//go:build gui

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

// TestUniquePath pins the auto-rename: a free name is returned untouched, a
// taken one gets -1, -2, ... inserted before the extension (so the suffix the
// decoders key on survives), and a name with no extension still gets a clean
// counter.
func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	if got, want := uniquePath(dir, "a.png"), filepath.Join(dir, "a.png"); got != want {
		t.Errorf("uniquePath(free) = %q, want %q", got, want)
	}
	writeIn(t, dir, "a.png")
	if got, want := uniquePath(dir, "a.png"), filepath.Join(dir, "a-1.png"); got != want {
		t.Errorf("uniquePath(1 taken) = %q, want %q", got, want)
	}
	writeIn(t, dir, "a-1.png")
	if got, want := uniquePath(dir, "a.png"), filepath.Join(dir, "a-2.png"); got != want {
		t.Errorf("uniquePath(2 taken) = %q, want %q", got, want)
	}
	if got, want := uniquePath(dir, "noext"), filepath.Join(dir, "noext"); got != want {
		t.Errorf("uniquePath(noext free) = %q, want %q", got, want)
	}
	writeIn(t, dir, "noext")
	if got, want := uniquePath(dir, "noext"), filepath.Join(dir, "noext-1"); got != want {
		t.Errorf("uniquePath(noext taken) = %q, want %q", got, want)
	}
}

// TestMarkCopy: "m" copies the image into the imgo/ subdirectory beside it,
// leaving the original and the view in place; a second copy of the same file
// is auto-renamed instead of clobbering the first; and because imgo/ is a
// directory, the image listing never picks up its contents.
func TestMarkCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(src, []byte("pixels"), 0o640); err != nil {
		t.Fatal(err)
	}
	v := &viewer{files: []string{src}}
	if err := v.markCurrent(false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != "pixels" {
		t.Fatalf("source changed: %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, markDirName, "pic.png")); err != nil || string(b) != "pixels" {
		t.Fatalf("mark copy: %q, %v", b, err)
	}
	if v.idx != 0 || len(v.files) != 1 {
		t.Errorf("copy changed the view: idx=%d files=%v", v.idx, v.files)
	}
	if want := "copied to " + markDirName + "/pic.png"; v.tipText != want {
		t.Errorf("tip = %q, want %q", v.tipText, want)
	}

	if err := v.markCurrent(false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, markDirName, "pic-1.png")); err != nil || string(b) != "pixels" {
		t.Fatalf("second mark (auto-renamed): %q, %v", b, err)
	}

	if got := dirImages(dir); len(got) != 1 || got[0] != src {
		t.Errorf("dirImages = %v, want just %q", got, src)
	}
}

// TestMarkMoveLast: "M" takes the file out of the directory and, with nothing
// left to show, ends the session (ebiten.Termination) exactly like deleting the
// last image. The file itself lands intact in imgo/.
func TestMarkMoveLast(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "only.png")
	if err := os.WriteFile(src, []byte("solo"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := &viewer{files: []string{src}}
	if err := v.markCurrent(true); err != ebiten.Termination {
		t.Fatalf("markCurrent(move) = %v, want Termination", err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("source still there: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, markDirName, "only.png")); err != nil || string(b) != "solo" {
		t.Fatalf("mark move: %q, %v", b, err)
	}
	if len(v.files) != 0 {
		t.Errorf("files = %v, want empty", v.files)
	}
}
