//go:build gui && !darwin && !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestMoveToTrash covers the normal rename path: the file leaves its
// directory, lands in $XDG_DATA_HOME/Trash/files and gets an info record
// naming its origin; a second file of the same name is suffixed instead of
// overwriting the first trashed one.
func TestMoveToTrash(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	dir := t.TempDir()

	src := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(src, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrash(src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(xdg, "Trash", "files", "photo.png")); err != nil || string(b) != "first" {
		t.Fatalf("trashed copy: %q, %v", b, err)
	}
	info, err := os.ReadFile(filepath.Join(xdg, "Trash", "info", "photo.png.trashinfo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Trash Info]", "Path=", src, "DeletionDate="} {
		if !strings.Contains(string(info), want) {
			t.Errorf("record misses %q:\n%s", want, info)
		}
	}

	src2 := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(src2, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrash(src2); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(xdg, "Trash", "files", "photo.png.1")); err != nil || string(b) != "second" {
		t.Fatalf("suffixed copy: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(xdg, "Trash", "info", "photo.png.1.trashinfo")); err != nil {
		t.Fatalf("suffixed info record: %v", err)
	}
}

// TestMoveToTrashCrossDevice exercises the copy+unlink fallback: the source
// sits on tmpfs (/tmp) while the trash lives on the home disk, so the rename
// fails with EXDEV. Skipped when both happen to share a filesystem.
func TestMoveToTrashCrossDevice(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	xdg := filepath.Join(home, ".cache", fmt.Sprintf("imgo-trash-test-%d", os.Getpid()))
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(xdg) })

	src := filepath.Join(t.TempDir(), "shot.jpg")
	var d1, d2 syscall.Stat_t
	if err := syscall.Stat(filepath.Dir(src), &d1); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat(xdg, &d2); err != nil {
		t.Fatal(err)
	}
	if d1.Dev == d2.Dev {
		t.Skip("source and trash share a filesystem; EXDEV unreachable")
	}
	t.Setenv("XDG_DATA_HOME", xdg)

	if err := os.WriteFile(src, []byte("xdev"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrash(src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	dst := filepath.Join(xdg, "Trash", "files", "shot.jpg")
	if b, err := os.ReadFile(dst); err != nil || string(b) != "xdev" {
		t.Fatalf("trashed copy: %q, %v", b, err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o644 {
		t.Errorf("copied mode = %o, want 644", perm)
	}
	if _, err := os.Stat(filepath.Join(xdg, "Trash", "info", "shot.jpg.trashinfo")); err != nil {
		t.Fatalf("info record: %v", err)
	}
}
