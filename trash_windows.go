//go:build gui && windows

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// moveToTrash sends the file to the Windows Recycle Bin through .NET's
// FileSystem.DeleteFile (present on every supported Windows) instead of
// deleting it outright, so a delete stays reversible from Explorer.
func moveToTrash(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// single-quoted PowerShell literal; a literal quote is doubled
	esc := strings.ReplaceAll(abs, "'", "''")
	script := `Add-Type -AssemblyName Microsoft.VisualBasic; ` +
		`[Microsoft.VisualBasic.FileIO.FileSystem]::DeleteFile('` + esc + `', ` +
		`'OnlyErrorDialogs', 'SendToRecycleBin')`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
