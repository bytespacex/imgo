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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
