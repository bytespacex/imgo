//go:build gui

// GUI image viewer: Ebitengine implementation.
// Build: go build -tags gui -o imgo .
//
// Keys:
//
//	? / F1       show / hide help overlay (up / down / j / k / pgup / pgdn /
//	             wheel scroll it when the list does not fit)
//	ESC          close the overlay; in the normal view, quit
//	Left/Right   prev / next image (same directory); k / j do the same
//	             with the hand on the home row
//	PgUp/PgDn    prev / next sibling directory (first image; a tooltip
//	             shows "[3/12] ~/dir" after every jump)
//	Space        toggle original size <-> fit to window
//	0            original size (1:1)
//	f            toggle fullscreen
//	+/-/wheel    zoom (wheel and Shift+/- around the cursor, +/- around the image centre)
//	r / t        rotate 90° clockwise / counter-clockwise
//	w / e        mirror left-right / up-down
//	i            toggle negative (invert; display-only)
//	s            toggle smooth (linear) / crisp (nearest) scaling
//	< / >        brightness down / up (Shift+, / Shift+.)
//	\            slideshow (2s; [ / ] tune the interval)
//	p            pause / resume animation playback
//	, / .        step animation one frame back / on (pauses it)
//	m / M        copy / move the image into an imgo/ dir beside it
//	o            open in an external program (list in IMGO_OPEN)
//	/            dir file list (j/k move, / filters, enter picks)
//	c            copy the colour under the cursor as hex (not in cursor mode)
//	v            measure: drag a rectangle, read its size in image / screen px
//	ctrl+p / u   copy the file path / file:// URI to the clipboard
//	drag         pan (when not fitted)
//	click        1:1 zoom, then hands-free pan following the mouse (esc leaves)
//	q / ESC      leave hands-free pan, then the slideshow, then quit
//
// A "[1/822][2.3M] name.png" status line is drawn in the top-left corner at
// all times.
//
// The default view is fit-to-window: small images are enlarged to fill the
// window, big ones shrunk, always keeping the aspect ratio. Fitting is
// re-applied automatically when the window or screen size changes.
package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/colorm"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	// Side-effect import: x11quiet installs xgb's log filter in its init, which
	// must run before ebiten's internal/ui opens the X connection during its own
	// init. See that package's doc comment for the whole story.
	_ "imgo/x11quiet"
)

// guiDefault is true: the imgo build opens a window directly, no terminal mode
var guiDefault = true

// slideshow timing: \ toggles at the default, [ and ] tune the interval
const (
	slideDefault = 2 * time.Second
	slideMin     = 500 * time.Millisecond
	slideMax     = 10 * time.Second
	slideStep    = 500 * time.Millisecond
)

// display adjustment: < and > scale the brightness by brightStep, clamped to
// [brightMin, brightMax]; brightNeutral leaves the pixels untouched.
const (
	brightMin     = 0.1
	brightMax     = 3.0
	brightStep    = 0.1
	brightNeutral = 1.0
)

// markDirName is the subdirectory "m" / "M" drop images into. It is created
// beside the image — inside the image's own directory — not at the process
// working directory, so a mark travels with the collection it belongs to.
const markDirName = "imgo"

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".bmp": true, ".tif": true, ".tiff": true,
	".ico": true, ".svg": true, ".svgz": true,
	".avif": true, ".heic": true, ".heif": true, ".jxl": true,
}

type viewer struct {
	files         []string // image list in the same directory
	idx           int
	img           *ebiten.Image
	frames        []animFrame // animation frames; len>1 means animated
	frameIdx      int
	isPDF              bool // current file is a multi-page PDF (pages == frames)
	pdfDPI             int  // raster resolution for PDF pages (0 = default 150)
	pdfReq             chan pdfReq
	pdfResult          chan pdfRendered
	pdfPending         map[int]bool // page indices requested but not yet applied
	pdfGen             int          // bumped on every load to discard stale results
	pdfWorkerStarted   bool
	frameNext     time.Time
	animPaused    bool          // animation held: , and . step it a frame at a time
	slide         bool          // slideshow: the next image comes on a timer
	slideNext     time.Time     // when the current slide expires
	slideEvery    time.Duration // interval between images (default 2s)
	zoom          float64
	offX          float64
	offY          float64
	rot           int     // quarter turns clockwise: 0 upright, 1 = 90° right
	flipX         bool    // mirrored left-right
	flipY         bool    // mirrored up-down
	invert        bool    // negative: RGB flipped (display-only, pixels untouched)
	bright        float64 // display brightness scale, 1 = neutral
	smooth        bool    // linear filtering when scaling; false for crisp pixels
	centered      bool    // image is centered; recomputed on every size change
	atFit         bool    // current view is "fit to window" (Space returns to it)
	dragging      bool
	dragX         int
	dragY         int
	pressX        int // press position, to tell a click from a drag
	pressY        int
	panLock       bool // hands-free panning: cursor captured, canvas follows mouse
	lockX         int  // last cursor position while locked (frame diff = delta)
	lockY         int
	lockPrimed    bool // false until the first captured frame (skip the jump)
	lockWasFit    bool // view state to restore when the mode ends
	winW          int
	winH          int
	pendW         int // candidate size waiting to be confirmed
	pendH         int
	pendN         int // frames the candidate has been seen unchanged
	savedW        int // last windowed size, re-asserted when leaving fullscreen
	savedH        int
	fullW         int // last size seen while fullscreen, to recognise stale reports
	fullH         int
	showHelp      bool          // help overlay visible
	helpScroll    int           // first help row shown while the list scrolls
	dim           *ebiten.Image // cached translucent overlay, rebuilt on resize
	checkImg      *ebiten.Image // cached transparency checkerboard, rebuilt on resize
	helpImg       *ebiten.Image // rendered help panel, system font
	helpKey       string        // body size the panel was built for
	statusImg     *ebiten.Image // rendered top status line, system font
	statusKey     string        // size+text the status line was built for
	statusPath    string        // file the cached size belongs to
	statusSize    string        // cached fmtSize output for statusPath
	sibs          []string      // sibling dirs with images; scanned on first PgUp/PgDn
	sibsDone      bool          // the sibling scan ran (the result may be empty)
	tipText       string        // transient tooltip message; empty = hidden
	tipUntil      time.Time     // when the current tooltip expires
	tipImg        *ebiten.Image // rendered tooltip panel, system font
	tipKey        string        // size+text the tooltip panel was built for
	tipSwatch     *color.RGBA   // optional colour chip drawn before the tooltip text
	measure       bool          // measure mode: drag out a rectangle instead of panning
	measDragging  bool          // a drag is in progress (button held)
	measAX        float64       // one corner, in source image pixel coordinates
	measAY        float64
	measBX        float64 // the other corner (moves during the drag)
	measBY        float64
	measImg       *ebiten.Image // rendered measure readout, system font
	measKey       string        // size+text the readout panel was built for
	showOpen      bool          // external-program launcher overlay visible
	openFilter    string        // typed prefix narrowing the launcher list
	openIdx       int           // highlighted program within the filtered list
	openProgs     []openProgram // parsed IMGO_OPEN, cached on first use
	openDone      bool          // the IMGO_OPEN parse ran (the result may be empty)
	openSkipRunes bool          // drop one frame of runes right after opening
	openImg       *ebiten.Image // rendered launcher panel, system font
	openKey       string        // filter+selection the launcher panel was built for
	showList      bool          // directory file-list panel visible
	listInput     bool          // the filter box has focus (keystrokes type, not navigate)
	listFilter    string        // filter text narrowing the file list
	listSel       int           // selected row within the filtered list
	listTop       int           // first visible row of the filtered list
	listSkipRunes bool          // drop one frame of runes right after focusing the filter
	listImg       *ebiten.Image // rendered file-list panel, system font
	listKey       string        // filter+selection the file-list panel was built for
}

// openProgram is one entry of IMGO_OPEN: the command to run together with its
// fixed arguments. The current image's path is appended as the final argument
// when the program is launched.
type openProgram struct {
	label string   // menu label, home written as "~" ("foot ~/bn/lf")
	argv  []string // command + fixed arguments, with ~ and $VAR expanded
}

// animFrame is a decoded animation frame, already on the GPU.
type animFrame struct {
	img   *ebiten.Image
	delay time.Duration
}

func runGUI(path string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		fatal(err)
	}
	v := &viewer{zoom: 1, winW: 1280, winH: 800, slideEvery: slideDefault, bright: brightNeutral, smooth: true}

	// dir: collect images inside it; file: collect sibling images (for arrow navigation)
	var dir string
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		dir = abs
	} else {
		dir, _ = filepath.Split(abs)
	}
	v.files = dirImages(dir)
	if len(v.files) == 0 {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			fatal(fmt.Errorf("%s: no images in directory", abs))
		}
		v.files = []string{abs}
	}
	// opening a dir: start at first image; opening a file: locate that file
	for i, f := range v.files {
		if f == abs {
			v.idx = i
			break
		}
	}

	if err := v.load(); err != nil {
		fatal(err)
	}

	ebiten.SetWindowSize(v.winW, v.winH)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowTitle("imgo - " + filepath.Base(abs))
	if err := ebiten.RunGame(v); err != nil {
		fatal(err)
	}
}

// dirImages lists the image files directly inside dir (no recursion),
// sorted by name: the file list for one directory.
func dirImages(dir string) []string {
	var out []string
	entries, _ := os.ReadDir(dir) // unreadable: empty, which callers treat as "nothing here"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if imageExts[strings.ToLower(filepath.Ext(e.Name()))] {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// siblingDirs lists the subdirectories of parent that directly contain
// image files, in name order — the set PgUp / PgDn walks. Symlinked
// directories are not followed (DirEntry.IsDir is false for them, matching
// how symlinked image files are treated), hidden directories are included
// just as hidden image files are.
func siblingDirs(parent string) []string {
	var out []string
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(parent, e.Name())
		if len(dirImages(d)) > 0 {
			out = append(out, d)
		}
	}
	return out
}

