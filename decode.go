// Image decoding.
// Native (pure Go): png/jpeg/gif/webp/tiff/bmp/ico/svg
// External fallback: avif/heic/heif/jxl (ffmpeg -> png pipe)
// Unsupported formats return an error; caller falls back to bat/cat.
package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

func decodeFile(path string) (image.Image, error) {
	ext := strings.ToLower(filepath.Ext(path))

	// ---------- pure Go path ----------
	switch ext {
	case ".webp":
		return openDecode(path, webp.Decode)
	case ".tiff", ".tif":
		return openDecode(path, tiff.Decode)
	case ".bmp":
		return openDecode(path, bmp.Decode)
	case ".svg", ".svgz":
		return svgFile(path)
	// these formats go straight to ffmpeg, skip stdlib sniff
	case ".avif", ".heic", ".heif", ".jxl":
		return ffmpegDecode(path)
	}

	// png/jpeg/gif/ico etc: stdlib sniff
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if img, _, err := image.Decode(f); err == nil {
		return img, nil
	}

	// ---------- external fallback: heic/heif/jxl/unknown containers ----------
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		if img, err := ffmpegDecode(path); err == nil {
			return img, nil
		}
	}
	return nil, fmt.Errorf("decode: unsupported format %q", ext)
}

func openDecode(path string, fn func(io.Reader) (image.Image, error)) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return fn(f)
}

// ffmpegDecode converts the first frame to a PNG pipe via ffmpeg
// (fallback for heic/heif/jxl), 5s timeout.
func ffmpegDecode(path string) (image.Image, error) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("no decoder and no ffmpeg: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-v", "error", "-i", path,
		"-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	img, _, err := image.Decode(bytes.NewReader(out.Bytes()))
	return img, err
}

