// SVG text rendering.
//
// oksvg parses paths, shapes and paint servers but has no support for <text>:
// the element is silently dropped, so a chart's labels and units vanish (and
// with them every Chinese caption). This file fills that gap. The document is
// walked a second time, CSS and the inherited presentation attributes are
// resolved, glyphs are selected from the system's fonts with per-rune
// fallback, and their outlines are filled through the same bounded scanner
// that draws paths.
//
// Text is painted after the vector tree, which matches the usual document
// order (labels sit on top of the artwork). A shape painted after a label
// would cover it in a browser but not here; that ordering is rare enough to
// be an acceptable trade for not rebuilding oksvg's paint pipeline.
package main

import (
	"bytes"
	"encoding/xml"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/srwiley/rasterx"
	"golang.org/x/image/colornames"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// svgText draws every <text> element of an SVG document onto dst. view is the
// viewBox-to-pixels transform oksvg applied to the paths. Parse errors are
// ignored: a missing label is better than a failed image.
func svgText(data []byte, dst *image.RGBA, w, h int, view rasterx.Matrix2D) {
	if math.IsNaN(view.A) || math.IsInf(view.A, 0) || math.IsNaN(view.F) || math.IsInf(view.F, 0) {
		return // degenerate viewBox, same as the paths
	}
	root, err := parseSVGTree(data)
	if err != nil || root == nil {
		return
	}
	doc := &svgDoc{root: root}
	doc.css = collectCSS(root)

	sc := newSVGScanner(dst, w, h)
	tr := &svgTextRenderer{doc: doc, sc: sc, view: view}
	st := styleMap{"font-size": "16px", "fill": "black"}
	tr.walk(root, st, rasterx.Identity, 1, 16)
}

// ---------------------------------------------------------------------------
// document tree

type svgElem struct {
	name  string
	attrs map[string]string
	kids  []svgKid
}

type svgKid struct {
	text string
	elem *svgElem
}

func parseSVGTree(data []byte) (*svgElem, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var root *svgElem
	var stack []*svgElem
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Keep whatever was parsed; text early in a malformed file still
			// helps, and oksvg has its own recovery mode for the shapes.
			return root, nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			e := &svgElem{name: t.Name.Local, attrs: make(map[string]string, len(t.Attr))}
			for _, a := range t.Attr {
				e.attrs[a.Name.Local] = a.Value
			}
			if len(stack) == 0 {
				root = e
			} else {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, svgKid{elem: e})
			}
			stack = append(stack, e)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, svgKid{text: string(t)})
			}
		}
	}
	return root, nil
}

// Elements whose text content must never be painted. defs, clipPath and mask
// are not drawn directly, and title/desc/style/script are metadata.
var svgSkip = map[string]bool{
	"defs": true, "clipPath": true, "mask": true, "pattern": true,
	"marker": true, "symbol": true, "title": true, "desc": true,
	"style": true, "script": true, "metadata": true, "linearGradient": true,
	"radialGradient": true, "filter": true,
}

// ---------------------------------------------------------------------------
// CSS

type cssDecl struct {
	prop, val string
}

type cssRule struct {
	kind  byte // 'e' element, 'c' class, 'i' id
	name  string
	spec  int
	decls []cssDecl
}

type svgDoc struct {
	root *svgElem
	css  []cssRule
}

func collectCSS(root *svgElem) []cssRule {
	var rules []cssRule
	var walk func(*svgElem)
	walk = func(e *svgElem) {
		if e.name == "style" {
			for _, k := range e.kids {
				rules = append(rules, parseCSS(k.text)...)
			}
			return
		}
		for _, k := range e.kids {
			if k.elem != nil {
				walk(k.elem)
			}
		}
	}
	walk(root)
	return rules
}