func (v *viewer) load() error {
	path := v.files[v.idx]
	v.isPDF = false
	// PDF: multi-page, rendered on demand into the existing frames model so
	// , / . step through pages exactly like animation frames.
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		return v.loadPDF(path)
	}
	pdfStartPage = 1 // -p only applies to the command-line PDF; other files clear it
	// animated formats (gif / apng / animated webp) first, static otherwise
	if raw := decodeAnim(path); len(raw) > 0 {
		v.frames = make([]animFrame, len(raw))
		for i, fr := range raw {
			v.frames[i] = animFrame{img: ebiten.NewImageFromImage(fr.img), delay: fr.delay}
		}
	} else {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		img, _, err := image.Decode(f)
		if err != nil {
			// non-stdlib formats via decodeFile (svg/avif/heic etc)
			f.Close()
			img, err = decodeFile(path)
			if err != nil {
				return err
			}
		} else {
			f.Close()
		}
		v.frames = []animFrame{{img: ebiten.NewImageFromImage(img)}}
	}
	v.frameIdx = 0
	v.img = v.frames[0].img
	v.frameNext = time.Now().Add(v.frameDelay())
	// a fresh image starts playing, upright: the pause and the view transforms
	// all belong to the picture that was on screen, not the next one
	v.animPaused = false
	// rotation and mirrors belong to one image only: the next starts upright
	v.rot = 0
	v.flipX, v.flipY = false, false
	// the negative and brightness adjustments belong to one image only too:
	// the next starts as a plain, neutrally-lit picture
	v.invert = false
	v.bright = brightNeutral
	// a drag measures the picture that was on screen, not the next one
	v.measDragging = false
	v.fit()
	ebiten.SetWindowTitle(fmt.Sprintf("imgo - %s", filepath.Base(path)))
	return nil
}

// loadPDF opens a PDF as a multi-page document: one frame per page, rendered
// lazily. We only rasterize what the viewer actually reaches, so a 500-page
// file holds at most a few pages in memory at once. The document starts
// paused on page 1, like a still, and , / . step through pages.
func (v *viewer) loadPDF(path string) error {
	total, err := pdfTotalPages(path)
	if err != nil {
		return err
	}
	if total < 1 {
		return fmt.Errorf("pdf: no pages")
	}
	if v.pdfDPI == 0 {
		v.pdfDPI = 150
	}
	if !v.pdfWorkerStarted {
		v.pdfReq = make(chan pdfReq, 16)
		v.pdfResult = make(chan pdfRendered, 16)
		v.pdfPending = make(map[int]bool)
		go v.pdfWorker()
		v.pdfWorkerStarted = true
	}
	v.pdfGen++
	v.pdfPending = make(map[int]bool)
	v.isPDF = true
	v.frames = make([]animFrame, total)
	v.frameIdx = 0
	// render first page now so something shows immediately; surface the real
	// backend error (mutool or poppler) rather than a guess at which failed.
	if err := v.ensurePage(0); err != nil {
		return err
	}
	if v.frames[0].img == nil {
		return fmt.Errorf("pdf: failed to render page 1")
	}
	v.img = v.frames[0].img
	// honour -p from the command line once: jump to the requested page, then
	// reset so later files opened with Left/Right start on page 1.
	if pdfStartPage > 1 {
		t := pdfStartPage - 1
		if t >= 0 && t < len(v.frames) {
			v.frameIdx = t
			v.ensurePage(v.frameIdx)
			v.img = v.frames[v.frameIdx].img
		}
		pdfStartPage = 1
	}
	v.frameNext = time.Now().Add(v.frameDelay())
	v.animPaused = true // documents do not auto-play
	v.rot = 0
	v.flipX, v.flipY = false, false
	v.invert = false
	v.bright = brightNeutral
	v.measDragging = false
	v.fit()
	ebiten.SetWindowTitle(fmt.Sprintf("imgo - %s", filepath.Base(path)))
	return nil
}

// pdfReq asks the background worker to rasterize one page.
// gen lets stale results (from a file we have since left) be discarded.
type pdfReq struct {
	gen  int
	idx  int
	path string
	dpi  int
}

// pdfRendered is a worker's answer; gen must match the current file.
type pdfRendered struct {
	gen int
	idx int
	img image.Image
	err error
}

// pdfWorker renders requested pages off the main thread so turning pages
// never blocks on pdftoppm. Results come back through pdfResult and are
// applied on the main thread by pdfDrain (ebiten textures must be made there).
func (v *viewer) pdfWorker() {
	for req := range v.pdfReq {
		img, err := pdfRenderPage(req.path, req.idx+1, req.dpi)
		v.pdfResult <- pdfRendered{gen: req.gen, idx: req.idx, img: img, err: err}
	}
}

// pdfRequest asks the worker to render idx if it is not already loaded or in
// flight. Main-thread only, so pdfPending needs no lock.
func (v *viewer) pdfRequest(idx int) {
	if !v.isPDF || idx < 0 || idx >= len(v.frames) {
		return
	}
	if v.frames[idx].img != nil || v.pdfPending[idx] {
		return
	}
	v.pdfPending[idx] = true
	select {
	case v.pdfReq <- pdfReq{gen: v.pdfGen, idx: idx, path: v.files[v.idx], dpi: v.pdfDPI}:
	default:
		// queue full: drop the request, it will be re-issued by the next
		// ensurePage so we never block the render frame here
		delete(v.pdfPending, idx)
	}
}

// pdfDrain applies any finished renders. Called every frame from Update.
func (v *viewer) pdfDrain() {
	for {
		select {
		case r := <-v.pdfResult:
			delete(v.pdfPending, r.idx)
			if r.gen != v.pdfGen {
				continue // file changed while this was rendering
			}
			if r.err != nil {
				v.tooltip("pdf: " + r.err.Error())
				continue
			}
			if r.idx >= 0 && r.idx < len(v.frames) && v.frames[r.idx].img == nil {
				v.frames[r.idx] = animFrame{img: ebiten.NewImageFromImage(r.img)}
			}
		default:
			return
		}
	}
}

// ensurePage makes sure page idx is displayed: if missing it is rendered
// synchronously (only slow on a first view or a far jump), then the two pages
// ahead and one behind are requested asynchronously so normal reading never
// waits on pdftoppm.
func (v *viewer) ensurePage(idx int) error {
	if !v.isPDF || idx < 0 || idx >= len(v.frames) {
		return nil
	}
	if v.frames[idx].img == nil {
		img, err := pdfRenderPage(v.files[v.idx], idx+1, v.pdfDPI)
		if err != nil {
			v.tooltip("pdf: " + err.Error())
			return err
		}
		v.frames[idx] = animFrame{img: ebiten.NewImageFromImage(img)}
	}
	v.pdfRequest(idx - 1)
	v.pdfRequest(idx + 1)
	v.pdfRequest(idx + 2) // one extra lead page smooths holding ',' / '.'
	return nil
}

// gotoPage jumps to an absolute page (Home / End) and shows it.
func (v *viewer) gotoPage(idx int) {
	if !v.isPDF || idx < 0 || idx >= len(v.frames) {
		return
	}
	v.animPaused = true
	v.frameIdx = idx
	v.ensurePage(v.frameIdx)
	v.img = v.frames[v.frameIdx].img
}

// step moves to a neighbouring image (wrapping at both ends) and restarts the
// slideshow countdown, so however a new image appears — arrow key or timer —
// it gets the full interval on screen.
func (v *viewer) step(delta int) {
	v.idx = (v.idx + delta + len(v.files)) % len(v.files)
	if err := v.load(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	v.slideNext = time.Now().Add(v.slideEvery)
}

// stepDir moves the view into the previous (-1) or next (+1) sibling
// directory that contains images, landing on its first image. The sibling
// set is scanned once, on the first press, and cached after that; every
// jump re-reads the target directory, so files added or removed outside
// the viewer show up. At either end the view stays where it is — with only
// one directory, or the first/last of several, the key answers with a
// tooltip naming the current directory instead of doing nothing.
func (v *viewer) stepDir(delta int) {
	if len(v.files) == 0 {
		return
	}
	cur := filepath.Dir(v.files[v.idx])
	if !v.sibsDone {
		v.sibs = siblingDirs(filepath.Dir(cur))
		v.sibsDone = true
	}
	i := slices.Index(v.sibs, cur)
	if i < 0 {
		return // paranoia: the current dir is not in its own sibling scan
	}
	j := min(max(i+delta, 0), len(v.sibs)-1)
	if j != i {
		files := dirImages(v.sibs[j])
		if len(files) == 0 {
			return // emptied since the scan; nothing to jump to
		}
		v.files = files
		v.idx = 0
		v.step(0) // load the new dir's first image, re-arm the slideshow
	}
	// "[3/12] ~/photos/shots": position within the jump set, then the full
	// path of the directory now on screen, home written as "~"
	v.tooltip(fmt.Sprintf("[%d/%d] %s", j+1, len(v.sibs), prettyPath(v.sibs[j])))
}

// deleteCurrent sends the current file to the trash and moves the view onto
// whatever now sits in its slot (wrapping like the arrow keys). With nothing
// left the window closes: there is nothing left to show.
func (v *viewer) deleteCurrent() error {
	if len(v.files) == 0 {
		return ebiten.Termination
	}
	if err := moveToTrash(v.files[v.idx]); err != nil {
		return err
	}
	v.files = append(v.files[:v.idx], v.files[v.idx+1:]...)
	if len(v.files) == 0 {
		return ebiten.Termination
	}
	v.step(0)
	return nil
}

// markCurrent copies (move=false) or moves (move=true) the current image into
// an imgo/ subdirectory beside it, creating the directory on demand. A name
// already taken there is auto-renamed with a counter, so marking the same file
// twice never overwrites the first mark. Copying leaves the view on the image;
// moving takes it out of the directory, so the view advances to the next image
// and the file list shrinks, exactly like delete (and closes when nothing is
// left).
func (v *viewer) markCurrent(move bool) error {
	if len(v.files) == 0 {
		return nil
	}
	src := v.files[v.idx]
	dir := filepath.Join(filepath.Dir(src), markDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := uniquePath(dir, filepath.Base(src))
	if move {
		// rename first (instant, same filesystem); a cross-device mark falls
		// back to a copy-then-unlink, cleaning up the destination on failure
		if err := os.Rename(src, dst); err != nil {
			if !crossDevice(err) {
				return err
			}
			if err := copyFile(src, dst); err != nil {
				return err
			}
			if err := os.Remove(src); err != nil {
				os.Remove(dst)
				return err
			}
		}
		v.files = append(v.files[:v.idx], v.files[v.idx+1:]...)
		if len(v.files) == 0 {
			return ebiten.Termination
		}
		v.step(0)
		v.tooltip("moved to " + markDirName + "/" + filepath.Base(dst))
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	v.tooltip("copied to " + markDirName + "/" + filepath.Base(dst))
	return nil
}

// uniquePath returns a path in dir for base that does not exist yet: base
// itself when free, otherwise base-1, base-2, ... with the counter inserted
// before the extension so the file keeps its decoder-friendly suffix.
func uniquePath(dir, base string) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	dst := filepath.Join(dir, base)
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			return dst
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
	}
}

// parseOpenPrograms reads IMGO_OPEN: a comma-separated list of commands, each
// of which may carry fixed arguments ("gimp.appimage,foot lf,mpv"). The image
// path is appended as the final argument at launch time. A leading ~ expands
// to the home directory and $VAR / ${VAR} to the environment, so
// "~/pg/gimp/AppRun" and "$HOME/bin/foo" both resolve. The label is rebuilt
// from the resolved fields with the home directory written as "~" again, so the
// menu shows "~/bn/lf" rather than an absolute "/home/me/bn/lf". Blank entries
// are dropped.
func parseOpenPrograms() []openProgram {
	var out []openProgram
	for _, entry := range strings.Split(os.Getenv("IMGO_OPEN"), ",") {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}
		labels := make([]string, len(fields))
		for i, f := range fields {
			fields[i] = expandHome(os.ExpandEnv(f))
			labels[i] = prettyPath(fields[i])
		}
		out = append(out, openProgram{label: strings.Join(labels, " "), argv: fields})
	}
	return out
}

