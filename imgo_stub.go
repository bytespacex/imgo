//go:build !gui

// Stub for non-GUI builds: ebiten code is excluded, keeping pure Go zero deps.
package main

import (
	"fmt"
	"os"
)

// guiDefault is false: imgc stays a terminal tool
var guiDefault = false

func runGUI(string) {
	fmt.Fprintln(os.Stderr, "imgo: this binary was built without GUI support (need go build -tags gui)")
	os.Exit(1)
}