func parseCSS(src string) []cssRule {
	for {
		i := strings.Index(src, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(src[i+2:], "*/")
		if j < 0 {
			src = src[:i]
			break
		}
		src = src[:i] + " " + src[i+2+j+2:]
	}
	var rules []cssRule
	for len(src) > 0 {
		open := strings.IndexByte(src, '{')
		close := strings.IndexByte(src, '}')
		if open < 0 || close < open {
			break
		}
		sel := strings.TrimSpace(src[:open])
		decls := parseDecls(src[open+1 : close])
		src = src[close+1:]
		if strings.HasPrefix(sel, "@") {
			continue
		}
		for _, part := range strings.Split(sel, ",") {
			part = strings.TrimSpace(part)
			if part == "" || strings.ContainsAny(part, " \t\n>[:(+~") {
				continue
			}
			r := cssRule{decls: decls}
			switch part[0] {
			case '.':
				r.kind, r.name, r.spec = 'c', part[1:], 10
			case '#':
				r.kind, r.name, r.spec = 'i', part[1:], 100
			default:
				r.kind, r.name, r.spec = 'e', strings.ToLower(part), 1
			}
			if r.name != "" {
				rules = append(rules, r)
			}
		}
	}
	return rules
}

func parseDecls(s string) []cssDecl {
	var out []cssDecl
	for _, part := range strings.Split(s, ";") {
		i := strings.IndexByte(part, ':')
		if i < 0 {
			continue
		}
		p := strings.ToLower(strings.TrimSpace(part[:i]))
		v := strings.TrimSpace(part[i+1:])
		v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important"))
		if p != "" && v != "" {
			out = append(out, cssDecl{p, v})
		}
	}
	return out
}

func (r cssRule) matches(e *svgElem) bool {
	switch r.kind {
	case 'e':
		return strings.EqualFold(e.name, r.name)
	case 'c':
		for _, c := range strings.Fields(e.attrs["class"]) {
			if c == r.name {
				return true
			}
		}
	case 'i':
		return e.attrs["id"] == r.name
	}
	return false
}

// Presentation attributes that take part in text styling. Geometry (x, y,
// transform, ...) is read directly from the element instead.
var svgStyleAttrs = []string{
	"fill", "fill-opacity", "font-family", "font-size", "font-weight",
	"font-style", "text-anchor", "letter-spacing", "opacity", "color",
	"display", "visibility", "white-space",
}

type styleMap map[string]string

// styleFor resolves one element's style: inherited values, then presentation
// attributes, then stylesheet rules, then the inline style attribute.
func (d *svgDoc) styleFor(e *svgElem, parent styleMap) styleMap {
	s := make(styleMap, len(parent)+8)
	for k, v := range parent {
		if k != "opacity" { // opacity applies per element, it does not inherit
			s[k] = v
		}
	}
	for _, p := range svgStyleAttrs {
		if v, ok := e.attrs[p]; ok {
			s[p] = v
		}
	}
	// Cascade stylesheet declarations by specificity, document order for ties.
	best := map[string]struct {
		spec, order int
		val         string
	}{}
	for i, r := range d.css {
		if !r.matches(e) {
			continue
		}
		for _, decl := range r.decls {
			cur, ok := best[decl.prop]
			if !ok || r.spec > cur.spec || (r.spec == cur.spec && i >= cur.order) {
				best[decl.prop] = struct {
					spec, order int
					val         string
				}{r.spec, i, decl.val}
			}
		}
	}
	for p, b := range best {
		s[p] = b.val
	}
	for _, decl := range parseDecls(e.attrs["style"]) {
		s[decl.prop] = decl.val
	}
	return s
}

// ---------------------------------------------------------------------------
// transforms

func parseTransform(s string) rasterx.Matrix2D {
	m := rasterx.Identity
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == ',') {
			i++
		}
		start := i
		for i < len(s) && s[i] != '(' {
			i++
		}
		if i >= len(s) {
			break
		}
		name := strings.TrimSpace(s[start:i])
		i++
		argStart := i
		for i < len(s) && s[i] != ')' {
			i++
		}
		args := parseNumbers(s[argStart:i])
		if i < len(s) {
			i++
		}
		var t rasterx.Matrix2D
		switch name {
		case "matrix":
			if len(args) >= 6 {
				t = rasterx.Matrix2D{A: args[0], B: args[1], C: args[2], D: args[3], E: args[4], F: args[5]}
			} else {
				continue
			}
		case "translate":
			if len(args) >= 1 {
				dy := 0.0
				if len(args) >= 2 {
					dy = args[1]
				}
				t = rasterx.Identity.Translate(args[0], dy)
			} else {
				continue
			}
		case "scale":
			if len(args) >= 1 {
				sy := args[0]
				if len(args) >= 2 {
					sy = args[1]
				}
				t = rasterx.Matrix2D{A: args[0], D: sy}
			} else {
				continue
			}
		case "rotate":
			if len(args) >= 1 {
				a := args[0] * math.Pi / 180
				t = rasterx.Identity.Rotate(a)
				if len(args) >= 3 {
					t = rasterx.Identity.Translate(args[1], args[2]).Mult(t).Translate(-args[1], -args[2])
				}
			} else {
				continue
			}
		case "skewX":
			if len(args) >= 1 {
				t = rasterx.Matrix2D{A: 1, C: math.Tan(args[0] * math.Pi / 180), D: 1}
			} else {
				continue
			}
		case "skewY":
			if len(args) >= 1 {
				t = rasterx.Matrix2D{A: 1, B: math.Tan(args[0] * math.Pi / 180), D: 1}
			} else {
				continue
			}
		default:
			continue
		}
		m = m.Mult(t)
	}
	return m
}

