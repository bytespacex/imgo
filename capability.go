// Terminal capability probing: Sixel support (DA1) and cell pixel size.
//
// Traps (verified): fzf/lf preview commands run in a background process group;
// POSIX: tcsetattr on /dev/tty there raises SIGTTOU (default action stops the
// process), reading raises SIGTTIN (read returns EIO). So:
//  1. main must signal.Ignore(SIGTTOU, SIGTTIN) at startup
//  2. never touch the tty when -f sixel/symbols is explicit (fzf owns /dev/tty
//     for keyboard input and would consume our probe replies as keystrokes)
//  3. cell size uses TIOCGWINSZ (ioctl) before any escape query: an ioctl
//     neither writes bytes the terminal could echo nor reads input, so it is
//     safe even while fzf holds /dev/tty for the keyboard.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

type caps struct {
	Sixel    bool // sixel support (only meaningful if SixKnown)
	SixKnown bool // sixel was actually probed (cache stores "x" when unknown)
	CellW    int
	CellH    int
}

// hasSixel reports Sixel support. Order: env override -> cache -> live probe (foreground only).
func hasSixel() bool {
	if v, ok := envBool("IMGO_SIXEL"); ok {
		return v
	}
	if c, ok := loadCache(); ok && c.SixKnown {
		return c.Sixel
	}
	resp, ok := query("\x1b[c", func(b []byte) bool { return strings.ContainsRune(string(b), 'c') })
	if !ok {
		return false
	}
	s := string(resp)
	i := strings.Index(s, "?")
	if i < 0 {
		return false
	}
	for _, p := range strings.Split(s[i+1:len(s)-1], ";") {
		if strings.TrimSpace(p) == "4" {
			updateCache(func(c *caps) { c.Sixel, c.SixKnown = true, true })
			return true
		}
	}
	updateCache(func(c *caps) { c.Sixel, c.SixKnown = false, true })
	return false
}

// cellSize returns the cell (width, height) in pixels.
//
// Order: IMGO_CELL env -> TIOCGWINSZ ioctl -> warmup cache -> live CSI 16t.
// The cell size is machine-specific (terminal + font family + size + DPI):
// 12x29 is one user's foot at one font size, anyone else gets another value.
// The ioctl comes before the cache because it is free, needs no probe and
// always reflects the current font size (the cache key cannot see that).
func cellSize() (w, h int) {
	if c := os.Getenv("IMGO_CELL"); c != "" {
		if a, b, ok := strings.Cut(c, "x"); ok {
			if w, err1 := strconv.Atoi(a); err1 == nil {
				if h, err2 := strconv.Atoi(b); err2 == nil && w > 0 && h > 0 {
					return w, h
				}
			}
		}
	}
	if w, h, ok := winsizeCell(); ok {
		return w, h
	}
	if c, ok := loadCache(); ok && c.CellW > 0 && c.CellH > 0 {
		return c.CellW, c.CellH
	}
	resp, ok := query("\x1b[16t", func(b []byte) bool { return strings.ContainsRune(string(b), 't') })
	if !ok {
		return 8, 16
	}
	s := string(resp)
	i := strings.Index(s, "[6;")
	if i < 0 {
		return 8, 16
	}
	parts := strings.Split(strings.TrimSuffix(s[i+3:], "t"), ";")
	if len(parts) != 2 {
		return 8, 16
	}
	hh, err1 := strconv.Atoi(parts[0])
	ww, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || ww <= 0 || hh <= 0 {
		return 8, 16
	}
	updateCache(func(c *caps) { c.CellW, c.CellH = ww, hh })
	return ww, hh
}