// popplerPages reports the PDF page count via `pdfinfo` (poppler-utils).
func popplerPages(path string) (int, error) {
	pi, err := exec.LookPath("pdfinfo")
	if err != nil {
		return 0, fmt.Errorf("pdf: pdfinfo not found (install poppler-utils): %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, pi, path).Output()
	if err != nil {
		return 0, fmt.Errorf("pdfinfo: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Pages:") {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
			if err != nil {
				return 0, fmt.Errorf("pdfinfo: bad page count %q", line)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("pdfinfo: no Pages field (not a PDF?)")
}

// popplerRender rasterizes one page via `pdftoppm` to a temp PNG.
func popplerRender(path string, n, dpi int) (image.Image, error) {
	pp, err := exec.LookPath("pdftoppm")
	if err != nil {
		return nil, fmt.Errorf("pdf: pdftoppm not found (install poppler-utils): %w", err)
	}
	if dpi <= 0 {
		dpi = 150
	}
	tmp, err := os.CreateTemp("", "imgo-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("pdftoppm: temp: %w", err)
	}
	tmpName := tmp.Name()
	tmp.Close()
	os.Remove(tmpName) // pdftoppm writes <prefix>.png (-singlefile)
	pngPath := tmpName + ".png"
	defer os.Remove(pngPath)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pp, "-png", "-r", strconv.Itoa(dpi),
		"-f", strconv.Itoa(n), "-l", strconv.Itoa(n), "-singlefile", path, tmpName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftoppm: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	f, err := os.Open(pngPath)
	if err != nil {
		return nil, fmt.Errorf("pdftoppm: open png: %w", err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// PDF backends: mutool (MuPDF) is preferred when present — it renders more
// PDF features (transparency, JPEG2000/JBIG2, CMYK) that poppler sometimes
// chokes on — and is a single binary. poppler-utils (pdftoppm + pdfinfo) is
// the fallback. IMGO_PDF can force one: "mutool" | "poppler" | "auto".
type pdfBackend int

const (
	pdfBackendAuto pdfBackend = iota
	pdfBackendMutool
	pdfBackendPoppler
)

var (
	pdfBackendOnce   sync.Once
	pdfBackendMu     sync.Mutex
	pdfBackendCur    pdfBackend
	pdfBackendForced bool // IMGO_PDF pinned a specific backend: no auto-fallback
)

// pdfBackendNow resolves and caches the chosen backend (mutool > poppler, or
// whatever IMGO_PDF forces). Both are external CLI tools, never linked.
func pdfBackendNow() pdfBackend {
	pdfBackendOnce.Do(func() {
		pdfBackendMu.Lock()
		defer pdfBackendMu.Unlock()
		switch strings.ToLower(os.Getenv("IMGO_PDF")) {
		case "mutool":
			pdfBackendCur = pdfBackendMutool
			pdfBackendForced = true
		case "poppler":
			pdfBackendCur = pdfBackendPoppler
			pdfBackendForced = true
		default: // "" or "auto"
			if _, err := exec.LookPath("mutool"); err == nil {
				pdfBackendCur = pdfBackendMutool
			} else {
				pdfBackendCur = pdfBackendPoppler
			}
		}
	})
	pdfBackendMu.Lock()
	defer pdfBackendMu.Unlock()
	return pdfBackendCur
}

// otherBackend returns the alternate PDF backend (mutool <-> poppler).
func otherBackend(b pdfBackend) pdfBackend {
	switch b {
	case pdfBackendMutool:
		return pdfBackendPoppler
	case pdfBackendPoppler:
		return pdfBackendMutool
	default:
		return pdfBackendAuto
	}
}

// switchBackend records the backend that actually worked so the rest of the
// session skips a tool that is failing/hanging on this file.
func switchBackend(b pdfBackend) {
	pdfBackendMu.Lock()
	pdfBackendCur = b
	pdfBackendMu.Unlock()
}

// pdfCount returns the page count of a PDF via a specific backend.
func pdfCount(path string, b pdfBackend) (int, error) {
	switch b {
	case pdfBackendMutool:
		return mutoolPages(path)
	case pdfBackendPoppler:
		return popplerPages(path)
	default:
		return 0, fmt.Errorf("pdf: no backend (install mutool or poppler-utils)")
	}
}

// pdfDraw rasterizes page n (1-based) of a PDF via a specific backend.
func pdfDraw(path string, b pdfBackend, n, dpi int) (image.Image, error) {
	switch b {
	case pdfBackendMutool:
		return mutoolRender(path, n, dpi)
	case pdfBackendPoppler:
		return popplerRender(path, n, dpi)
	default:
		return nil, fmt.Errorf("pdf: no backend (install mutool or poppler-utils)")
	}
}

// pdfTotalPages returns the page count of a PDF. If the active backend fails it
// transparently tries the other one (unless IMGO_PDF pinned a backend) and
// sticks to whichever worked, so a broken/hanging tool degrades gracefully.
func pdfTotalPages(path string) (int, error) {
	b := pdfBackendNow()
	n, err := pdfCount(path, b)
	if err == nil {
		return n, nil
	}
	if !pdfBackendForced {
		if o := otherBackend(b); o != pdfBackendAuto {
			if n2, err2 := pdfCount(path, o); err2 == nil {
				switchBackend(o)
				return n2, nil
			}
		}
	}
	return 0, fmt.Errorf("pdf: page count: %w", err)
}

// pdfRenderPage rasterizes page n (1-based) of a PDF. If the active backend
// fails it transparently tries the other one (unless IMGO_PDF pinned a backend)
// and sticks to whichever worked, so a broken/hanging tool degrades gracefully.
// dpi controls raster resolution; 150 is a sensible default for on-screen.
func pdfRenderPage(path string, n, dpi int) (image.Image, error) {
	b := pdfBackendNow()
	img, err := pdfDraw(path, b, n, dpi)
	if err == nil {
		return img, nil
	}
	if !pdfBackendForced {
		if o := otherBackend(b); o != pdfBackendAuto {
			if img2, err2 := pdfDraw(path, o, n, dpi); err2 == nil {
				switchBackend(o)
				return img2, nil
			}
		}
	}
	return nil, fmt.Errorf("pdf: render page %d: %w", n, err)
}

// mutoolPages reports the page count via `mutool info` (parses the "Pages:"
// line). Shelling out to MuPDF's CLI avoids any AGPL/commercial licensing on
// imgo itself — the library is never linked.
func mutoolPages(path string) (int, error) {
	mu, err := exec.LookPath("mutool")
	if err != nil {
		return 0, fmt.Errorf("pdf: mutool not found (install mupdf-tools): %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, mu, "info", path).Output()
	if err != nil {
		return 0, fmt.Errorf("mutool info: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.ToLower(line), "pages:") {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "pages:")))
			if err == nil {
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("mutool: no Pages field (not a PDF?)")
}

// mutoolRender rasterizes one page via `mutool draw` to a temp PNG (read back
// and removed). mutool can write to stdout with -o - but a temp file keeps it
// uniform with the poppler path and avoids version-specific quirks.
func mutoolRender(path string, n, dpi int) (image.Image, error) {
	mu, err := exec.LookPath("mutool")
	if err != nil {
		return nil, fmt.Errorf("pdf: mutool not found (install mupdf-tools): %w", err)
	}
	if dpi <= 0 {
		dpi = 150
	}
	tmp, err := os.CreateTemp("", "imgo-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("mutool: temp: %w", err)
	}
	tmpName := tmp.Name()
	tmp.Close()
	os.Remove(tmpName)
	pngPath := tmpName + ".png"
	defer os.Remove(pngPath)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// mutool draw takes the page number as a trailing positional argument
	// (NOT -f/-l; those are "fit" and "line width"). A single page is written
	// straight to -o, so no %d is needed.
	cmd := exec.CommandContext(ctx, mu, "draw", "-r", strconv.Itoa(dpi),
		"-o", pngPath, path, strconv.Itoa(n))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("mutool draw: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	f, err := os.Open(pngPath)
	if err != nil {
		return nil, fmt.Errorf("mutool: open png: %w", err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