func parseNumbers(s string) []float64 {
	var out []float64
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t\r\n,")
		if s == "" {
			break
		}
		i := 0
		for i < len(s) && !strings.ContainsRune(" \t\r\n,", rune(s[i])) {
			i++
		}
		v, err := strconv.ParseFloat(s[:i], 64)
		if err == nil {
			out = append(out, v)
		}
		s = s[i:]
	}
	return out
}

// firstNumber reads a coordinate attribute such as "12" or "12px". Lists
// ("10 20 30") use the first value.
func firstNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if i := strings.IndexAny(s, " \t\r\n,"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "px"), "pt")
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// ---------------------------------------------------------------------------
// style values

func parseLength(s string, parent, def float64) float64 {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return def
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "%"):
		mult = parent / 100
		s = strings.TrimSuffix(s, "%")
	case strings.HasSuffix(s, "px"):
		s = strings.TrimSuffix(s, "px")
	case strings.HasSuffix(s, "pt"):
		mult = 96.0 / 72.0
		s = strings.TrimSuffix(s, "pt")
	case strings.HasSuffix(s, "rem"):
		mult = 16
		s = strings.TrimSuffix(s, "rem")
	case strings.HasSuffix(s, "em"):
		mult = parent
		s = strings.TrimSuffix(s, "em")
	case strings.HasSuffix(s, "ex"):
		mult = parent / 2
		s = strings.TrimSuffix(s, "ex")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return def
	}
	return v * mult
}

func opacityOf(s styleMap) float64 {
	v, ok := s["opacity"]
	if !ok {
		return 1
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 1
	}
	return math.Max(0, math.Min(1, f))
}

func svgHidden(s styleMap) bool {
	return s["display"] == "none" || s["visibility"] == "hidden" || s["visibility"] == "collapse"
}

func anchorOf(s styleMap) string {
	switch strings.TrimSpace(s["text-anchor"]) {
	case "middle":
		return "middle"
	case "end":
		return "end"
	}
	return "start"
}