// probeCaps probes live and writes the cache. Run once in a foreground shell:
//
//	imgc -warmup
func probeCaps() {
	sixel := false
	if resp, ok := query("\x1b[c", func(b []byte) bool { return strings.ContainsRune(string(b), 'c') }); ok {
		s := string(resp)
		if i := strings.Index(s, "?"); i >= 0 {
			for _, p := range strings.Split(s[i+1:len(s)-1], ";") {
				if strings.TrimSpace(p) == "4" {
					sixel = true
				}
			}
		}
	}
	cw, ch := 0, 0
	if resp, ok := query("\x1b[16t", func(b []byte) bool { return strings.ContainsRune(string(b), 't') }); ok {
		s := string(resp)
		if i := strings.Index(s, "[6;"); i >= 0 {
			parts := strings.Split(strings.TrimSuffix(s[i+3:], "t"), ";")
			if len(parts) == 2 {
				if h, e1 := strconv.Atoi(parts[0]); e1 == nil {
					if w, e2 := strconv.Atoi(parts[1]); e2 == nil && w > 0 && h > 0 {
						cw, ch = w, h
					}
				}
			}
		}
	}
	if cw == 0 || ch == 0 {
		// terminal does not answer CSI 16t: fall back to the tty's pixel size
		if w, h, ok := winsizeCell(); ok {
			cw, ch = w, h
		}
	}
	if cw == 0 || ch == 0 {
		cw, ch = 8, 16
	}
	saveCache(caps{Sixel: sixel, SixKnown: true, CellW: cw, CellH: ch})
}

func ttyAvailable() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	tty.Close()
	return true
}

func envBool(name string) (bool, bool) {
	v := os.Getenv(name)
	if v == "" {
		return false, false
	}
	return v == "1" || strings.EqualFold(v, "true"), true
}

func cachePath() string {
	id := os.Getenv("TERM") + "|" + os.Getenv("TMUX") + "|" + os.Getenv("TERM_PROGRAM")
	sum := sha256.Sum256([]byte(id))
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "imgo", "cap-"+hex.EncodeToString(sum[:8])+".json")
}

func loadCache() (caps, bool) {
	var c caps
	b, err := os.ReadFile(cachePath())
	if err != nil {
		return c, false
	}
	f := strings.Fields(string(b))
	if len(f) != 3 {
		return c, false
	}
	// "1"/"0" = probed result, "x" (or legacy "0") = never probed
	if f[0] == "1" {
		c.Sixel, c.SixKnown = true, true
	} else if f[0] == "0" {
		c.SixKnown = false // legacy file: treat as unknown, re-probe
	} else {
		c.SixKnown = false
	}
	c.CellW, _ = strconv.Atoi(f[1])
	c.CellH, _ = strconv.Atoi(f[2])
	return c, true
}

func saveCache(c caps) {
	p := cachePath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	sx := "x"
	if c.SixKnown {
		sx = "0"
		if c.Sixel {
			sx = "1"
		}
	}
	os.WriteFile(p, []byte(sx+" "+strconv.Itoa(c.CellW)+" "+strconv.Itoa(c.CellH)+"\n"), 0o644)
}

// updateCache applies fn to the current cache (or a fresh entry) and saves it,
// so a successful foreground probe persists for later background previews.
func updateCache(fn func(*caps)) {
	c, _ := loadCache()
	if c.CellW == 0 {
		c.CellW, c.CellH = 8, 16
	}
	fn(&c)
	saveCache(c)
}

// query sends an escape sequence to /dev/tty and waits for the reply, 200ms timeout.
func query(seq string, done func([]byte) bool) ([]byte, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, false
	}
	defer tty.Close()

	fd := int(tty.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false
	}
	defer term.Restore(fd, old)

	if _, err := tty.WriteString(seq); err != nil {
		return nil, false
	}

	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		buf := make([]byte, 256)
		var acc []byte
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				if done(acc) {
					ch <- res{acc, nil}
					return
				}
			}
			if err != nil {
				ch <- res{acc, err}
				return
			}
		}
	}()

	select {
	case r := <-ch:
		return r.b, r.err == nil && len(r.b) > 0
	case <-time.After(200 * time.Millisecond):
		return nil, false
	}
}
