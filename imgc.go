// imgc — pure Go terminal image previewer (Sixel first, half-block fallback)
//
// Usage:
//
//	imgc -W 106 -H 47 image.png
//	imgc -W $FZF_PREVIEW_COLUMNS -H $FZF_PREVIEW_LINES image.jpg
//	imgc -f symbols -W 80 -H 24 image.png   # force half-block mode
//
// Design:
//   - Zero C dependencies, single binary, cross-compilable (linux/macOS/Windows)
//   - Convert cells to pixels via cell size before Sixel output, keep aspect ratio
//   - Terminal probing: DA1 for Sixel capability, CSI 16t for cell pixel size
//   - Terminals without Sixel fall back to Unicode half-block chars (24bit color)
package main

import (
	"flag"
	"fmt"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
)

func main() {
	var (
		cols    = flag.Int("W", 0, "preview width in cells")
		rows    = flag.Int("H", 0, "preview height in cells")
		format  = flag.String("f", "auto", "output format: auto|sixel|symbols")
		cellW   = flag.Int("cw", 0, "cell width in pixels (0=auto detect)")
		cellH   = flag.Int("ch", 0, "cell height in pixels (0=auto detect)")
		dither  = flag.Bool("dither", true, "floyd-steinberg dithering (sixel)")
		colors  = flag.Int("colors", 256, "palette size (2-256, sixel)")
		probe   = flag.Bool("probe", true, "auto detect terminal capabilities")
		version = flag.Bool("version", false, "print version")
		warmup  = flag.Bool("warmup", false, "detect terminal caps in foreground, save to cache, exit")
	)
	flag.Parse()

	if *version {
		fmt.Println("imgc 0.1.0")
		return
	}
	if *warmup {
		ignoreJobControlSignals()
		probeCaps()
		return
	}
	// GUI build (-tags gui) opens a window by default: imgo <image>
	if guiDefault {
		runGUI(flag.Arg(0))
		return
	}
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: imgc [-W cols] [-H rows] [-f auto|sixel|symbols] <image>")
		os.Exit(2)
	}
	// Important: fzf preview etc. run in a background process group; tcsetattr
	// on /dev/tty there raises SIGTTOU whose default action stops the process
	// (fzf spins forever). Must ignore it first.
	ignoreJobControlSignals()
	path := flag.Arg(0)

	// ---------- 1. decode ----------
	img, err := decodeFile(path)
	if err != nil {
		// not an image: draw nothing, exit 1 so caller can fall back to bat/cat
		fmt.Fprintf(os.Stderr, "imgc: %s: %v\n", filepath.Base(path), err)
		os.Exit(1)
	}

	// ---------- 2. terminal capabilities ----------
	capSixel := false
	cw, ch := 8, 16
	// Note: inside lf/fzf the previewer's stdout is a pipe, so isTTY(stdout)
	// always fails. Both capabilities come from /dev/tty, so we probe only when
	// /dev/tty is openable. Cell size uses TIOCGWINSZ (an ioctl: writes nothing,
	// reads nothing), so it is safe even while fzf holds /dev/tty. The DA1 query
	// is different: it writes an escape sequence whose reply fzf would eat as
	// keystrokes (garbage ?62;4;...c in its query box), so with an explicit
	// -f sixel/symbols we skip it entirely.
	if *probe && ttyAvailable() {
		if *cellW == 0 || *cellH == 0 {
			w, h := cellSize()
			if *cellW == 0 {
				cw = w
			}
			if *cellH == 0 {
				ch = h
			}
		}
		if *format == "auto" {
			capSixel = hasSixel()
		}
	}
	if *cellW > 0 {
		cw = *cellW
	}
	if *cellH > 0 {
		ch = *cellH
	}

	useSixel := false
	switch *format {
	case "sixel":
		useSixel = true
	case "symbols":
		useSixel = false
	default: // auto
		useSixel = capSixel
	}

	// ---------- 3. size conversion ----------
	// With W/H given: convert cells to pixels keeping aspect ratio.
	// Without: use original size (capped at 1024).
	var targetW, targetH int
	if *cols > 0 && *rows > 0 {
		targetW, targetH = fit(img.Bounds().Dx(), img.Bounds().Dy(),
			*cols*cw, (*rows-1)*ch) // leave one row for status bar / avoid scrolling
	} else {
		targetW, targetH = fit(img.Bounds().Dx(), img.Bounds().Dy(), 1024, 1024)
	}

	scaled := scaleImg(img, targetW, targetH)

	// ---------- 4. output ----------
	if useSixel {
		if err := outputSixel(os.Stdout, scaled, *dither, *colors); err != nil {
			fmt.Fprintf(os.Stderr, "imgc: sixel: %v\n", err)
			os.Exit(1)
		}
		return
	}
	// symbol mode: each cell uses ▀ with two stacked pixels, half cell height
	symH := targetH / 2
	if symH < 1 {
		symH = 1
	}
	sym := scaleImg(img, targetW, symH)
	outputSymbols(os.Stdout, sym)
}

// fit computes the largest size that fits in (maxW,maxH) keeping aspect ratio
func fit(iw, ih, maxW, maxH int) (int, int) {
	if iw <= 0 || ih <= 0 {
		return 1, 1
	}
	if maxW <= 0 {
		maxW = iw
	}
	if maxH <= 0 {
		maxH = ih
	}
	// sixel pixels are square, scale proportionally
	if iw*maxH > ih*maxW {
		w := maxW
		h := ih * maxW / iw
		if h < 1 {
			h = 1
		}
		return w, h
	}
	h := maxH
	w := iw * maxH / ih
	if w < 1 {
		w = 1
	}
	return w, h
}