func boldOf(s styleMap) bool {
	v := strings.ToLower(strings.TrimSpace(s["font-weight"]))
	if v == "bold" || v == "bolder" {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 600
}

func italicOf(s styleMap) bool {
	v := strings.ToLower(strings.TrimSpace(s["font-style"]))
	return v == "italic" || v == "oblique"
}

func letterSpacingOf(s styleMap) float64 {
	v := strings.TrimSpace(s["letter-spacing"])
	if v == "" || v == "normal" {
		return 0
	}
	return parseLength(v, 0, 0)
}

func fillOf(s styleMap, alpha float64) color.NRGBA {
	v := strings.TrimSpace(s["fill"])
	if v == "" {
		v = "black"
	}
	lo := strings.ToLower(v)
	if lo == "none" || lo == "transparent" {
		return color.NRGBA{}
	}
	if lo == "currentcolor" {
		v = s["color"]
		if v == "" {
			v = "black"
		}
	}
	c, ok := parseColor(v)
	if !ok {
		// url(#gradient) and other paint servers are not supported; fall back
		// to the current text colour so the label stays readable.
		if strings.HasPrefix(lo, "url(") {
			if cc, ok2 := parseColor(s["color"]); ok2 {
				c = cc
			} else {
				c = color.NRGBA{0, 0, 0, 255}
			}
		} else {
			c = color.NRGBA{0, 0, 0, 255}
		}
	}
	if v, ok := s["fill-opacity"]; ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			alpha *= math.Max(0, math.Min(1, f))
		}
	}
	c.A = uint8(math.Round(float64(c.A) * alpha))
	return c
}

func parseColor(v string) (color.NRGBA, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	if c, ok := colornames.Map[v]; ok {
		return color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A}, true
	}
	if strings.HasPrefix(v, "#") {
		h := v[1:]
		hex := func(i int) (uint8, bool) {
			n, err := strconv.ParseUint(h[i:i+2], 16, 8)
			return uint8(n), err == nil
		}
		switch len(h) {
		case 3, 4:
			vals := make([]uint8, len(h))
			for i := range h {
				n, err := strconv.ParseUint(string(h[i])+string(h[i]), 16, 8)
				if err != nil {
					return color.NRGBA{}, false
				}
				vals[i] = uint8(n)
			}
			c := color.NRGBA{R: vals[0], G: vals[1], B: vals[2], A: 255}
			if len(vals) == 4 {
				c.A = vals[3]
			}
			return c, true
		case 6, 8:
			r, ok1 := hex(0)
			g, ok2 := hex(2)
			b, ok3 := hex(4)
			if !ok1 || !ok2 || !ok3 {
				return color.NRGBA{}, false
			}
			c := color.NRGBA{R: r, G: g, B: b, A: 255}
			if len(h) == 8 {
				if a, ok := hex(6); ok {
					c.A = a
				}
			}
			return c, true
		}
		return color.NRGBA{}, false
	}
	if i := strings.IndexByte(v, '('); i > 0 && strings.HasSuffix(v, ")") {
		fn := strings.TrimSpace(v[:i])
		if fn != "rgb" && fn != "rgba" {
			return color.NRGBA{}, false
		}
		body := v[i+1 : len(v)-1]
		body = strings.ReplaceAll(body, "/", " ")
		body = strings.ReplaceAll(body, ",", " ")
		parts := strings.Fields(body)
		if len(parts) < 3 {
			return color.NRGBA{}, false
		}
		ch := func(s string) uint8 {
			pct := strings.HasSuffix(s, "%")
			s = strings.TrimSuffix(s, "%")
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return 0
			}
			if pct {
				f = f * 255 / 100
			}
			return uint8(math.Max(0, math.Min(255, math.Round(f))))
		}
		c := color.NRGBA{R: ch(parts[0]), G: ch(parts[1]), B: ch(parts[2]), A: 255}
		if len(parts) >= 4 {
			s := parts[3]
			pct := strings.HasSuffix(s, "%")
			s = strings.TrimSuffix(s, "%")
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				if pct {
					f /= 100
				}
				c.A = uint8(math.Round(math.Max(0, math.Min(1, f)) * 255))
			}
		}
		return c, true
	}
	return color.NRGBA{}, false
}