// expandHome expands a leading ~ to the user's home directory. It runs per
// field, after os.ExpandEnv has handled $VAR / ${VAR}, so a plain argument is
// left untouched and "~/x" and "$HOME/x" resolve to the same path.
func expandHome(s string) string {
	if s != "~" && !strings.HasPrefix(s, "~/") {
		return s
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return s
	}
	if s == "~" {
		return home
	}
	return filepath.Join(home, s[2:])
}

// openPrograms is parseOpenPrograms run once and cached, so the environment is
// read at most once even though the panel may be opened repeatedly.
func (v *viewer) openPrograms() []openProgram {
	if !v.openDone {
		v.openProgs = parseOpenPrograms()
		v.openDone = true
	}
	return v.openProgs
}

// openMatches narrows the configured programs to those located by the typed
// filter (the "first letter" locate). An empty filter matches everything.
func (v *viewer) openMatches() []openProgram {
	progs := v.openPrograms()
	if v.openFilter == "" {
		return progs
	}
	prefix := strings.ToLower(v.openFilter)
	var out []openProgram
	for _, p := range progs {
		if openLabelMatches(p.label, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// openLabelMatches reports whether prefix (already lower-cased) is the start of
// any path segment or whitespace-separated word of label. So "g" locates
// "~/pg/gimp/AppRun" (segment "gimp") and "l" locates "foot lf" (word "lf"),
// while a bare command like "mpv" is still found by its first letter.
func openLabelMatches(label, prefix string) bool {
	seg := strings.FieldsFunc(label, func(r rune) bool {
		return r == '/' || unicode.IsSpace(r)
	})
	for _, s := range seg {
		if strings.HasPrefix(strings.ToLower(s), prefix) {
			return true
		}
	}
	return false
}

// launchOpen runs a configured program on the current image, detached: the
// child inherits the environment (so a GUI app finds Wayland / X11) and starts
// in the image's own directory (so a file manager opens there). Start returns
// immediately; a background Wait reaps the child so a finished program leaves
// no zombie while the viewer keeps running.
func (v *viewer) launchOpen(p openProgram) {
	if len(v.files) == 0 {
		return
	}
	file := v.files[v.idx]
	argv := append(append([]string{}, p.argv...), file)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = filepath.Dir(file)
	if err := cmd.Start(); err != nil {
		v.tooltip(fmt.Sprintf("%s: %v", p.label, err))
		return
	}
	go cmd.Wait()
	v.tooltip(fmt.Sprintf("launching %s (%s)", p.label, filepath.Base(file)))
}

// toggleOpen is the "o" handler: a single configured program runs at once with
// no menu; several open the picker; none reports that IMGO_OPEN is unset.
func (v *viewer) toggleOpen() {
	progs := v.openPrograms()
	switch len(progs) {
	case 0:
		v.tooltip("IMGO_OPEN is not set")
	case 1:
		v.launchOpen(progs[0])
	default:
		v.showOpen = true
		v.openFilter = ""
		v.openIdx = 0
		// the 'o' that opened the menu is handed to us as a printable rune a
		// frame later; swallow the next frame's runes so it never becomes the
		// first filter letter (the list would flash and flip to "no match").
		v.openSkipRunes = true
	}
}

// closeOpen hides the launcher and re-arms the slideshow countdown: the show
// was paused while the panel was up (Update returns early), so it should resume
// with a full interval, exactly as closing the help panel does.
func (v *viewer) closeOpen() {
	v.showOpen = false
	v.slideNext = time.Now().Add(v.slideEvery)
}

// openList shows the directory file list with the image now on screen selected.
// The panel owns the keyboard while it is up, which also pauses the slideshow;
// like the help and launcher panels it re-arms the countdown when it closes.
func (v *viewer) openList() {
	v.showList = true
	v.listInput = false
	v.listFilter = ""
	v.listSel, v.listTop = 0, 0
	matches := v.listMatches()
	for r, fi := range matches {
		if fi == v.idx {
			v.listSel = r
			break
		}
	}
	v.clampList(len(matches))
}

// closeList hides the file list and re-arms the slideshow countdown, matching
// closeOpen and the help panel.
func (v *viewer) closeList() {
	v.showList = false
	v.listInput = false
	v.listFilter = ""
	v.slideNext = time.Now().Add(v.slideEvery)
}

// listMatches indexes v.files down to the rows the filter keeps, in order. An
// empty filter keeps everything; otherwise the base name is matched
// case-insensitively as a substring, which is friendlier than a prefix for
// finding one file among many.
func (v *viewer) listMatches() []int {
	out := make([]int, 0, len(v.files))
	if v.listFilter == "" {
		for i := range v.files {
			out = append(out, i)
		}
		return out
	}
	q := strings.ToLower(v.listFilter)
	for i, f := range v.files {
		if strings.Contains(strings.ToLower(filepath.Base(f)), q) {
			out = append(out, i)
		}
	}
	return out
}

// clampList keeps the selection and the scroll window legal for a filtered
// list of n rows, scrolling only as far as needed to keep the selection shown.
func (v *viewer) clampList(n int) {
	if n <= 0 {
		v.listSel, v.listTop = 0, 0
		return
	}
	if v.listSel < 0 {
		v.listSel = 0
	}
	if v.listSel >= n {
		v.listSel = n - 1
	}
	vis := listVisibleRows(helpBodySize(v.winH), v.winH, n)
	if v.listSel < v.listTop {
		v.listTop = v.listSel
	}
	if v.listSel >= v.listTop+vis {
		v.listTop = v.listSel - vis + 1
	}
	maxTop := n - vis
	if maxTop < 0 {
		maxTop = 0
	}
	if v.listTop > maxTop {
		v.listTop = maxTop
	}
	if v.listTop < 0 {
		v.listTop = 0
	}
}

// updateListInput handles the focused filter box: printable keys extend the
// filter, backspace trims it, enter commits and esc cancels, both returning to
// navigation. j / k reach the box here, which is the whole point of the modal
// split — you leave navigation before letters mean anything.
func (v *viewer) updateListInput(typed []rune) error {
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		v.listFilter = ""
		v.listInput = false
		v.listSel, v.listTop = 0, 0
		v.clampList(len(v.listMatches()))
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
		v.listInput = false
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) && v.listFilter != "" {
		rs := []rune(v.listFilter)
		v.listFilter = string(rs[:len(rs)-1])
		v.listSel, v.listTop = 0, 0
		v.clampList(len(v.listMatches()))
		return nil
	}
	added := false
	for _, r := range typed {
		if unicode.IsPrint(r) {
			v.listFilter += string(r)
			added = true
		}
	}
	if added {
		v.listSel, v.listTop = 0, 0
		v.clampList(len(v.listMatches()))
	}
	return nil
}

// updateListNav handles the file list in navigation mode: j / k and the arrows
// move one row, pgup / pgdn and ctrl+b / ctrl+f page, the wheel scrolls, enter
// shows the selected image, / focuses the filter, and esc / q close.
func (v *viewer) updateListNav() error {
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) ||
		inpututil.IsKeyJustPressed(ebiten.KeyQ) {
		v.closeList()
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeySlash) {
		v.listInput = true
		// the '/' itself arrives as a rune next frame: drop it so it does not
		// become the first filter character
		v.listSkipRunes = true
		return nil
	}
	matches := v.listMatches()
	n := len(matches)
	vis := listVisibleRows(helpBodySize(v.winH), v.winH, n)
	step := 0
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowDown),
		inpututil.IsKeyJustPressed(ebiten.KeyJ):
		step = 1
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowUp),
		inpututil.IsKeyJustPressed(ebiten.KeyK):
		step = -1
	case inpututil.IsKeyJustPressed(ebiten.KeyPageDown),
		inpututil.IsKeyJustPressed(ebiten.KeyF) && ebiten.IsKeyPressed(ebiten.KeyControl):
		step = vis
	case inpututil.IsKeyJustPressed(ebiten.KeyPageUp),
		inpututil.IsKeyJustPressed(ebiten.KeyB) && ebiten.IsKeyPressed(ebiten.KeyControl):
		step = -vis
	}
	if _, dy := ebiten.Wheel(); dy != 0 {
		if dy > 0 {
			step--
		} else {
			step++
		}
	}
	if step != 0 {
		v.listSel += step
		v.clampList(n)
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnter) && n > 0 {
		v.idx = matches[v.listSel]
		v.closeList()
		if err := v.load(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	return nil
}

// crossDevice reports whether a rename failed because source and destination
// live on different filesystems (EXDEV).
func crossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// copyFile copies src to dst (exclusive create), keeping the source's
// permission bits so the restored file behaves like the original.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// imgSize is the unzoomed size of the image on screen: a quarter turn
// swaps width and height. Fit, centering and the checkerboard all size
// themselves from it, so a rotated view stays coherent everywhere.
func (v *viewer) imgSize() (float64, float64) {
	b := v.img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	if v.rot%2 == 1 {
		return h, w
	}
	return w, h
}

// fit scales the image to fill the window, keeping the aspect ratio: small
// images are enlarged, big ones shrunk. This is the view after every load.
func (v *viewer) fit() {
	if v.img == nil {
		return
	}
	iw, ih := v.imgSize()
	v.zoom = math.Min(float64(v.winW)/iw, float64(v.winH)/ih)
	v.atFit = true
	v.centered = true
	v.center()
}

// actual scales the image to 1:1, one image pixel per screen pixel.
func (v *viewer) actual() {
	v.zoom = 1
	v.atFit = false
	v.centered = true
	v.center()
}

// viewAltered reapplies the view after a rotation: while the image fills
// the window the new orientation is re-fitted, a centered 1:1 view
// recenters, and a panned view keeps its zoom and position untouched.
func (v *viewer) viewAltered() {
	switch {
	case v.atFit:
		v.fit()
	case v.centered:
		v.center()
	}
}

// setPanLock turns hands-free panning on or off (the MassiGra style): on, the
// view jumps to 1:1 and the cursor is captured, so moving the mouse pans the
// canvas with no button held and no edge can stop it; off, the cursor is
// released and the pre-click view state comes back.
func (v *viewer) setPanLock(on bool) {
	if on == v.panLock {
		return
	}
	v.panLock = on
	if on {
		v.lockWasFit = v.atFit
		v.actual() // one click: straight to the original size
		ebiten.SetCursorMode(ebiten.CursorModeCaptured)
		x, y := ebiten.CursorPosition()
		v.lockX, v.lockY, v.lockPrimed = x, y, false
	} else {
		ebiten.SetCursorMode(ebiten.CursorModeVisible)
		v.dragging = false
		if v.lockWasFit {
			v.fit()
		}
	}
}

// zoomPt is the point zooming happens around: the cursor normally, but the
// window centre while the cursor is captured, because captured coordinates
// keep growing past the window and mean nothing as a screen position.
func (v *viewer) zoomPt() image.Point {
	if v.panLock {
		return image.Pt(v.winW/2, v.winH/2)
	}
	x, y := ebiten.CursorPosition()
	return image.Pt(x, y)
}

// zoomTarget is the point the - / = keys zoom around: the cursor while Shift
// is held (precise, exactly like the wheel), the image's own centre otherwise,
// so plain keyboard zooming never depends on where the mouse happens to rest.
func (v *viewer) zoomTarget() image.Point {
	if ebiten.IsKeyPressed(ebiten.KeyShift) {
		return v.zoomPt()
	}
	return v.imageCenterPt()
}

// imageCenterPt is the screen position of the image's own centre: the middle of
// the displayed (rotated, scaled, panned) rectangle.
func (v *viewer) imageCenterPt() image.Point {
	iw, ih := v.imgSize()
	return image.Pt(int(v.offX+iw*v.zoom/2), int(v.offY+ih*v.zoom/2))
}

// setSize records a new view size and keeps the image placed correctly in it.
// A fitted view is re-fitted; any other view is shifted by half the size
// delta, so a panned image stays panned around the window centre instead of
// sliding off the edge when the window (or screen) changes size.
func (v *viewer) setSize(w, h int) {
	if w == v.winW && h == v.winH {
		return
	}
	if ebiten.IsFullscreen() {
		v.fullW, v.fullH = w, h // remember for the stale-report check
	} else {
		v.savedW, v.savedH = w, h // remember the windowed size for later
	}
	dx, dy := w-v.winW, h-v.winH
	v.winW, v.winH = w, h
	if v.atFit {
		v.fit()
	} else {
		v.offX += float64(dx) / 2
		v.offY += float64(dy) / 2
		if v.centered {
			v.center()
		}
	}
	v.debugf("size %dx%d (was %dx%d) zoom=%.2f off=%.0f,%.0f fit=%v centred=%v",
		w, h, w-dx, h-dy, v.zoom, v.offX, v.offY, v.atFit, v.centered)
}

// setSizeStable filters out size flip-flop before calling setSize. Verified on
// the user's Wayland session: after leaving fullscreen the reported size
// alternates between the window size and the old fullscreen size every single
// frame, so any rule that picks "the larger" or "the more recent" value ends
// up bouncing the image off screen. Only a size that holds still is applied.
func (v *viewer) setSizeStable(w, h int) {
	if w == v.winW && h == v.winH {
		v.pendN = 0
		return
	}
	if w == v.pendW && h == v.pendH {
		v.pendN++
	} else {
		v.pendW, v.pendH, v.pendN = w, h, 1
	}
	if v.pendN >= 2 {
		v.setSize(w, h)
		v.pendN = 0
	}
}

// debugf prints view state when IMGO_DEBUG=1, for tracking down resize bugs.
func (v *viewer) debugf(format string, a ...any) {
	if os.Getenv("IMGO_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "imgo: "+format+"\n", a...)
}

func (v *viewer) center() {
	if v.img == nil {
		return
	}
	iw, ih := v.imgSize()
	v.offX = (float64(v.winW) - iw*v.zoom) / 2
	v.offY = (float64(v.winH) - ih*v.zoom) / 2
}

func (v *viewer) Update() error {
	// Drain the text-input queue every frame. A keystroke's rune is delivered
	// one tick after the key event, so the 'o' that opens the launcher would
	// otherwise be read as the first filter letter the frame the menu appears
	// (the list flashes, then flips to "no match"). Draining consumes it even
	// when no panel is open; openSkipRunes drops the frame right after opening.
	typed := ebiten.AppendInputChars(nil)
	if v.openSkipRunes {
		v.openSkipRunes = false
		typed = nil
	}
	if v.listSkipRunes {
		v.listSkipRunes = false
		typed = nil
	}
	// apply any PDF pages the background worker finished since last frame
	v.pdfDrain()
	// help overlay: ? (Shift+/) or F1. While it is open it owns the keyboard,
	// so reading it cannot zoom, pan or flip pages by accident.
	if inpututil.IsKeyJustPressed(ebiten.KeyF1) ||
		(inpututil.IsKeyJustPressed(ebiten.KeySlash) && ebiten.IsKeyPressed(ebiten.KeyShift)) {
		v.showHelp = !v.showHelp
		// the captured cursor is invisible: show it while reading the panel
		// and capture again (re-primed, no jump) when the panel closes.
		if v.panLock {
			if v.showHelp {
				ebiten.SetCursorMode(ebiten.CursorModeVisible)
			} else {
				ebiten.SetCursorMode(ebiten.CursorModeCaptured)
				v.lockPrimed = false
			}
		}
		// the panel paused the slideshow: count a full interval from here
		if !v.showHelp {
			v.slideNext = time.Now().Add(v.slideEvery)
		}
		return nil
	}
	if v.showHelp {
		// esc / q close the overlay first; they only quit from the normal view
		if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyQ) {
			v.showHelp = false
			v.slideNext = time.Now().Add(v.slideEvery)
			return nil
		}
		// the list scrolls when it does not fit: arrows / j k move a row,
		// pgup pgdn a page, the wheel follows the hand. The offset is clamped
		// to the rows that actually fit, so it can never rest on blanks.
		step := 0
		switch {
		case inpututil.IsKeyJustPressed(ebiten.KeyArrowDown),
			inpututil.IsKeyJustPressed(ebiten.KeyJ):
			step = 1
		case inpututil.IsKeyJustPressed(ebiten.KeyArrowUp),
			inpututil.IsKeyJustPressed(ebiten.KeyK):
			step = -1
		case inpututil.IsKeyJustPressed(ebiten.KeyPageDown):
			step = helpVisibleRows(helpBodySize(v.winH), v.winH)
		case inpututil.IsKeyJustPressed(ebiten.KeyPageUp):
			step = -helpVisibleRows(helpBodySize(v.winH), v.winH)
		}
		if _, dy := ebiten.Wheel(); dy != 0 {
			if dy > 0 {
				step-- // wheel up reads as "earlier rows"
			} else {
				step++
			}
		}
		if step != 0 {
			last := len(helpRows) - helpVisibleRows(helpBodySize(v.winH), v.winH)
			if last < 0 {
				last = 0
			}
			v.helpScroll += step
			if v.helpScroll > last {
				v.helpScroll = last
			}
			if v.helpScroll < 0 {
				v.helpScroll = 0
			}
		}
		return nil
	}
	// o: external-program launcher. While open it owns the keyboard: letters
	// narrow the list by prefix, a single match launches at once, enter opens
	// the highlighted one, and esc / q / o close without running anything.
	if v.showOpen {
		if inpututil.IsKeyJustPressed(ebiten.KeyEscape) ||
			inpututil.IsKeyJustPressed(ebiten.KeyQ) ||
			inpututil.IsKeyJustPressed(ebiten.KeyO) {
			v.closeOpen()
			return nil
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) && v.openFilter != "" {
			v.openFilter = v.openFilter[:len(v.openFilter)-1]
			v.openIdx = 0
			return nil
		}
		matches := v.openMatches()
		added := false
		for _, r := range typed {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				v.openFilter += string(r)
				added = true
			}
		}
		if added {
			v.openIdx = 0
			if m := v.openMatches(); len(m) == 1 {
				v.launchOpen(m[0]) // the typed prefix left a single match
				v.closeOpen()
			}
			return nil
		}
		switch {
		case inpututil.IsKeyJustPressed(ebiten.KeyArrowUp),
			inpututil.IsKeyJustPressed(ebiten.KeyK):
			if v.openIdx > 0 {
				v.openIdx--
			}
		case inpututil.IsKeyJustPressed(ebiten.KeyArrowDown),
			inpututil.IsKeyJustPressed(ebiten.KeyJ):
			if v.openIdx < len(matches)-1 {
				v.openIdx++
			}
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyEnter) && len(matches) > 0 {
			if v.openIdx >= len(matches) {
				v.openIdx = len(matches) - 1
			}
			v.launchOpen(matches[v.openIdx])
			v.closeOpen()
		}
		return nil
	}
	// / opens the file list; a second / focuses the filter box, so "//" reads
	// as "browse this directory, then search it". While the panel is up it
	// owns the keyboard: j / k and the arrows move the selection, pgup / pgdn
	// and ctrl+b / ctrl+f page, enter shows the selected image, and / focuses
	// a filter box (there every key types, so j / k no longer navigate).
	// esc / q close the panel. It is navigation-first precisely so j / k stay
	// usable for moving.
	if v.showList {
		if v.listInput {
			return v.updateListInput(typed)
		}
		return v.updateListNav()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyO) {
		v.toggleOpen()
	}
	// "/" opens the list. Shift+/ is "?" (the help toggle, handled above), but
	// the slash key is still "just pressed" on that frame, so guard the shift.
	if inpututil.IsKeyJustPressed(ebiten.KeySlash) && !ebiten.IsKeyPressed(ebiten.KeyShift) {
		v.openList()
	}
	// quit (esc / q leave the measure mode, then hands-free panning, then the
	// slideshow, then quit — so a running show or a selection is never lost to
	// a stray keypress)
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyQ) {
		if v.measure {
			v.measure = false
			v.measDragging = false
			return nil
		}
		if v.panLock {
			v.setPanLock(false)
			return nil
		}
		if v.slide {
			v.slide = false
			return nil
		}
		return ebiten.Termination
	}
	// navigate: arrows, or vim's k / j for the other hand.
	// Shift+K / Shift+J step PDF pages instead of moving between files.
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
		v.step(-1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
		v.step(1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyK) {
		if ebiten.IsKeyPressed(ebiten.KeyShift) && v.isPDF {
			v.stepFrame(-1)
		} else {
			v.step(-1)
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyJ) {
		if ebiten.IsKeyPressed(ebiten.KeyShift) && v.isPDF {
			v.stepFrame(1)
		} else {
			v.step(1)
		}
	}
	// PgUp / PgDn: previous / next sibling directory with images
	if inpututil.IsKeyJustPressed(ebiten.KeyPageUp) {
		v.stepDir(-1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyPageDown) {
		v.stepDir(1)
	}
	// , / . step animation one frame back / on; the same physical keys with
	// Shift are < / > and adjust brightness instead. Stepping pauses first, so
	// a single press always lands on the neighbouring frame and leaves it
	// there; resuming re-arms the delay of the frame now on screen, so
	// playback continues from here instead of jumping ahead. p yields to Ctrl
	// so Ctrl+P (copy path) is never read as a pause.
	if inpututil.IsKeyJustPressed(ebiten.KeyP) &&
		!ebiten.IsKeyPressed(ebiten.KeyControl) && !v.isPDF && len(v.frames) > 1 {
		v.animPaused = !v.animPaused
		if !v.animPaused {
			v.frameNext = time.Now().Add(v.frameDelay())
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyComma) {
		if ebiten.IsKeyPressed(ebiten.KeyShift) {
			v.adjustBright(-brightStep)
		} else {
			v.stepFrame(-1)
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyPeriod) {
		if ebiten.IsKeyPressed(ebiten.KeyShift) {
			v.adjustBright(brightStep)
		} else {
			v.stepFrame(1)
		}
	}
	// Home / End jump to the first / last PDF page (no-op for plain images).
	if v.isPDF {
		if inpututil.IsKeyJustPressed(ebiten.KeyHome) {
			v.gotoPage(0)
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyEnd) {
			v.gotoPage(len(v.frames) - 1)
		}
	}
	// slideshow: \ toggles it, [ / ] shorten or extend the interval. While
	// the help panel is open Update returns above, so reading it pauses the
	// show; closing the panel re-arms the countdown with a full interval.
	if inpututil.IsKeyJustPressed(ebiten.KeyBackslash) {
		v.slide = !v.slide
		v.slideNext = time.Now().Add(v.slideEvery)
	}
	if v.slide {
		if inpututil.IsKeyJustPressed(ebiten.KeyLeftBracket) && v.slideEvery > slideMin {
			v.slideEvery -= slideStep
			v.slideNext = time.Now().Add(v.slideEvery)
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyRightBracket) && v.slideEvery < slideMax {
			v.slideEvery += slideStep
			v.slideNext = time.Now().Add(v.slideEvery)
		}
		if len(v.files) > 1 && time.Now().After(v.slideNext) {
			v.step(1)
		}
	}
	// d: current file to the trash, view moves on to the next image
	if inpututil.IsKeyJustPressed(ebiten.KeyD) {
		if err := v.deleteCurrent(); err != nil {
			if err == ebiten.Termination {
				return err
			}
			fmt.Fprintln(os.Stderr, err)
		}
	}
	// m / M: copy / move the current file into an imgo/ subdirectory beside it
	// (auto-renamed when the name is taken). Copying keeps the view put; moving
	// advances to the next image like delete, so the list follows the directory.
	if inpututil.IsKeyJustPressed(ebiten.KeyM) {
		if err := v.markCurrent(ebiten.IsKeyPressed(ebiten.KeyShift)); err != nil {
			if err == ebiten.Termination {
				return err
			}
			fmt.Fprintln(os.Stderr, err)
		}
	}
	// clipboard: ctrl+p copies the absolute path (plain text), ctrl+u the
	// file:// URI as text/uri-list so browsers, file managers and chat apps
	// treat the paste as a file. Both answer through the tooltip.
	if len(v.files) > 0 {
		if inpututil.IsKeyJustPressed(ebiten.KeyP) && ebiten.IsKeyPressed(ebiten.KeyControl) {
			v.copyToClipboard("path", v.files[v.idx], "")
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyU) && ebiten.IsKeyPressed(ebiten.KeyControl) {
			v.copyToClipboard("uri", fileURI(v.files[v.idx]), "text/uri-list")
		}
	}
	// c: copy the colour under the cursor as hex. Only in the plain view:
	// hands-free panning captures the cursor (it cannot be aimed) and the
	// slideshow races the file change, so both modes are excluded. Ctrl is
	// required to be up so a future Ctrl+C binding stays free.
	if inpututil.IsKeyJustPressed(ebiten.KeyC) && !ebiten.IsKeyPressed(ebiten.KeyControl) &&
		!v.panLock && !v.slide {
		v.pickColor()
	}
	// v: measure mode. A drag reads out the rectangle's size in source image
	// pixels and on-screen pixels, and copies it. Like the colour picker it
	// needs the cursor and a still picture, so toggleMeasure leaves hands-free
	// panning and stops the slideshow itself.
	if inpututil.IsKeyJustPressed(ebiten.KeyV) && !ebiten.IsKeyPressed(ebiten.KeyControl) {
		v.toggleMeasure()
	}
	// zoom: the wheel always follows the cursor; plain - / = zoom about the
	// image's own centre, so the keyboard need not aim the mouse, while Shift
	// plus those keys zoom about the cursor exactly as the wheel does.
	if _, dy := ebiten.Wheel(); dy != 0 {
		v.zoomAt(v.zoomPt(), 1.1*dy)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEqual) || inpututil.IsKeyJustPressed(ebiten.KeyKPAdd) {
		v.zoomAt(v.zoomTarget(), 0.5)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyMinus) || inpututil.IsKeyJustPressed(ebiten.KeyKPSubtract) {
		v.zoomAt(v.zoomTarget(), -0.5)
	}
	// space: toggle original size <-> fit to window; 0: original size
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		if v.atFit {
			v.actual()
		} else {
			v.fit()
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.Key0) {
		v.actual()
	}
	// r / t rotate 90° clockwise / counter-clockwise, w / e mirror
	// left-right / up-down; a mirror keeps the on-screen rectangle, so only
	// rotation re-fits
	if inpututil.IsKeyJustPressed(ebiten.KeyR) {
		v.rot = (v.rot + 1) % 4
		v.viewAltered()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyT) {
		v.rot = (v.rot + 3) % 4
		v.viewAltered()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyW) {
		v.flipX = !v.flipX
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyE) {
		v.flipY = !v.flipY
	}
	// i: toggle the negative (display-only; the pixels on disk are untouched)
	if inpututil.IsKeyJustPressed(ebiten.KeyI) {
		v.invert = !v.invert
		if v.invert {
			v.tooltip("negative on")
		} else {
			v.tooltip("negative off")
		}
	}
	// s: smooth scaling (linear) or crisp pixels (nearest). Smooth is the
	// default because scaled-down photos and SVG labels otherwise alias.
	if inpututil.IsKeyJustPressed(ebiten.KeyS) {
		v.smooth = !v.smooth
		if v.smooth {
			v.tooltip("smooth scaling on")
		} else {
			v.tooltip("crisp pixels (nearest)")
		}
	}
	// fullscreen: under Sway let the compositor do it. ebiten's own
	// SetFullscreen leaves its internal size stuck at the fullscreen value on
	// exit (WindowSize() and the canvas both keep reporting 2560x1440 while
	// the window is really 1276x698), which threw the image off screen.
	// swaymsg drives the transition from the outside, so the configure events
	// reaching ebiten are always correct. Elsewhere fall back to ebiten, with
	// the windowed size re-asserted on exit.
	if inpututil.IsKeyJustPressed(ebiten.KeyF) {
		sway := os.Getenv("SWAYSOCK") != "" && exec.Command("swaymsg", "fullscreen", "toggle").Run() == nil
		if !sway {
			if ebiten.IsFullscreen() {
				ebiten.SetFullscreen(false)
				if v.savedW > 0 {
					ebiten.SetWindowSize(v.savedW, v.savedH)
				}
			} else {
				ebiten.SetFullscreen(true)
			}
		}
		v.atFit = true
		v.centered = true
		v.fit()
	}
	// click (press -> release with almost no movement) toggles hands-free
	// panning: zoom to 1:1 and capture the cursor, after which the canvas
	// follows the mouse with no button held. A real drag stays a drag, and
	// while captured any click leaves the mode again.
	if v.measure {
		// measure mode owns the left button: a drag draws a selection instead
		// of panning, and a click never enters hands-free panning.
		v.updateMeasure()
	} else {
		if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
			v.dragging = !v.panLock
			x, y := ebiten.CursorPosition()
			v.dragX, v.dragY = x, y
			v.pressX, v.pressY = x, y
		}
		if inpututil.IsMouseButtonJustReleased(ebiten.MouseButtonLeft) {
			x, y := ebiten.CursorPosition()
			if v.panLock || (math.Abs(float64(x-v.pressX)) < 4 && math.Abs(float64(y-v.pressY)) < 4) {
				v.setPanLock(!v.panLock)
			}
			v.dragging = false
		}
		if v.dragging {
			x, y := ebiten.CursorPosition()
			v.offX += float64(x - v.dragX)
			v.offY += float64(y - v.dragY)
			v.dragX, v.dragY = x, y
			v.atFit = false
			v.centered = false
		}
	}
	// hands-free panning: each frame's captured cursor delta moves the canvas.
	// The cursor is locked inside the window (GLFW virtual position keeps
	// growing past every edge), so this never gets stuck at a screen border.
	if v.panLock {
		x, y := ebiten.CursorPosition()
		if v.lockPrimed {
			v.offX += float64(x - v.lockX)
			v.offY += float64(y - v.lockY)
			v.atFit = false
			v.centered = false
		}
		v.lockX, v.lockY = x, y
		v.lockPrimed = true
	}
	// animation playback: one frame at a time, each with its own delay. While
	// paused (p) the clock is held and , and . step the frames by hand.
	if len(v.frames) > 1 && !v.isPDF && !v.animPaused && time.Now().After(v.frameNext) {
		v.frameIdx = (v.frameIdx + 1) % len(v.frames)
		f := v.frames[v.frameIdx]
		v.img = f.img
		v.frameNext = time.Now().Add(v.frameDelay())
	}
	return nil
}

// frameDelay is how long the frame now on screen is held. A frame with no
// delay of its own (or a corrupt zero) falls back to 100ms, the same floor
// the playback loop has always used.
func (v *viewer) frameDelay() time.Duration {
	d := v.frames[v.frameIdx].delay
	if d <= 0 {
		return 100 * time.Millisecond
	}
	return d
}

// stepFrame moves the animation one frame back (-1) or on (+1) and holds it
// there, so , and . can inspect a paused sequence frame by frame. Wrapping at
// both ends matches the arrow keys, and stepping while the show is running
// pauses it: one press means "stop here on the neighbouring frame".
func (v *viewer) stepFrame(delta int) {
	if len(v.frames) < 2 {
		return // a still image has nothing to step through
	}
	v.animPaused = true
	v.frameIdx = (v.frameIdx + delta + len(v.frames)) % len(v.frames)
	v.ensurePage(v.frameIdx)
	f := v.frames[v.frameIdx]
	v.img = f.img
}

// adjustBright nudges the display brightness by delta and clamps it to the
// supported range. The step lands on a clean decimal, so the tooltip reads
// back a tidy figure (1.2, 0.8) with no floating-point fuzz.
func (v *viewer) adjustBright(delta float64) {
	b := v.bright
	if b <= 0 {
		b = brightNeutral // a zero-value viewer starts from neutral
	}
	b = math.Round((b+delta)*10) / 10
	if b < brightMin {
		b = brightMin
	}
	if b > brightMax {
		b = brightMax
	}
	v.bright = b
	v.tooltip(fmt.Sprintf("brightness %.1fx", b))
}

func (v *viewer) zoomAt(cursor image.Point, delta float64) {
	old := v.zoom
	v.zoom *= 1 + delta*0.2
	if v.zoom < 0.01 {
		v.zoom = 0.01
	}
	if v.zoom > 64 {
		v.zoom = 64
	}
	// zoom centered on cursor
	k := v.zoom / old
	v.offX = float64(cursor.X) - (float64(cursor.X)-v.offX)*k
	v.offY = float64(cursor.Y) - (float64(cursor.Y)-v.offY)*k
	v.atFit = false
	v.centered = false
}

// viewSize is the size the user is actually seeing: the canvas size, which is
// what Layout was told and what screen.Bounds() reports. ebiten's WindowSize()
// is deliberately not consulted. On Wayland it can stay frozen at whatever
// SetWindowSize asked for at startup — verified on Sway with a tiled window:
// WindowSize() kept reporting the initial 1280x800 while the canvas was
// 640x1404. Taking the smaller of the two there mixed a real width with a
// stale height, so a fitted view swapped between 640x1404 and 640x800 every
// frame: the image appeared to flicker and jump. (At half size the stale
// height happened to be larger than the real one, so the minimum landed on the
// real size and nothing moved.)
//
// A fullscreen transition can still leave the canvas reporting the old
// fullscreen size; if that exact size was recorded while fullscreen, the last
// known windowed size is used until the canvas catches up.
func (v *viewer) viewSize(cw, ch int) (int, int) {
	if v.savedW > 0 && cw == v.fullW && ch == v.fullH {
		return v.savedW, v.savedH
	}
	return cw, ch
}

// Transparency checkerboard: the classic light checks (#FFFFFF / #CCCCCC)
// under the image, so alpha is actually visible instead of blending into
// black. The pattern is screen-anchored and cached at window size.
const (
	checkerCell  = 16  // square edge in screen pixels
	checkerTileN = 256 // repeating block; a multiple of 2*checkerCell keeps the grid aligned
)

var (
	checkerLight   = color.RGBA{255, 255, 255, 255}
	checkerDark    = color.RGBA{204, 204, 204, 255}
	checkerTileImg *ebiten.Image // built on first use; Draw runs on one goroutine
)

// checkerPattern paints one checkerTileN block starting from a light square
// at the origin.
func checkerPattern() *image.RGBA {
	rgba := image.NewRGBA(image.Rect(0, 0, checkerTileN, checkerTileN))
	for y := 0; y < checkerTileN; y++ {
		for x := 0; x < checkerTileN; x++ {
			c := checkerLight
			if (x/checkerCell+y/checkerCell)%2 == 1 {
				c = checkerDark
			}
			i := rgba.PixOffset(x, y)
			rgba.Pix[i] = c.R
			rgba.Pix[i+1] = c.G
			rgba.Pix[i+2] = c.B
			rgba.Pix[i+3] = c.A
		}
	}
	return rgba
}

func checkerTile() *ebiten.Image {
	if checkerTileImg == nil {
		checkerTileImg = ebiten.NewImageFromImage(checkerPattern())
	}
	return checkerTileImg
}

// buildChecker fills a w*h texture by blitting the repeating block at
// multiples of checkerTileN: the tile straddling the right/bottom edge is
// clipped by the target, so every pixel lands on the global grid.
func buildChecker(w, h int) *ebiten.Image {
	img := ebiten.NewImage(w, h)
	img.Fill(checkerLight)
	tile := checkerTile()
	for y := 0; y < h; y += checkerTileN {
		for x := 0; x < w; x += checkerTileN {
			op := &ebiten.DrawImageOptions{}
			op.GeoM.Translate(float64(x), float64(y))
			img.DrawImage(tile, op)
		}
	}
	return img
}

// drawChecker paints the checkerboard under the image area only: the whole
// window is checkered first (one cached draw), then black masks leave the
// pattern visible exactly where the image is, so the letterbox around it
// stays black as before. Semi-transparent pixels therefore composite over
// the checks instead of over black.
func (v *viewer) drawChecker(screen *ebiten.Image, w, h int) {
	if v.checkImg == nil || v.checkImg.Bounds().Dx() != w || v.checkImg.Bounds().Dy() != h {
		v.checkImg = buildChecker(w, h)
	}
	screen.DrawImage(v.checkImg, nil)

	black := color.RGBA{0, 0, 0, 255}
	fill := func(x, y, w, h float64) {
		vector.FillRect(screen, float32(x), float32(y), float32(w), float32(h), black, false)
	}
	x0, y0 := v.offX, v.offY
	iw, ih := v.imgSize()
	x1 := x0 + iw*v.zoom
	y1 := y0 + ih*v.zoom
	cx0, cy0 := math.Max(0, x0), math.Max(0, y0)
	cx1, cy1 := math.Min(float64(w), x1), math.Min(float64(h), y1)
	if cx1 <= cx0 || cy1 <= cy0 { // image fully panned off screen
		fill(0, 0, float64(w), float64(h))
		return
	}
	if cy0 > 0 {
		fill(0, 0, float64(w), cy0)
	}
	if cy1 < float64(h) {
		fill(0, cy1, float64(w), float64(h)-cy1)
	}
	if cx0 > 0 {
		fill(0, cy0, cx0, cy1-cy0)
	}
	if cx1 < float64(w) {
		fill(cx1, cy0, float64(w)-cx1, cy1-cy0)
	}
}

// viewMatrix builds the draw matrix for a w*h image under the given view
// state. The flips act in image space, the rotation turns the result by rot
// quarter turns clockwise (the screen y axis points down, so ebiten's
// positive angle turns clockwise), and zoom plus position land the image in
// the rotated rectangle at (offX, offY) — the same rectangle imgSize
// reports, which is what fit, centering and the checkerboard assume.
func viewMatrix(w, h, rot int, flipX, flipY bool, zoom, offX, offY float64) *ebiten.DrawImageOptions {
	op := &ebiten.DrawImageOptions{}
	fx, fy, ox, oy := 1.0, 1.0, 0.0, 0.0
	if flipX {
		fx, ox = -1, float64(w)
	}
	if flipY {
		fy, oy = -1, float64(h)
	}
	op.GeoM.Scale(fx, fy)
	op.GeoM.Translate(ox, oy)
	switch rot % 4 {
	case 1: // 90° clockwise: (u,v) -> (h-v, u)
		op.GeoM.Rotate(math.Pi / 2)
		op.GeoM.Translate(float64(h), 0)
	case 2: // 180°: (u,v) -> (w-u, h-v)
		op.GeoM.Scale(-1, -1)
		op.GeoM.Translate(float64(w), float64(h))
	case 3: // 90° counter-clockwise: (u,v) -> (v, w-u)
		op.GeoM.Rotate(-math.Pi / 2)
		op.GeoM.Translate(0, float64(w))
	}
	op.GeoM.Scale(zoom, zoom)
	op.GeoM.Translate(offX, offY)
	return op
}

// imageOp is viewMatrix for the current image and view state.
func (v *viewer) imageOp() *ebiten.DrawImageOptions {
	b := v.img.Bounds()
	op := viewMatrix(b.Dx(), b.Dy(), v.rot, v.flipX, v.flipY, v.zoom, v.offX, v.offY)
	op.Filter = v.scaleFilter()
	return op
}

// scaleFilter picks the GPU sampling filter. Linear (the default) keeps text
// and photographs smooth when the image is scaled; nearest is used at exactly
// 1:1, where the centre offset can sit on a half pixel and linear filtering
// would blur every texel, and is available for all zooms with "s" when crisp
// pixels matter more than smooth edges.
func (v *viewer) scaleFilter() ebiten.Filter {
	if v.smooth && v.zoom != 1 {
		return ebiten.FilterLinear
	}
	return ebiten.FilterNearest
}

// colorAltered reports whether the display color transform is doing anything
// (negative on, or brightness off neutral). When false the plain fast draw
// path is used; when true the image goes through a colorm.ColorM instead. A
// non-positive brightness counts as neutral, so a zero-value viewer is safe.
func (v *viewer) colorAltered() bool {
	return v.invert || (v.bright > 0 && v.bright != brightNeutral)
}

// viewColorM builds the display-only color transform: negative flips RGB
// around mid-grey (out = 1-in) and leaves alpha alone, brightness scales RGB.
// The two compose, so with both on the result is brightness applied to the
// negative. Identity when neither is in effect, matching colorAltered.
func (v *viewer) viewColorM() colorm.ColorM {
	var cm colorm.ColorM
	if v.invert {
		// invert = scale(-1,-1,-1,1) then translate(1,1,1,0): out = 1-in, a=a
		cm.Scale(-1, -1, -1, 1)
		cm.Translate(1, 1, 1, 0)
	}
	if v.bright > 0 && v.bright != brightNeutral {
		cm.Scale(v.bright, v.bright, v.bright, 1)
	}
	return cm
}

func (v *viewer) Draw(screen *ebiten.Image) {
	b := screen.Bounds()
	w, h := v.viewSize(b.Dx(), b.Dy())
	v.setSizeStable(w, h)
	if v.img != nil {
		if v.centered {
			v.center()
		}
		v.drawChecker(screen, w, h)
		if v.colorAltered() {
			// invert / brightness route through a color matrix; only the image
			// is transformed, never the checkerboard, status line or overlays
			op := v.imageOp()
			colorm.DrawImage(screen, v.img, v.viewColorM(), &colorm.DrawImageOptions{
				GeoM:   op.GeoM,
				Filter: op.Filter,
			})
		} else {
			screen.DrawImage(v.img, v.imageOp())
		}
	}
	v.drawMeasureRect(screen)
	if v.showHelp {
		v.drawHelp(screen)
	}
	if v.showOpen {
		v.drawOpen(screen)
	}
	if v.showList {
		v.drawList(screen)
	}
	v.drawStatus(screen)
	v.drawMeasureReadout(screen)
	v.drawTooltip(screen)
}

// statusLine reports the top-corner status as (counter, file name): the
// "[1/822][2.3M][1920x1080]" prefix and the base name it labels. The counter
// leads, the file size follows, and the current image's pixel dimensions
// close the group: the size says how big the file is on disk, the WxH says how
// many pixels the picture really has, so the pair fits one glance.
func (v *viewer) statusLine() (prefix, name string) {
	if len(v.files) == 0 || v.idx >= len(v.files) {
		return "", ""
	}
	p := v.files[v.idx]
	// stat once per file, not once per frame: a size cached by path keeps
	// slow filesystems out of the render loop
	if v.statusPath != p || v.statusSize == "" {
		v.statusPath, v.statusSize = p, fmtSize(p)
	}
	if v.img == nil {
		return "", ""
	}
	name = filepath.Base(p)
	if v.isPDF && len(v.frames) > 0 {
		name = fmt.Sprintf("%s (p%d/%d)", name, v.frameIdx+1, len(v.frames))
	}
	b := v.img.Bounds()
	return statusPrefix(v.idx, len(v.files), v.statusSize, b.Dx(), b.Dy()), name
}

// statusPrefix renders the counter / file-size / resolution group shown at the
// top of the window: "[1/822][2.3M][1920x1080]". The counter is the position in
// the list, the size the file on disk, and the WxH the picture itself — the
// pixel count is what the eye cares about, the disk size is just context.
func statusPrefix(idx, total int, size string, w, h int) string {
	return fmt.Sprintf("[%d/%d][%s][%dx%d]", idx+1, total, size, w, h)
}

// fmtSize is the file size in the units the status line shows (M for
// megabytes and up, K and B below that).
func fmtSize(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return "?"
	}
	n := fi.Size()
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// drawStatus paints "[1/822][2.3M][1920x1080] name.png" on a translucent, slightly
// rounded panel at the top centre of the window, on top of everything
// including the help overlay, so the current file is identifiable at any time
// instead of only while the help panel is open.
func (v *viewer) drawStatus(screen *ebiten.Image) {
	prefix, name := v.statusLine()
	if prefix == "" {
		return
	}
	w, h := v.viewSize(screen.Bounds().Dx(), screen.Bounds().Dy())
	body := helpBodySize(h)
	// active modes go on the left of the counter; their presence is also
	// what deepens the panel, so one tag string drives both label and style
	tag := ""
	if v.panLock {
		tag += "[cursor mode] "
	}
	if v.slide {
		tag += fmt.Sprintf("[slide mode-%gs] ", v.slideEvery.Seconds())
	}
	key := fmt.Sprintf("%d|%d|%s|%s|%s", body, w, tag, prefix, name)
	if v.statusImg == nil || v.statusKey != key {
		if img := renderStatus(body, tag, prefix, name, w-16); img != nil {
			if v.statusImg != nil {
				v.statusImg.Dispose() // the old panel's texture, not the frame's
			}
			v.statusImg = ebiten.NewImageFromImage(img)
			v.statusKey = key
		}
	}
	if v.statusImg == nil {
		ebitenutil.DebugPrintAt(screen, prefix+" "+name, 8, 8)
		return
	}
	op := &ebiten.DrawImageOptions{}
	b := v.statusImg.Bounds()
	op.GeoM.Translate(float64((w-b.Dx())/2), 8)
	screen.DrawImage(v.statusImg, op)
}

// tooltipDur is how long a tooltip stays up before it disappears on its own.
const tooltipDur = 2 * time.Second

// tooltip shows a short transient message at the bottom centre of the
// window for tooltipDur, replacing any message already on screen. It is a
// general-purpose notification channel, deliberately not tied to any one
// feature: the directory jump reports where it landed through it, and
// future features can call it the same way with any short line of text.
func (v *viewer) tooltip(msg string) {
	v.tipText = msg
	v.tipSwatch = nil
	v.tipUntil = time.Now().Add(tooltipDur)
}

// tooltipSwatch is tooltip with a colour chip in front of the message, so the
// colour picker can show the value it just copied as well as name it.
func (v *viewer) tooltipSwatch(msg string, c color.RGBA) {
	v.tipText = msg
	chip := c
	v.tipSwatch = &chip
	v.tipUntil = time.Now().Add(tooltipDur)
}

// prettyPath shortens an absolute path for display by writing the user's
// home directory as "~": ~/photos/shots instead of /home/me/photos/shots.
// Paths outside the home stay absolute; a sibling merely sharing the prefix
// ("/home/me2") is not swallowed because the match requires the separator.
func prettyPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// fileURI renders an absolute path as a file:// URI, escaping whatever the
// path may contain (spaces, non-ASCII names).
func fileURI(p string) string {
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// setClipboardText puts text on the system clipboard by piping it into
// whichever clipboard helper the platform provides. Ebitengine exposes no
// public clipboard API (only an internal GLFW binding), so this mirrors the
// ffmpeg / swaymsg external-process fallbacks already used elsewhere: Wayland's
// wl-copy first, then the X11 tools, then the per-OS helpers. Each helper that
// could block is given a short backstop timeout so a wedged tool cannot hang
// the viewer.
//
// mime is the advertised type: "" leaves the helper's default (plain text),
// while "text/uri-list" marks the payload as a file URI so browsers, file
// managers and chat apps paste it as a file rather than a bare string. Only
// wl-copy and xclip can select a type; the rest carry plain text regardless.
func setClipboardText(text, mime string) error {
	typed := func(base []string) []string {
		if mime == "" {
			return base
		}
		return append(base, "-t", mime)
	}
	var cmds [][]string
	switch runtime.GOOS {
	case "darwin":
		cmds = [][]string{{"pbcopy"}} // the mac pasteboard picks types itself
	case "windows":
		// clip.exe reads ANSI stdin; UTF-8 paths may mangle, acceptable here
		cmds = [][]string{{"clip.exe"}}
	default: // linux, bsd, ...
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			cmds = append(cmds, typed([]string{"wl-copy"}))
		}
		// -quiet: fork to serve the selection, then let the parent exit
		cmds = append(cmds,
			typed([]string{"xclip", "-selection", "clipboard", "-in", "-quiet"}),
			[]string{"xsel", "--clipboard", "--input"}, // xsel has no type flag
			[]string{"termux-clipboard-set"},
		)
	}
	var lastErr error
	for _, argv := range cmds {
		bin, err := exec.LookPath(argv[0])
		if err != nil {
			lastErr = err
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, bin, argv[1:]...)
		cmd.Stdin = strings.NewReader(text)
		err = cmd.Run()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no clipboard helper found")
	}
	return fmt.Errorf("clipboard: %w", lastErr)
}

// copyToClipboard puts text on the system clipboard and reports the outcome
// through the tooltip, so a copy is never silent.
func (v *viewer) copyToClipboard(kind, text, mime string) {
	if err := setClipboardText(text, mime); err != nil {
		fmt.Fprintln(os.Stderr, err)
		v.tooltip(kind + " copy failed")
		return
	}
	v.tooltip(kind + ": " + text)
}

// pickColor copies the image pixel under the cursor to the clipboard as a hex
// colour (#RRGGBB) and announces it in the tooltip with a colour chip. The
// value is read from the source image, so the black letterbox and the
// transparency checkerboard around it can never be picked, and invert /
// brightness — which are display-only — do not change it either. ebiten keeps
// pixels alpha-premultiplied, so a translucent pixel is un-premultiplied back
// to its own colour.
func (v *viewer) pickColor() {
	if v.img == nil {
		return
	}
	gm := v.imageOp().GeoM
	if !gm.IsInvertible() {
		return
	}
	gm.Invert()
	cx, cy := ebiten.CursorPosition()
	px, py := gm.Apply(float64(cx), float64(cy))
	b := v.img.Bounds()
	x, y := b.Min.X+int(math.Floor(px)), b.Min.Y+int(math.Floor(py))
	if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
		v.tooltip("color: cursor is not over the image")
		return
	}
	c := v.img.At(x, y).(color.RGBA) // premultiplied, as ebiten stores it
	r, g, bl := straight(c.R, c.A), straight(c.G, c.A), straight(c.B, c.A)
	hex := fmt.Sprintf("#%02X%02X%02X", r, g, bl)
	if err := setClipboardText(hex, ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		v.tooltip("color copy failed")
		return
	}
	msg := hex
	if c.A != 255 {
		msg = fmt.Sprintf("%s  alpha %d", hex, c.A)
	}
	v.tooltipSwatch(msg, color.RGBA{R: r, G: g, B: bl, A: 255})
}

// straight un-premultiplies one 8-bit channel: ebiten's At reports
// premultiplied alpha, and a colour value should not darken with transparency.
func straight(v, a uint8) uint8 {
	if a == 0 {
		return 0
	}
	n := int(v) * 255 / int(a)
	if n > 255 {
		return 255
	}
	return uint8(n)
}

// drawTooltip paints the pending tooltip, if any, above everything else at
// the bottom centre of the window. The panel is cached by (size, text) like
// the status line and re-trimmed when the window width changes.
func (v *viewer) drawTooltip(screen *ebiten.Image) {
	if v.tipText == "" {
		return
	}
	if time.Now().After(v.tipUntil) {
		v.tipText = ""
		return
	}
	w, h := v.viewSize(screen.Bounds().Dx(), screen.Bounds().Dy())
	key := fmt.Sprintf("%d|%d|%s", helpBodySize(h), w, v.tipText)
	if v.tipSwatch != nil {
		key += fmt.Sprintf("|%02X%02X%02X%02X", v.tipSwatch.R, v.tipSwatch.G, v.tipSwatch.B, v.tipSwatch.A)
	}
	if v.tipImg == nil || v.tipKey != key {
		if img := renderTooltip(helpBodySize(h), v.tipText, w-16, v.tipSwatch); img != nil {
			if v.tipImg != nil {
				v.tipImg.Dispose() // the old panel's texture, not the frame's
			}
			v.tipImg = ebiten.NewImageFromImage(img)
			v.tipKey = key
		}
	}
	if v.tipImg == nil {
		ebitenutil.DebugPrintAt(screen, v.tipText, 8, h-16) // no system font: last resort
		return
	}
	op := &ebiten.DrawImageOptions{}
	b := v.tipImg.Bounds()
	op.GeoM.Translate(float64((w-b.Dx())/2), float64(h-b.Dy()-8))
	screen.DrawImage(v.tipImg, op)
}

// drawHelp paints a translucent dimmer over the whole screen plus a panel
// listing the shortcuts. The dimmer is cached and rebuilt only on resize.
func (v *viewer) drawHelp(screen *ebiten.Image) {
	// same visible-size rule as the image: if the canvas is still stale from a
	// fullscreen transition, the window only shows its top-left corner, so
	// centre the panel in what is actually visible.
	w, h := v.viewSize(screen.Bounds().Dx(), screen.Bounds().Dy())
	if v.dim == nil || v.dim.Bounds().Dx() != w || v.dim.Bounds().Dy() != h {
		v.dim = ebiten.NewImage(w, h)
	}
	v.dim.Fill(color.RGBA{8, 8, 10, 185})
	screen.DrawImage(v.dim, nil)

	body := helpBodySize(h)
	// the key covers the scroll offset and the window height too: either
	// changing means different rows are on screen, so the panel is rebuilt
	key := fmt.Sprint(body, ":", h, ":", v.helpScroll)
	if v.helpImg == nil || v.helpKey != key {
		if img := buildHelp(body, v.helpScroll, h); img != nil {
			if v.helpImg != nil {
				v.helpImg.Dispose()
			}
			v.helpImg, v.helpKey = img, key
		}
	}
	if v.helpImg == nil {
		// font plumbing failed: say something with the built-in debug font
		ebitenutil.DebugPrintAt(screen, "imgo - press ? or esc to close", w/2-70, h/2)
		return
	}
	b := v.helpImg.Bounds()
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64((w-b.Dx())/2), float64((h-b.Dy())/2))
	screen.DrawImage(v.helpImg, op)
}

// drawOpen paints the external-program launcher: a dimmed backdrop and the
// panel of matching programs, centred. It mirrors drawHelp's caching so the
// panel is only re-rendered when the filter or the selection changes.
func (v *viewer) drawOpen(screen *ebiten.Image) {
	w, h := v.viewSize(screen.Bounds().Dx(), screen.Bounds().Dy())
	if v.dim == nil || v.dim.Bounds().Dx() != w || v.dim.Bounds().Dy() != h {
		v.dim = ebiten.NewImage(w, h)
	}
	v.dim.Fill(color.RGBA{8, 8, 10, 185})
	screen.DrawImage(v.dim, nil)

	matches := v.openMatches()
	names := make([]string, len(matches))
	for i, p := range matches {
		names[i] = p.label
	}
	sel := v.openIdx
	if sel >= len(names) {
		sel = len(names) - 1
	}
	body := helpBodySize(h)
	key := fmt.Sprintf("%d|%d|%d|%s|%s", body, h, sel, v.openFilter, strings.Join(names, "\x00"))
	if v.openImg == nil || v.openKey != key {
		if img := buildOpenMenu(body, names, sel, v.openFilter, w-16); img != nil {
			if v.openImg != nil {
				v.openImg.Dispose()
			}
			v.openImg, v.openKey = img, key
		}
	}
	if v.openImg == nil {
		ebitenutil.DebugPrintAt(screen, "imgo - open in external program", w/2-90, h/2)
		return
	}
	b := v.openImg.Bounds()
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64((w-b.Dx())/2), float64((h-b.Dy())/2))
	screen.DrawImage(v.openImg, op)
}

