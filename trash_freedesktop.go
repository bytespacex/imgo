//go:build gui && !darwin && !windows

package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// moveToTrash moves a file into the freedesktop.org trash
// ($XDG_DATA_HOME/Trash, usually ~/.local/share/Trash) instead of destroying
// it, so deleting an image is never final: file managers can restore it from
// there. The .trashinfo record is written first — an orphan record is
// harmless, an untracked trashed file cannot be restored. A file on another
// filesystem cannot be renamed across, so that case copies and unlinks.
func moveToTrash(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		data = filepath.Join(home, ".local", "share")
	}
	filesDir := filepath.Join(data, "Trash", "files")
	infoDir := filepath.Join(data, "Trash", "info")
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return err
	}
	// keep the original name when the trash has no such file yet, otherwise
	// append a counter (the info record is named after the final name)
	base := filepath.Base(abs)
	dst := filepath.Join(filesDir, base)
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		}
		dst = filepath.Join(filesDir, fmt.Sprintf("%s.%d", base, i))
	}
	info := filepath.Join(infoDir, filepath.Base(dst)+".trashinfo")
	rec := "[Trash Info]\nPath=" + (&url.URL{Path: abs}).String() +
		"\nDeletionDate=" + time.Now().Format("2006-01-02T15:04:05") + "\n"
	if err := os.WriteFile(info, []byte(rec), 0o600); err != nil {
		return err
	}
	if err := os.Rename(abs, dst); err == nil {
		return nil
	} else if !crossDevice(err) {
		os.Remove(info)
		return err
	}
	// cross-device: stream a copy over, then drop the original
	if err := copyFile(abs, dst); err != nil {
		os.Remove(dst)
		os.Remove(info)
		return err
	}
	if err := os.Remove(abs); err != nil {
		os.Remove(dst)
		os.Remove(info)
		return err
	}
	return nil
}