// ---------------------------------------------------------------------------
// fonts

var (
	svgFontFiles sync.Map // path -> *opentype.Font
	svgFontChain sync.Map // requested family list + weight/style -> []*opentype.Font
)

func loadFontFile(path string) *opentype.Font {
	if v, ok := svgFontFiles.Load(path); ok {
		return v.(*opentype.Font)
	}
	var f *opentype.Font
	if b, err := os.ReadFile(path); err == nil {
		if pf, err := parseSfnt(b); err == nil {
			f = pf
		}
	}
	svgFontFiles.Store(path, f)
	return f
}

// generic families and platform aliases that fontconfig cannot map to a real
// file on every OS; the system chain already covers them.
var svgGenericFamily = map[string]bool{
	"serif": true, "sans-serif": true, "monospace": true, "cursive": true,
	"fantasy": true, "system-ui": true, "ui-sans-serif": true,
	"ui-serif": true, "ui-monospace": true, "-apple-system": true,
	"blinkmacsystemfont": true, "inherit": true, "initial": true,
	"unset": true, "revert": true,
}

func splitFamilies(v string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ',':
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func svgFontsFor(style styleMap) []*opentype.Font {
	bold := boldOf(style)
	italic := italicOf(style)
	families := splitFamilies(style["font-family"])
	key := strings.Join(families, "\x00") + "|" + strconv.FormatBool(bold) + strconv.FormatBool(italic)
	if v, ok := svgFontChain.Load(key); ok {
		return v.([]*opentype.Font)
	}

	var chain []*opentype.Font
	seen := map[*opentype.Font]bool{}
	add := func(f *opentype.Font) {
		if f != nil && !seen[f] {
			seen[f] = true
			chain = append(chain, f)
		}
	}
	for _, fam := range families {
		if fam == "" || svgGenericFamily[strings.ToLower(fam)] {
			continue
		}
		pat := fam
		switch {
		case bold && italic:
			pat += ":style=Bold Italic"
		case bold:
			pat += ":style=Bold"
		case italic:
			pat += ":style=Italic"
		}
		add(loadFontFile(fcMatch(pat)))
	}
	for _, f := range systemFonts(bold) {
		add(f)
	}
	svgFontChain.Store(key, chain)
	return chain
}

// ---------------------------------------------------------------------------
// layout and drawing

type textGlyph struct {
	r     rune
	x, y  float64
	size  float64
	font  *opentype.Font
	gi    sfnt.GlyphIndex
	adv   float64
	fill  color.NRGBA
	xform rasterx.Matrix2D
	chunk int
}

type textChunk struct {
	startX float64
	anchor string
}

type svgTextRenderer struct {
	doc  *svgDoc
	sc   *svgScanner
	view rasterx.Matrix2D
	buf  sfnt.Buffer
}

func (r *svgTextRenderer) walk(e *svgElem, parent styleMap, t rasterx.Matrix2D, alpha, size float64) {
	if svgSkip[e.name] {
		return
	}
	s := r.doc.styleFor(e, parent)
	if s["display"] == "none" {
		return
	}
	size = parseLength(s["font-size"], size, size)
	alpha *= opacityOf(s)
	t = t.Mult(parseTransform(e.attrs["transform"]))

	if e.name == "text" {
		r.layoutText(e, s, t, alpha, size)
		return
	}
	for _, k := range e.kids {
		if k.elem != nil {
			r.walk(k.elem, s, t, alpha, size)
		}
	}
}

type svgTextLayout struct {
	r     *svgTextRenderer
	glyph []textGlyph
	chunk []textChunk

	x, y     float64
	style    styleMap
	xform    rasterx.Matrix2D
	alpha    float64
	size     float64
	anchor   string
	cur      int
	emitted  bool
	pending  bool
	preserve bool

	prevRune  rune
	prevFont  *opentype.Font
	prevGI    sfnt.GlyphIndex
	prevOK    bool
	prevChunk int
}

func (r *svgTextRenderer) layoutText(e *svgElem, s styleMap, t rasterx.Matrix2D, alpha, size float64) {
	if svgHidden(s) {
		return
	}
	l := &svgTextLayout{r: r, style: s, xform: t, alpha: alpha, size: size, anchor: anchorOf(s)}
	l.x, _ = firstNumber(e.attrs["x"])
	l.y, _ = firstNumber(e.attrs["y"])
	if v, ok := firstNumber(e.attrs["dx"]); ok {
		l.x += v
	}
	if v, ok := firstNumber(e.attrs["dy"]); ok {
		l.y += v
	}
	l.preserve = e.attrs["space"] == "preserve"
	l.newChunk()
	l.kids(e.kids)
	l.draw()
}

func (l *svgTextLayout) newChunk() {
	l.cur = len(l.chunk)
	l.chunk = append(l.chunk, textChunk{startX: l.x, anchor: l.anchor})
}

func (l *svgTextLayout) kids(kids []svgKid) {
	for _, k := range kids {
		if k.elem == nil {
			l.text(k.text)
			continue
		}
		switch k.elem.name {
		case "tspan", "textPath", "a":
			l.tspan(k.elem)
		case "title", "desc", "metadata":
			// not rendered
		}
	}
}

func (l *svgTextLayout) tspan(e *svgElem) {
	saveStyle, saveSize, saveAlpha, saveAnchor := l.style, l.size, l.alpha, l.anchor
	saveXform := l.xform
	s := l.r.doc.styleFor(e, l.style)
	l.style = s
	l.size = parseLength(s["font-size"], l.size, l.size)
	l.alpha *= opacityOf(s)
	l.xform = l.xform.Mult(parseTransform(e.attrs["transform"]))
	l.anchor = anchorOf(s)

	moved := false
	if v, ok := firstNumber(e.attrs["x"]); ok {
		l.x, moved = v, true
	}
	if v, ok := firstNumber(e.attrs["y"]); ok {
		l.y, moved = v, true
	}
	if v, ok := firstNumber(e.attrs["dx"]); ok {
		l.x += v
	}
	if v, ok := firstNumber(e.attrs["dy"]); ok {
		l.y += v
	}
	if moved {
		l.newChunk()
	}
	l.kids(e.kids)

	l.style, l.size, l.alpha, l.anchor, l.xform = saveStyle, saveSize, saveAlpha, saveAnchor, saveXform
}

func (l *svgTextLayout) text(s string) {
	space := func() {
		if l.preserve {
			l.emit(' ')
			return
		}
		if l.emitted {
			l.pending = true
		}
	}
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t', ' ':
			space()
			continue
		}
		if l.pending {
			l.pending = false
			l.emit(' ')
		}
		l.emit(r)
	}
}