// drawList paints the directory file list: a dimmed backdrop and the panel of
// matching images, the selected row highlighted and the image now on screen
// marked with a dot. Cached like the other panels, so it is only rebuilt when
// the filter, the selection, the scroll or the window changes.
func (v *viewer) drawList(screen *ebiten.Image) {
	w, h := v.viewSize(screen.Bounds().Dx(), screen.Bounds().Dy())
	if v.dim == nil || v.dim.Bounds().Dx() != w || v.dim.Bounds().Dy() != h {
		v.dim = ebiten.NewImage(w, h)
	}
	v.dim.Fill(color.RGBA{8, 8, 10, 185})
	screen.DrawImage(v.dim, nil)

	matches := v.listMatches()
	names := make([]string, len(matches))
	cur := -1
	for r, fi := range matches {
		names[r] = filepath.Base(v.files[fi])
		if fi == v.idx {
			cur = r
		}
	}
	sel := v.listSel
	if sel >= len(names) && len(names) > 0 {
		sel = len(names) - 1
	}
	dir := ""
	if len(v.files) > 0 {
		dir = filepath.Base(filepath.Dir(v.files[v.idx]))
	}
	body := helpBodySize(h)
	key := fmt.Sprintf("%d|%d|%d|%d|%d|%v|%s|%s|%s",
		body, w, sel, v.listTop, cur, v.listInput, v.listFilter, dir, strings.Join(names, "\x00"))
	if v.listImg == nil || v.listKey != key {
		if img := buildListMenu(body, names, sel, v.listTop, cur, v.listInput, dir, v.listFilter, w-16, h); img != nil {
			if v.listImg != nil {
				v.listImg.Dispose()
			}
			v.listImg, v.listKey = img, key
		}
	}
	if v.listImg == nil {
		ebitenutil.DebugPrintAt(screen, "imgo - directory file list", w/2-90, h/2)
		return
	}
	b := v.listImg.Bounds()
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64((w-b.Dx())/2), float64((h-b.Dy())/2))
	screen.DrawImage(v.listImg, op)
}

// Layout reports the canvas size; keeping it equal to the window size means
// image pixels and screen pixels are 1:1 at zoom 1.
func (v *viewer) Layout(outsideWidth, outsideHeight int) (int, int) {
	v.setSize(outsideWidth, outsideHeight)
	return outsideWidth, outsideHeight
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "imgo:", err)
	os.Exit(1)
}
