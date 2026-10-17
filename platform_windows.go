//go:build windows

// Windows-specific terminal glue: job-control signals (a no-op) and
// cell-size probing. Counterpart of platform_unix.go.
package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ignoreJobControlSignals is a no-op on Windows: there is no SIGTTOU/SIGTTIN
// job-control concept, so /dev/tty-style access cannot suspend the process.
func ignoreJobControlSignals() {}

// coord / consoleFontInfoEx mirror the Win32 COORD / CONSOLE_FONT_INFOEX that
// GetCurrentConsoleFontEx fills in. fontSize is the character cell size in
// pixels, which is exactly the cell size the caller wants.
type coord struct{ x, y int16 }

type consoleFontInfoEx struct {
	cbSize     uint32
	font       uint32
	fontSize   coord
	fontFamily uint32
	fontWeight uint32
	faceName   [32]uint16
}

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procGetCurrentConsoleFontEx = kernel32.NewProc("GetCurrentConsoleFontEx")
)

// winsizeCell reads the console's current font cell size, the Windows answer to
// the Unix TIOCGWINSZ path. x/sys/windows does not wrap GetCurrentConsoleFontEx,
// so it is called directly. With no real console (redirected stdout, a pipe) it
// reports ok=false and the caller falls back to the cache or the 8x16 default.
func winsizeCell() (w, h int, ok bool) {
	if err := procGetCurrentConsoleFontEx.Find(); err != nil {
		return 0, 0, false
	}
	var mode uint32
	if err := windows.GetConsoleMode(windows.Stdout, &mode); err != nil {
		return 0, 0, false // stdout is not attached to a console
	}
	fi := consoleFontInfoEx{cbSize: uint32(unsafe.Sizeof(consoleFontInfoEx{}))}
	r, _, _ := procGetCurrentConsoleFontEx.Call(uintptr(windows.Stdout), 0, uintptr(unsafe.Pointer(&fi)))
	if r == 0 {
		return 0, 0, false
	}
	w, h = int(fi.fontSize.x), int(fi.fontSize.y)
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