func (l *svgTextLayout) emit(r rune) {
	f, gi, ok := l.fontFor(r)
	if !ok {
		return
	}
	ppem := fixed.Int26_6(math.Round(l.size * 64))
	if ppem <= 0 {
		return
	}
	adv, err := f.GlyphAdvance(&l.r.buf, gi, ppem, font.HintingNone)
	if err != nil {
		return
	}
	if l.prevOK && l.prevFont == f && l.prevChunk == l.cur && l.prevRune != 0 {
		if k, err := f.Kern(&l.r.buf, l.prevGI, gi, ppem, font.HintingNone); err == nil {
			l.x += float64(k) / 64
		}
	}
	l.glyph = append(l.glyph, textGlyph{
		r: r, x: l.x, y: l.y, size: l.size, font: f, gi: gi,
		adv: float64(adv) / 64, fill: fillOf(l.style, l.alpha), xform: l.xform, chunk: l.cur,
	})
	l.x += float64(adv)/64 + letterSpacingOf(l.style)
	l.emitted = true
	l.prevRune, l.prevFont, l.prevGI, l.prevOK, l.prevChunk = r, f, gi, true, l.cur
}

func (l *svgTextLayout) fontFor(r rune) (*opentype.Font, sfnt.GlyphIndex, bool) {
	for _, f := range svgFontsFor(l.style) {
		gi, err := f.GlyphIndex(&l.r.buf, r)
		if err == nil && gi != 0 {
			return f, gi, true
		}
	}
	return nil, 0, false
}

