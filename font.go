//go:build gui

package main

// Help overlay and status line typography. ebitenutil's debug font is a 5x9
// bitmap meant for log output, so both panels are rendered into RGBA images
// with the system's fonts and reused on every frame.
//
// Three problems are solved here:
//
//   - Glyph coverage: the default sans font is usually Latin-only, so a
//     Chinese file name would render as tofu boxes. Every rune is drawn with
//     the first face in the chain that actually has the glyph (a
//     CJK-capable font is queried from fontconfig and appended to the chain).
//   - Font collections: x/image only parses standalone sfnt files, but
//     fontconfig happily returns .ttc files (Noto Sans CJK ships as one), so
//     the wanted entry is extracted from the collection first.
//   - Quality: everything is drawn at panelScale the size and box-filtered
//     down, which antialiases both glyphs and the rounded panel corners.

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// panelScale is the supersampling factor used for both panels.
const panelScale = 4

// helpRows is the key/description table of the overlay. Keep the text ASCII
// so any font renders it; the file name in the status line may be anything.
var helpRows = [][2]string{
	{"?", "help"},
	{"esc / q", "close this help"},
	{"f", "toggle fullscreen"},
	{"space", "original size <-> fit to window"},
	{"0", "original size (1:1)"},
	{"+ / -", "zoom about image centre"},
	{"shift+- / shift+=", "zoom about cursor"},
	{"wheel", "zoom about cursor"},
	{"drag", "pan"},
	{"click", "1:1 + hands-free pan"},
	{"r / t", "rotate 90 CW / CCW"},
	{"w / e", "mirror left-right / up-down"},
	{"i", "negative (invert)"},
	{"< / >", "brightness down / up"},
	{"left / right", "prev / next image"},
	{"k / j", "prev / next image (vim)"},
	{"pgup / pgdn", "prev / next directory"},
	{`\`, "slideshow (2s)"},
	{"[ / ]", "slideshow interval"},
	{"p", "pause animation"},
	{", / .", "step animation back / on"},
	{"d", "delete (to trash)"},
	{"m / M", "copy / move to imgo/"},
	{"o", "external program (env IMGO_OPEN)"},
	{"/", "directory file list (/ again to filter)"},
	{"c", "copy colour under cursor (hex)"},
	{"v", "measure a rectangle (image / screen px)"},
	{"ctrl+p / ctrl+u", "copy path / file URI"},
}

var (
	helpBG     = color.RGBA{14, 15, 20, 204} // 80% opaque
	helpBorder = color.RGBA{80, 85, 100, 255}
	helpTitle  = color.RGBA{246, 246, 249, 255}
	helpKey    = color.RGBA{125, 190, 255, 255}
	helpDesc   = color.RGBA{208, 210, 219, 255}

	// scroll bar down the panel's right edge while the list is taller than
	// the window: a faint track and a thumb sized like the visible window.
	helpScrollTrack = color.RGBA{48, 52, 64, 255}
	helpScrollThumb = color.RGBA{150, 160, 185, 255}

	// openSelBG highlights the selected row of the "o" external-program menu.
	openSelBG = color.RGBA{40, 46, 62, 255}

	// statusBG/Border style the top status pill: more see-through than the
	// help panel (the image behind it should stay readable) and with only a
	// slight corner rounding instead of the pill shape. While the cursor is
	// captured the pill deepens, so hands-free panning is recognisable from
	// the status line alone.
	statusBG     = color.RGBA{14, 15, 20, 170}
	statusBGLock = color.RGBA{8, 9, 13, 242}
	statusBorder = color.RGBA{70, 75, 90, 200}
	statusRad    = 6 * panelScale                 // corner radius in logical pixels
	statusTag    = color.RGBA{255, 190, 110, 255} // "[cursor mode]" accent
)

// helpBodySize picks a readable body size for the current window height.
func helpBodySize(h int) int {
	s := h / 40
	if s < 13 {
		s = 13
	}
	if s > 26 {
		s = 26
	}
	return s
}

// ---------------------------------------------------------------------------
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

// ---------------------------------------------------------------------------
// face chain with per-rune glyph fallback

// textFace measures and draws strings using a chain of faces: each rune goes
// to the first face whose cmap covers it, and consecutive runes sharing a face
// are drawn as one run so kerning inside a script still applies.
type textFace struct {
	faces []font.Face
}

// newFaces builds the chain at the given size (already multiplied by
// panelScale by the caller). Hinting is off: the supersampled result is
// box-filtered down afterwards, which is its own hinting.
func newFaces(size float64, bold bool) *textFace {
	tf := &textFace{}
	for _, f := range systemFonts(bold) {
		face, err := opentype.NewFace(f, &opentype.FaceOptions{
			Size:    size,
			DPI:     72,
			Hinting: font.HintingNone,
		})
		if err != nil {
			continue
		}
		tf.faces = append(tf.faces, face)
	}
	return tf
}

func (t *textFace) empty() bool { return len(t.faces) == 0 }

func (t *textFace) metrics() font.Metrics { return t.faces[0].Metrics() }

// faceFor returns the first face covering r, or the primary face (whose
// .notdef box the caller renders when nothing in the chain covers the rune).
func (t *textFace) faceFor(r rune) font.Face {
	for _, f := range t.faces {
		if _, ok := f.GlyphAdvance(r); ok {
			return f
		}
	}
	return t.faces[0]
}

// has reports whether any face in the chain covers r.
func (t *textFace) has(r rune) bool {
	for _, f := range t.faces {
		if _, ok := f.GlyphAdvance(r); ok {
			return true
		}
	}
	return false
}

// measure reports the pen advance for s with the same per-rune face choice
// and kerning rules as draw, so layout always matches rendering.
func (t *textFace) measure(s string) fixed.Int26_6 {
	var x fixed.Int26_6
	var prev rune = -1
	var prevF font.Face
	for _, r := range s {
		f := t.faceFor(r)
		if prev >= 0 && f == prevF {
			x += f.Kern(prev, r)
		}
		a, _ := f.GlyphAdvance(r)
		x += a
		prev, prevF = r, f
	}
	return x
}

// draw renders s with its baseline at (x, y), one DrawString call per run of
// runes that share a face.
func (t *textFace) draw(dst draw.Image, s string, x, y int, c color.Color) {
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Dot: fixed.P(x, y)}
	var run []rune
	var runF font.Face
	flush := func() {
		if len(run) == 0 {
			return
		}
		d.Face = runF
		d.DrawString(string(run))
		run = run[:0]
	}
	for _, r := range s {
		f := t.faceFor(r)
		if runF != nil && f != runF {
			flush()
		}
		runF = f
		run = append(run, r)
	}
	flush()
}

// ---------------------------------------------------------------------------
// rounded panels and downsampling

func sq(v float64) float64 { return v * v }

// inRound reports whether the pixel whose centre is (x+0.5, y+0.5) lies in the
// rectangle (x0..x1, y0..y1 boundaries) with corner radius r.
func inRound(x, y, x0, y0, x1, y1, r int) bool {
	if x < x0 || x >= x1 || y < y0 || y >= y1 {
		return false
	}
	if r <= 0 {
		return true
	}
	px, py, rr := float64(x)+0.5, float64(y)+0.5, float64(r)
	x0f, y0f := float64(x0), float64(y0)
	x1f, y1f := float64(x1), float64(y1)
	switch {
	case px < x0f+rr && py < y0f+rr:
		return sq(px-(x0f+rr))+sq(py-(y0f+rr)) <= rr*rr
	case px >= x1f-rr && py < y0f+rr:
		return sq(px-(x1f-rr))+sq(py-(y0f+rr)) <= rr*rr
	case px < x0f+rr && py >= y1f-rr:
		return sq(px-(x0f+rr))+sq(py-(y1f-rr)) <= rr*rr
	case px >= x1f-rr && py >= y1f-rr:
		return sq(px-(x1f-rr))+sq(py-(y1f-rr)) <= rr*rr
	}
	return true
}

// roundRect fills a rounded rectangle and strokes a one-logical-pixel border
// into a panelScale-times oversized image. All coordinates are in the
// destination image's own (supersampled) pixels.
func roundRect(dst *image.RGBA, x0, y0, x1, y1, rad int, fill, stroke color.RGBA) {
	sw := panelScale // border thickness: one logical pixel
	if rad < 0 {
		rad = 0
	}
	if m := (x1 - x0) / 2; rad > m {
		rad = m
	}
	if m := (y1 - y0) / 2; rad > m {
		rad = m
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if !inRound(x, y, x0, y0, x1, y1, rad) {
				continue
			}
			c := stroke
			if x1-2*sw > x0 && y1-2*sw > y0 &&
				inRound(x, y, x0+sw, y0+sw, x1-sw, y1-sw, rad-sw) {
				c = fill
			}
			dst.SetRGBA(x, y, c)
		}
	}
}

// downsample box-filters a panelScale-times oversized image by averaging raw
// pixels: that antialiases glyphs and rounded corners alike without any
// font-hinting tricks.
func downsample(src *image.RGBA, s int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx()/s, b.Dy()/s
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sp, dp := src.Pix, dst.Pix
	n := uint32(s * s)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc [4]uint32
			for j := 0; j < s; j++ {
				row := (b.Min.Y+y*s+j)*src.Stride + (b.Min.X+x*s)*4
				for i := 0; i < s; i++ {
					p := row + i*4
					acc[0] += uint32(sp[p+0])
					acc[1] += uint32(sp[p+1])
					acc[2] += uint32(sp[p+2])
					acc[3] += uint32(sp[p+3])
				}
			}
			o := y*dst.Stride + x*4
			dp[o+0] = uint8(acc[0] / n)
			dp[o+1] = uint8(acc[1] / n)
			dp[o+2] = uint8(acc[2] / n)
			dp[o+3] = uint8(acc[3] / n)
		}
	}
	return dst
}

// ---------------------------------------------------------------------------
// panels

// buildHelp renders the overlay panel at the given body size and scroll
// offset, sized to fit availH window pixels; it is rebuilt whenever any of
// the three changes.
func buildHelp(bodySize, scroll, availH int) *ebiten.Image {
	img := renderHelp(bodySize, scroll, availH)
	if img == nil {
		return nil
	}
	return ebiten.NewImageFromImage(img)
}

// helpMargin is the clearance the help panel keeps from the window's top and
// bottom edge, in logical pixels, so it never looks glued to the frame.
const helpMargin = 20

// helpGeom measures the panel's vertical geometry at panelScale for a body
// size: the faces to draw with, the fixed header height (top padding, title,
// gap before the first row) and one row's height. renderHelp lays the panel
// out from it and helpVisibleRows counts with it, so the scroll range the
// viewer allows and the list it draws can never disagree.
func helpGeom(bodySize int) (bodyFace, titleFace *textFace, header, rowH int) {
	const s = panelScale
	bodyFace = newFaces(float64(bodySize*s), false)
	titleFace = newFaces(float64(bodySize*s+5*s), true)
	if bodyFace.empty() || titleFace.empty() {
		return bodyFace, titleFace, 0, 0
	}
	afterTitle := bodySize * s
	tm, bm := titleFace.metrics(), bodyFace.metrics()
	ascent, descent := bm.Ascent.Ceil(), bm.Descent.Ceil()
	rowH = ascent + descent + bodySize*s*6/10
	header = 20*s + tm.Ascent.Ceil() + tm.Descent.Ceil() + afterTitle
	return
}

// helpVisibleRows is how many of helpRows fit in an availH-pixel-tall window
// at this body size — at least one row, at most the whole list. It mirrors
// renderHelp's budget exactly (header + rows + bottom padding + margin), so
// a scroll offset the viewer considers legal always draws full rows.
// availH <= 0 means "no limit": everything is visible.
func helpVisibleRows(bodySize, availH int) int {
	_, _, header, rowH := helpGeom(bodySize)
	if rowH <= 0 || availH <= 0 {
		return len(helpRows)
	}
	n := ((availH-helpMargin)*panelScale - header - 20*panelScale) / rowH
	if n < 1 {
		n = 1
	}
	if n > len(helpRows) {
		n = len(helpRows)
	}
	return n
}

// listVisibleRows is how many file-list rows fit in an availH-pixel window at
// this body size — the same budget renderListMenu lays out with, so the scroll
// range the viewer allows and the rows it draws can never disagree. At least
// one row while there is content, never more than the total.
func listVisibleRows(bodySize, availH, total int) int {
	_, _, header, rowH := helpGeom(bodySize)
	if rowH <= 0 || availH <= 0 {
		return total
	}
	n := ((availH-helpMargin)*panelScale - header - 20*panelScale) / rowH
	if n < 1 {
		n = 1
	}
	if n > total {
		n = total
	}
	return n
}

// renderHelp draws the panel into a plain RGBA image, independent of ebiten,
// so it can be verified headlessly. All layout happens at panelScale the size.
// Only the rows that fit in availH window pixels are drawn — scroll selects
// the first one — and a scroll bar down the right edge reports the position
// whenever rows are hidden. availH <= 0 draws the whole list.
func renderHelp(bodySize, scroll, availH int) *image.RGBA {
	const (
		s     = panelScale
		title = "imgo - keys"
	)
	padX, padY, rad := 26*s, 20*s, 12*s
	bodyFace, titleFace, header, rowH := helpGeom(bodySize)
	if bodyFace.empty() || titleFace.empty() {
		return nil
	}
	ascent := bodyFace.metrics().Ascent.Ceil()
	keyGap := bodySize * 2 * s

	// the visible slice: whatever fits, with the offset clamped so it always
	// starts on a real row and never leaves blanks at the bottom
	rows := helpVisibleRows(bodySize, availH)
	if scroll > len(helpRows)-rows {
		scroll = len(helpRows) - rows
	}
	if scroll < 0 {
		scroll = 0
	}
	shown := helpRows[scroll : scroll+rows]

	keyW, descW := 0, 0
	for _, r := range helpRows {
		if w := bodyFace.measure(r[0]).Ceil(); w > keyW {
			keyW = w
		}
		if w := bodyFace.measure(r[1]).Ceil(); w > descW {
			descW = w
		}
	}
	contentW := keyW + keyGap + descW
	titleW := titleFace.measure(title).Ceil()
	if titleW > contentW {
		contentW = titleW
	}

	pw := contentW + 2*padX
	ph := header + rows*rowH + padY

	img := image.NewRGBA(image.Rect(0, 0, pw, ph))
	roundRect(img, 0, 0, pw, ph, rad, helpBG, helpBorder)

	tm := titleFace.metrics()
	y := padY + tm.Ascent.Ceil()
	titleFace.draw(img, title, (pw-titleW)/2, y, helpTitle)
	y = header + ascent // first row baseline
	for _, r := range shown {
		kx := padX + keyW - bodyFace.measure(r[0]).Ceil()
		bodyFace.draw(img, r[0], kx, y, helpKey)
		bodyFace.draw(img, r[1], padX+keyW+keyGap, y, helpDesc)
		y += rowH
	}
	// scroll bar over the row area only, and only while rows are hidden
	if rows < len(helpRows) {
		x0, x1 := pw-16*s, pw-12*s
		y0, y1 := header, header+rows*rowH
		roundRect(img, x0, y0, x1, y1, 2*s, helpScrollTrack, helpScrollTrack)
		th := (y1 - y0) * rows / len(helpRows)
		if th < 8*s {
			th = 8 * s
		}
		if th > y1-y0 {
			th = y1 - y0
		}
		ty := y0 + (y1-y0-th)*scroll/(len(helpRows)-rows)
		roundRect(img, x0, ty, x1, ty+th, 2*s, helpScrollThumb, helpScrollThumb)
	}
	return downsample(img, s)
}

// renderOpenMenu draws the "o" external-program launcher: the configured
// programs, one per row, the selected row highlighted, and the typed filter
// shown after the title. Names are cut with ".." so the panel never runs past
// maxW logical pixels. Independent of ebiten, so it can be checked headlessly.
func renderOpenMenu(bodySize int, names []string, sel int, filter string, maxW int) *image.RGBA {
	const s = panelScale
	padX, padY, rad := 26*s, 20*s, 12*s
	title := "imgo - open"
	if filter != "" {
		title += "  > " + filter
	}
	bodyFace := newFaces(float64(bodySize*s), false)
	titleFace := newFaces(float64(bodySize*s+5*s), true)
	if bodyFace.empty() || titleFace.empty() {
		return nil
	}
	ascent, descent := bodyFace.metrics().Ascent.Ceil(), bodyFace.metrics().Descent.Ceil()
	rowH := ascent + descent + bodySize*s*6/10
	tm := titleFace.metrics()
	header := 20*s + tm.Ascent.Ceil() + tm.Descent.Ceil() + bodySize*s
	maxW *= s

	cut := func(nm string) string {
		nm = printable(bodyFace, nm)
		rs := []rune(nm)
		for len(rs) > 5 && padX+bodyFace.measure(string(rs)).Ceil()+padX > maxW {
			rs = rs[:len(rs)-1]
		}
		if bodyFace.measure(string(rs)) < bodyFace.measure(nm) {
			rs = append(rs, '.', '.')
		}
		return string(rs)
	}
	shown := make([]string, len(names))
	w := 0
	for i, n := range names {
		shown[i] = cut(n)
		if ww := bodyFace.measure(shown[i]).Ceil(); ww > w {
			w = ww
		}
	}
	titleW := titleFace.measure(title).Ceil()
	if titleW > w {
		w = titleW
	}
	rows := len(names)
	if rows == 0 {
		rows = 1 // a single "(no match)" row
	}
	pw := w + 2*padX
	ph := header + rows*rowH + padY
	img := image.NewRGBA(image.Rect(0, 0, pw, ph))
	roundRect(img, 0, 0, pw, ph, rad, helpBG, helpBorder)

	y := padY + tm.Ascent.Ceil()
	titleFace.draw(img, title, (pw-titleW)/2, y, helpTitle)
	if len(names) == 0 {
		bodyFace.draw(img, "(no match)", padX, header+ascent, helpDesc)
		return downsample(img, s)
	}
	for i, n := range shown {
		rowTop := header + i*rowH
		c := helpDesc
		if i == sel {
			roundRect(img, padX/2, rowTop, pw-padX/2, rowTop+rowH, 6*s, openSelBG, openSelBG)
			c = helpTitle
		}
		bodyFace.draw(img, n, padX, rowTop+ascent, c)
	}
	return downsample(img, s)
}

func buildOpenMenu(bodySize int, names []string, sel int, filter string, maxW int) *ebiten.Image {
	img := renderOpenMenu(bodySize, names, sel, filter, maxW)
	if img == nil {
		return nil
	}
	return ebiten.NewImageFromImage(img)
}

// renderListMenu draws the "l" directory list: the image files of the current
// directory, one per row, the selected row highlighted, the image now on screen
// marked with a dot in the left gutter, and a scroll bar while rows are hidden.
// The filter shows in the title with a caret while its box has focus. Names are
// cut with ".." so the panel stays within maxW logical pixels. Independent of
// ebiten, so it can be checked headlessly.
func renderListMenu(bodySize int, names []string, sel, top, cur int, focused bool, dir, filter string, maxW, availH int) *image.RGBA {
	const s = panelScale
	padX, padY, rad := 26*s, 20*s, 12*s
	title := "imgo - " + dir
	if filter != "" || focused {
		title += "  > " + filter
		if focused {
			title += "_" // the filter box has the keyboard
		}
	}
	bodyFace := newFaces(float64(bodySize*s), false)
	titleFace := newFaces(float64(bodySize*s+5*s), true)
	if bodyFace.empty() || titleFace.empty() {
		return nil
	}
	ascent, descent := bodyFace.metrics().Ascent.Ceil(), bodyFace.metrics().Descent.Ceil()
	rowH := ascent + descent + bodySize*s*6/10
	tm := titleFace.metrics()
	header := 20*s + tm.Ascent.Ceil() + tm.Descent.Ceil() + bodySize*s
	maxW *= s

	// the gutter holds the "on screen" dot; the text column starts after it
	gutter := ascent + bodySize*s/2
	cut := func(nm string) string {
		nm = printable(bodyFace, nm)
		rs := []rune(nm)
		for len(rs) > 5 && padX+gutter+bodyFace.measure(string(rs)).Ceil()+padX > maxW {
			rs = rs[:len(rs)-1]
		}
		if bodyFace.measure(string(rs)) < bodyFace.measure(nm) {
			rs = append(rs, '.', '.')
		}
		return string(rs)
	}
	shown := make([]string, len(names))
	w := 0
	for i, n := range names {
		shown[i] = cut(n)
		if ww := bodyFace.measure(shown[i]).Ceil(); ww > w {
			w = ww
		}
	}
	w += gutter
	if titleW := titleFace.measure(title).Ceil(); titleW > w {
		w = titleW
	}

	rows := listVisibleRows(bodySize, availH, len(names))
	if rows < 1 {
		rows = 1 // the single "(no match)" row
	}
	if top > len(names)-rows {
		top = len(names) - rows
	}
	if top < 0 {
		top = 0
	}
	pw := w + 2*padX
	ph := header + rows*rowH + padY
	img := image.NewRGBA(image.Rect(0, 0, pw, ph))
	roundRect(img, 0, 0, pw, ph, rad, helpBG, helpBorder)

	titleFace.draw(img, title, (pw-titleFace.measure(title).Ceil())/2, padY+tm.Ascent.Ceil(), helpTitle)
	if len(names) == 0 {
		bodyFace.draw(img, "(no match)", padX+gutter, header+ascent, helpDesc)
		return downsample(img, s)
	}
	for i := top; i < top+rows && i < len(names); i++ {
		rowTop := header + (i-top)*rowH
		c := helpDesc
		if i == sel {
			roundRect(img, padX/2, rowTop, pw-padX/2, rowTop+rowH, 6*s, openSelBG, openSelBG)
			c = helpTitle
		}
		if i == cur {
			m := ascent / 2
			cx := padX + (ascent-m)/2
			cy := rowTop + (rowH-m)/2
			roundRect(img, cx, cy, cx+m, cy+m, m/2, helpKey, helpKey)
		}
		bodyFace.draw(img, shown[i], padX+gutter, rowTop+ascent, c)
	}
	if rows < len(names) {
		x0, x1 := pw-16*s, pw-12*s
		y0, y1 := header, header+rows*rowH
		roundRect(img, x0, y0, x1, y1, 2*s, helpScrollTrack, helpScrollTrack)
		th := (y1 - y0) * rows / len(names)
		if th < 8*s {
			th = 8 * s
		}
		if th > y1-y0 {
			th = y1 - y0
		}
		ty := y0 + (y1-y0-th)*top/(len(names)-rows)
		roundRect(img, x0, ty, x1, ty+th, 2*s, helpScrollThumb, helpScrollThumb)
	}
	return downsample(img, s)
}

func buildListMenu(bodySize int, names []string, sel, top, cur int, focused bool, dir, filter string, maxW, availH int) *ebiten.Image {
	img := renderListMenu(bodySize, names, sel, top, cur, focused, dir, filter, maxW, availH)
	if img == nil {
		return nil
	}
	return ebiten.NewImageFromImage(img)
}

// renderStatus draws the always-visible status line: mode tags on the left
// ("[cursor mode]" for hands-free panning, "[slide mode-2s]" for the
// slideshow), then the "[1/822][2.3M]" counter in the accent colour and the
// file name, on a rounded panel. Any tag present — i.e. any mode running —
// deepens the panel, so the state is recognisable from the status line alone.
// The name is cut with ".." rather than allowed to run past maxW; runes no
// face covers are skipped entirely instead of drawn as tofu boxes.
func renderStatus(bodySize int, tag, prefix, name string, maxW int) *image.RGBA {
	const s = panelScale
	padX, padY := 14*s, 6*s
	face := newFaces(float64(bodySize*s), false)
	if face.empty() {
		return nil
	}
	m := face.metrics()
	ascent, descent := m.Ascent.Ceil(), m.Descent.Ceil()
	tag = printable(face, tag)
	prefix = printable(face, prefix)
	name = printable(face, name)
	preW := face.measure(prefix).Ceil()
	if tag != "" {
		preW += face.measure(tag).Ceil()
	}
	maxW *= s

	cut := false
	width := func(nm string) int {
		return padX + preW + face.measure(" "+nm).Ceil() + padX
	}
	rs := []rune(name)
	for len(rs) > 5 && width(string(rs)) > maxW {
		rs = rs[:len(rs)-1]
		cut = true
	}
	rest := " " + string(rs)
	if cut {
		rest += ".."
	}

	w := padX + preW + face.measure(rest).Ceil() + padX
	h := ascent + descent + 2*padY
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := statusBG
	if tag != "" {
		bg = statusBGLock
	}
	roundRect(img, 0, 0, w, h, statusRad, bg, statusBorder)

	y := padY + ascent
	x := padX
	if tag != "" {
		face.draw(img, tag, x, y, statusTag)
		x += face.measure(tag).Ceil()
	}
	face.draw(img, prefix, x, y, helpKey)
	face.draw(img, rest, x+face.measure(prefix).Ceil(), y, helpDesc)
	return downsample(img, s)
}

// renderTooltip paints a transient message on a small translucent pill
// styled like the status line; maxW is the logical width to stay inside.
// A non-nil swatch draws a small rounded colour chip before the text, which
// the colour picker uses to show the value it copied. It pairs with
// viewer.tooltip / viewer.tooltipSwatch, which store the message and deadline.
func renderTooltip(bodySize int, msg string, maxW int, swatch *color.RGBA) *image.RGBA {
	const s = panelScale
	padX, padY := 14*s, 6*s
	face := newFaces(float64(bodySize*s), false)
	if face.empty() {
		return nil
	}
	m := face.metrics()
	ascent, descent := m.Ascent.Ceil(), m.Descent.Ceil()
	msg = printable(face, msg)
	maxW *= s
	// the chip occupies a square as tall as the text plus a gap before the
	// message, so both the panel width and the text offset shift by it.
	chip := 0
	if swatch != nil {
		chip = ascent + 2*s
	}
	gap := 0
	if swatch != nil {
		gap = 8 * s
	}
	width := func(t string) int { return padX + chip + gap + face.measure(t).Ceil() + padX }
	rs := []rune(msg)
	for len(rs) > 5 && width(string(rs)) > maxW {
		rs = rs[:len(rs)-1]
	}
	if width(string(rs)) < width(msg) {
		rs = append(rs, '.', '.') // trimmed: show it, don't silently shorten
		msg = string(rs)
	}
	w := width(msg)
	h := ascent + descent + 2*padY
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	roundRect(img, 0, 0, w, h, statusRad, statusBG, statusBorder)
	x := padX
	if swatch != nil {
		y0 := (h - chip) / 2
		roundRect(img, x, y0, x+chip, y0+chip, 2*s, *swatch, statusBorder)
		x += chip + gap
	}
	face.draw(img, msg, x, padY+ascent, helpDesc)
	return downsample(img, s)
}

// printable drops runes no face in the chain can render: better a short name
// than a row of tofu boxes.
func printable(t *textFace, s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || t.has(r) {
			out = append(out, r)
		}
	}
	return string(out)
}
