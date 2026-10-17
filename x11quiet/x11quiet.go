//go:build gui

// Package x11quiet silences xgb's harmless "no ~/.Xauthority" notices on Linux.
//
// Under a Wayland compositor with XWayland (Sway here) there is usually no
// ~/.Xauthority, so xgb — the X binding ebiten uses — logs
//
//	XGB: conn.go:47: Could not get authority info: open /home/.../.Xauthority: no such file or directory
//	XGB: conn.go:48: Trying connection without authority info...
//
// on start, even though the connection then succeeds without auth. Those two
// lines are noise; every other xgb message (real read/write errors, protocol
// bugs) must still reach stderr.
//
// ebiten's internal/ui opens the X connection during *its own* package init
// (internal/ui's init -> newUserInterface -> the monitor query -> xgb.NewConn),
// which runs before package main's init. So the filter cannot live in main: it
// must be installed by a package initialized ahead of internal/ui. Package main
// blank-imports this one (see imgo.go); it depends only on xgb, so it is
// initialized before ebiten's internal/ui, and the filter is installed first.
package x11quiet

import (
	"bytes"
	"log"
	"os"

	"github.com/jezek/xgb"
)

// noiseWriter forwards xgb's log output to stderr, swallowing the two
// missing-Xauthority notices.
type noiseWriter struct{}

func (noiseWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("Could not get authority info")) ||
		bytes.Contains(p, []byte("Trying connection without authority info")) {
		return len(p), nil // harmless: the connection works fine without auth
	}
	return os.Stderr.Write(p)
}

func init() {
	xgb.Logger = log.New(noiseWriter{}, "XGB: ", log.Lshortfile)
}