// draw applies text-anchor to each chunk and fills the glyph outlines. The
// outline path goes through rasterx's flattener so curves stay smooth, then
// through svgScanner, which clips to the glyph's own bounding box.
func (l *svgTextLayout) draw() {
	if len(l.glyph) == 0 {
		return
	}
	// Chunk widths, measured in the text's own coordinate system. The last
	// glyph may be a space, but trailing spaces are never emitted.
	for i := range l.chunk {
		width := 0.0
		last := -1
		for j := range l.glyph {
			if glyphChunk(l, j) != i {
				continue
			}
			last = j
		}
		if last >= 0 {
			width = l.glyph[last].x + l.glyph[last].adv - l.chunk[i].startX
		}
		shift := 0.0
		switch l.chunk[i].anchor {
		case "middle":
			shift = -width / 2
		case "end":
			shift = -width
		}
		if shift != 0 {
			for j := range l.glyph {
				if glyphChunk(l, j) == i {
					l.glyph[j].x += shift
				}
			}
		}
	}
	for i := range l.glyph {
		l.drawGlyph(&l.glyph[i])
	}
}

// glyphChunk recovers the chunk a glyph belongs to. While laying out, chunks
// are appended in order and glyphs record the chunk index implicitly by the
// number of chunks that existed when they were emitted; store that in the
// chunk field for the anchor pass.
func glyphChunk(l *svgTextLayout, j int) int {
	return l.glyph[j].chunk
}

func (l *svgTextLayout) drawGlyph(g *textGlyph) {
	if g.r == ' ' || g.fill.A == 0 || g.font == nil {
		return
	}
	ppem := fixed.Int26_6(math.Round(g.size * 64))
	if ppem <= 0 {
		return
	}
	segs, err := g.font.LoadGlyph(&l.r.buf, g.gi, ppem, nil)
	if err != nil {
		return
	}
	m := l.r.view.Mult(g.xform)
	pt := func(p fixed.Point26_6) fixed.Point26_6 {
		x := g.x + float64(p.X)/64
		y := g.y + float64(p.Y)/64
		tx, ty := m.Transform(x, y)
		return fixed.Point26_6{
			X: fixed.Int26_6(math.Round(tx * 64)),
			Y: fixed.Int26_6(math.Round(ty * 64)),
		}
	}
	sc := l.r.sc
	sc.Clear()
	sc.margin = 1
	f := rasterx.Filler{Scanner: sc}
	started := false
	for _, seg := range segs {
		switch seg.Op {
		case sfnt.SegmentOpMoveTo:
			if started {
				f.Stop(true)
			}
			f.Start(pt(seg.Args[0]))
			started = true
		case sfnt.SegmentOpLineTo:
			f.Line(pt(seg.Args[0]))
		case sfnt.SegmentOpQuadTo:
			f.QuadBezier(pt(seg.Args[0]), pt(seg.Args[1]))
		case sfnt.SegmentOpCubeTo:
			f.CubeBezier(pt(seg.Args[0]), pt(seg.Args[1]), pt(seg.Args[2]))
		}
	}
	if started {
		f.Stop(true)
	}
	sc.SetColor(g.fill)
	sc.Draw()
}
