//go:build gui && darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// moveToTrash moves the file into ~/.Trash — the boot volume's trash — so a
// delete is reversible from the Finder instead of being final. A file on
// another volume cannot be renamed into it, so that case copies and unlinks.
// (The per-volume .Trashes directories and Finder's "Put Back" metadata are
// not reproduced; the file is still recoverable from the Trash.)
func moveToTrash(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	trash := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	base := filepath.Base(abs)
	dst := filepath.Join(trash, base)
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		}
		dst = filepath.Join(trash, fmt.Sprintf("%s.%d", base, i))
	}
	if err := os.Rename(abs, dst); err == nil {
		return nil
	}
	// different filesystem: stream a copy over, then drop the original
	if err := copyFile(abs, dst); err != nil {
		os.Remove(dst)
		return err
	}
	if err := os.Remove(abs); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}
