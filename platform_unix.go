//go:build !windows

// Unix-specific terminal glue: job-control signals and cell-size probing.
// Everything here has a Windows counterpart in platform_windows.go.
package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// ignoreJobControlSignals stops the terminal from suspending the process when
// /dev/tty is touched from a background process group (fzf/lf preview):
// tcsetattr there raises SIGTTOU and reading raises SIGTTIN, both of whose
// default action stops the process. Windows has no such job-control signals.
func ignoreJobControlSignals() {
	signal.Ignore(syscall.SIGTTOU, syscall.SIGTTIN)
}

// winsizeCell derives the cell size from the tty's own pixel dimensions:
//
//	cell_w = ws_xpixel / ws_col,   cell_h = ws_ypixel / ws_row
//
// An ioctl writes no bytes and reads no input, so unlike a CSI 16t query it
// cannot be intercepted by another process holding /dev/tty for the keyboard
// (fzf), and it is legal in a background process group. Terminals that leave
// ws_xpixel at 0 (alacritty, xterm, ...) report ok=false.
func winsizeCell() (w, h int, ok bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, 0, false
	}
	defer tty.Close()
	sz, err := unix.IoctlGetWinsize(int(tty.Fd()), unix.TIOCGWINSZ)
	if err != nil || sz == nil || sz.Col == 0 || sz.Row == 0 {
		return 0, 0, false
	}
	w = int(sz.Xpixel) / int(sz.Col)
	h = int(sz.Ypixel) / int(sz.Row)
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
