// System font discovery shared by the help overlay and SVG text.
//
// Text is drawn with the machine's own fonts: fontconfig on Linux, fixed
// locations on macOS and Windows. Collections (.ttc) are unwrapped because
// x/image only parses standalone sfnt files. Nothing here touches ebiten, so
// the terminal viewer can render SVG text too.
package main

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

// font loading

var (
	fontsOnce    sync.Once
	regularFonts []*opentype.Font // parsed default chain, primary first
	boldFonts    []*opentype.Font // parsed bold chain, may be empty
)

// systemFonts returns the parsed font chain to try per glyph. When bold is
// requested the bold variants go first and the regular chain follows, so a
// font without a bold cut still renders instead of falling back to a box.
func systemFonts(bold bool) []*opentype.Font {
	fontsOnce.Do(parseSystemFonts)
	if bold && len(boldFonts) > 0 {
		return append(append([]*opentype.Font{}, boldFonts...), regularFonts...)
	}
	return regularFonts
}

func parseSystemFonts() {
	load := func(p string) *opentype.Font {
		if p == "" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		f, err := parseSfnt(b)
		if err != nil {
			return nil
		}
		return f
	}
	// Primary first (the user's configured font), then a font fontconfig
	// proves to cover CJK, then the usual system locations.
	seen := map[string]bool{}
	cands := append([]string{fcMatch("sans"), fcMatch(":charset=4e00")}, platformFontPaths()...)
	for _, p := range cands {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if f := load(p); f != nil {
			regularFonts = append(regularFonts, f)
		}
	}
	// Go's bundled font: Latin-only, but guaranteed to exist.
	if f, err := opentype.Parse(goregular.TTF); err == nil {
		regularFonts = append(regularFonts, f)
	}
	if f := load(fcMatch("sans:style=Bold")); f != nil {
		boldFonts = append(boldFonts, f)
	}
	// A Latin bold face usually has no CJK glyphs; give Chinese bold text a
	// real bold cut instead of falling through to the regular CJK face.
	if f := load(fcMatch(":charset=4e00:style=Bold")); f != nil {
		boldFonts = append(boldFonts, f)
	}
}

func fcMatch(pattern string) string {
	out, err := exec.Command("fc-match", "-f", "%{file}", pattern).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// platformFontPaths returns the usual system font locations for the running
// OS, used when fontconfig (fc-match) is unavailable. Linux leans on
// fontconfig; macOS and Windows do not ship it, so their fixed locations are
// listed here — each with a CJK-capable face, since the default sans is
// Latin-only and Chinese file names would otherwise render as tofu.
func platformFontPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/System/Library/Fonts/Helvetica.ttc",
			"/System/Library/Fonts/Geneva.ttf",
			"/System/Library/Fonts/Supplemental/Arial.ttf",
			"/Library/Fonts/Arial.ttf",
			// CJK-capable
			"/System/Library/Fonts/PingFang.ttc",
			"/System/Library/Fonts/Hiragino Sans GB.ttc",
			"/System/Library/Fonts/STHeiti Light.ttc",
			"/System/Library/Fonts/AppleSDGothicNeo.ttc",
		}
	case "windows":
		return []string{
			`C:\Windows\Fonts\segoeui.ttf`,
			`C:\Windows\Fonts\tahoma.ttf`,
			`C:\Windows\Fonts\arial.ttf`,
			// CJK-capable
			`C:\Windows\Fonts\msyh.ttc`,   // Microsoft YaHei
			`C:\Windows\Fonts\simhei.ttf`, // SimHei
			`C:\Windows\Fonts\simsun.ttc`, // SimSun
			`C:\Windows\Fonts\msgothic.ttc`,
		}
	default:
		return []string{
			"/usr/share/fonts/TTF/DejaVuSans.ttf",
			"/usr/share/fonts/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
			"/usr/share/fonts/liberation/LiberationSans-Regular.ttf",
			"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
			"/usr/share/fonts/noto/NotoSans-Regular.ttf",
			"/usr/share/fonts/truetype/freefont/FreeSans.ttf",
			"/usr/share/fonts/google-noto/NotoSans-Regular.ttf",
			// CJK fallbacks (most ship as .ttc collections)
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/google-noto-cjk/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
			"/usr/share/fonts/wqy-zenhei/wqy-zenhei.ttc",
			"/usr/share/fonts/truetype/arphic/uming.ttc",
		}
	}
}

// parseSfnt parses TrueType/OpenType data, including font collections: the
// wanted entry of a .ttc is rebuilt as a standalone sfnt first.
func parseSfnt(data []byte) (*opentype.Font, error) {
	if f, err := opentype.Parse(data); err == nil {
		return f, nil
	}
	if len(data) < 16 || string(data[:4]) != "ttcf" {
		return nil, errors.New("not an sfnt font")
	}
	solo, err := extractTTC(data, 0)
	if err != nil {
		return nil, err
	}
	return opentype.Parse(solo)
}

// extractTTC rebuilds entry index of a TrueType collection as a standalone
// sfnt. A collection's table directory stores offsets relative to the start of
// the whole file, so the table data is copied unchanged and only the offsets
// inside the copied header are rewritten to point at the appended data.
func extractTTC(data []byte, index int) ([]byte, error) {
	u32 := func(b []byte) int { return int(binary.BigEndian.Uint32(b)) }
	if string(data[:4]) != "ttcf" {
		return nil, errors.New("not a ttc collection")
	}
	n := u32(data[8:12])
	if index < 0 || index >= n {
		return nil, errors.New("ttc index out of range")
	}
	off := u32(data[12+4*index:])
	if off < 0 || off+12 > len(data) {
		return nil, errors.New("bad ttc entry offset")
	}
	numTables := int(binary.BigEndian.Uint16(data[off+4 : off+6]))
	if numTables <= 0 || numTables > 512 || off+12+16*numTables > len(data) {
		return nil, errors.New("bad table directory")
	}
	type tbl struct{ off, ln int }
	tables := make([]tbl, numTables)
	for i := range tables {
		rec := data[off+12+16*i:]
		t, l := u32(rec[8:]), u32(rec[12:])
		if t < 0 || l < 0 || t+l > len(data) {
			return nil, errors.New("bad table record")
		}
		tables[i] = tbl{t, l}
	}
	align := func(v int) int { return (v + 3) &^ 3 }
	pos := 12 + 16*numTables
	for _, t := range tables {
		pos = align(pos)
		pos += t.ln
	}
	out := make([]byte, pos)
	copy(out, data[off:off+12+16*numTables])
	p := 12 + 16*numTables // restart at the end of the copied directory
	for i, t := range tables {
		p = align(p)
		copy(out[p:], data[t.off:t.off+t.ln])
		binary.BigEndian.PutUint32(out[12+16*i+8:], uint32(p))
		p += t.ln
	}
	return out, nil
}
