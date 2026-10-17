# imgo

> A pure-Go image toolkit: `imgc`, a terminal Sixel previewer, and `imgo`, a GPU-accelerated image viewer built on Ebitengine.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8.svg?logo=go)](https://go.dev/)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey.svg)](#build--install)

One codebase builds two binaries, covering everything from terminal previews to a desktop viewer:

- **imgc (CLI)** — a single ~4 MB binary, zero C dependencies, cross-compiles to Linux / macOS / Windows. Emits native Sixel, falling back to Unicode half-blocks when the terminal does not support it.
- **imgo (GUI)** — built on Ebitengine, ~10.5 MB, GPU-accelerated. Wayland / X11 on Linux, Metal and Retina on macOS.

<!-- Suggested: a demo GIF here (imgc in the terminal + the imgo window) -->

## Features

### imgc — terminal previewer

- **Native Sixel** — foot and tmux 3.8+ parse it directly; no `allow-passthrough` needed.
- **Automatic cell sizing** — scales to the terminal's real character-cell pixels and keeps the aspect ratio. Probe chain: `IMGO_CELL` → `TIOCGWINSZ` → cache → `CSI 16t` (see [Cell pixel resolution](#cell-pixel-resolution)); no manual pixel values.
- **Pure-Go decoding** — png / jpeg / gif / webp / tiff / bmp / ico / svg.
- **External fallback** — avif / heic / heif / jxl are piped through ffmpeg with a 5 s timeout.
- **Graceful degradation** — terminals without Sixel get Unicode half-blocks; an undecodable file exits with code `1` so callers can fall back to `bat` / `cat`.

### imgo — GUI viewer

- **Formats** — the same pure-Go decoding and ffmpeg fallback as the CLI. GIF / APNG / Animated WebP play frame by frame honouring each format's own timing, dispose and blend rules; when the frames would exceed a 256 MB budget, playback falls back to the first frame.
- **View** — fit-to-window by default (small images upscale, large ones shrink, aspect kept); `Space` toggles fit ↔ original; `0` is 1:1; `+` / `-` zoom around the image centre (`Shift` variants and the wheel zoom around the cursor); drag pans; `r` / `t` rotate 90°; `w` / `e` mirror. Rotation and mirrors reset when you change image.
- **Display adjustments** — `i` inverts (negative) and `<` / `>` scale brightness 0.1–3.0×. Both are display-only (a GPU colour matrix); disk pixels are untouched and both reset on image change.
- **Transparency** — images with alpha are drawn over the classic light checkerboard (`#FFFFFF` / `#CCCCCC`) instead of black, so shadows and glows read correctly; the letterbox stays black.
- **Cursor-locked panning** — a single click enters 1:1 and grabs the cursor so the image follows the mouse even at the screen edge; click again or press `ESC` / `q` to leave.
- **Directory browsing** — pass a directory to start at the first image; `←` / `→` walk the directory.
- **Sibling-directory jumps** — `PgUp` / `PgDn` jump between sibling directories that contain images, landing on the first image. The set is scanned once and cached; each landing re-reads the target directory, so external changes show up immediately. Every jump (including a no-op at an endpoint) shows a centred `[3/12] ~/photos/shots` tooltip for 2 s.
- **Trash** — `d` moves the image to the system trash (freedesktop `~/.local/share/Trash` with a `.trashinfo` record on Linux / BSD, `~/.Trash` on macOS, the Recycle Bin on Windows), then advances; deleting the last image closes the window. Cross-device moves fall back to copy + unlink.
- **Marking** — `m` / `M` copy / move the image into an `imgo/` subdirectory next to it (created on demand), auto-numbering collisions with `-1`, `-2`; `M` advances like delete. Cross-device moves copy.
- **External programs** — `o` opens a launcher for the programs in `IMGO_OPEN` (comma-separated, each may carry arguments, `~` and `$VAR` expand, e.g. `"gimp.appimage,foot lf"`). Home is displayed as `~`; type a letter to locate (path segment / argument matching), a unique match launches at once, `Enter` opens the selection, `Esc` / `q` cancels. Programs start detached with the image's directory as the working directory.
- **File list** — `/` opens the current directory's images (images only), the current one dotted and the selection highlighted. `/` again focuses the filter box, so a double press opens the list and starts searching. `j`/`k` or the arrows move, `PgUp`/`PgDn` and `Ctrl+B`/`Ctrl+F` page, `Enter` switches image, `Esc`/`q` closes. While the filter box is focused every key (including `j`/`k`) types into a live file-name substring filter; `Enter`/`Esc` return to navigation — navigation always wins.
- **Slideshow** — `\` toggles automatic advance (2 s default, `[` / `]` step 0.5 s over 0.5–10 s); the status bar shows an amber `[slide mode-2.5s]` and darkens. It pauses while the help panel is open. While playing, `ESC` / `q` stops the slideshow first; a second press quits.
- **Animation stepping** — `p` pauses / resumes, `,` / `.` step one frame back / forward (pausing first, so a single press lands on the neighbouring frame); resuming continues from the current frame.
- **Clipboard** — `Ctrl+P` copies the absolute path, `Ctrl+U` copies a `file://` URI with spaces and non-ASCII escaped. Uses system helpers (Wayland `wl-copy`, X11 `xclip`, falling back to `xsel` / `pbcopy` / `clip.exe`), no CGO; URIs are broadcast as `text/uri-list`, so browsers, file managers and chat apps paste a file rather than text. A tooltip echoes the result.
- **Colour picking** — `c` copies the colour under the cursor as `#RRGGBB`, with the swatch and value in a tooltip (alpha included when the pixel is translucent). It samples the original image pixel, so the letterbox and the transparency checkerboard are never picked and display-only tweaks do not affect it. Disabled in cursor-lock and slideshow mode.
- **Measure** — `v` enters measure mode: drag a rectangle (a 50 % black mask, snapped to whole source pixels and clamped to the image); **on mouse release** the reading `image 100x100   screen 150x150   (150%)` is copied to the clipboard and shown as a tooltip, then measure mode exits and the box clears. `image` is the exact source-pixel size (independent of display scaling), `screen` is the on-screen pixels and the parenthesised value is the zoom. `Esc` / `q` exits. Unavailable in cursor-lock and slideshow mode.
- **Status bar** — a persistent rounded, translucent `[1/822][2.3M] name.png` (index / size / name) centred at the top; CJK names render fine. While cursor-lock or slideshow is active the background darkens and an amber `[cursor mode]` / `[slide mode-2.5s]` shows on the left (side by side if both).
- **Help panel** — `?` toggles it, rendered with system fonts. While it is open, `ESC` / `q` only close the panel (the layered quit order is cursor-lock → slideshow → program). The panel never exceeds the window; a long list scrolls with `↑` / `↓` (`j` / `k`, `PgUp` / `PgDn`, wheel) and shows a scrollbar on the right.

> **Fonts** — the help panel and status bar use system fonts at runtime: `fc-match sans` for the default, `fc-match ':charset=4e00'` to add CJK glyphs, then a list of common paths (DejaVu / Liberation / Noto), and finally Go's built-in Go Regular. `.ttc` collections such as Noto are parsed for their first face. The size follows the window height, CJK names render as real glyphs, and no configuration is needed.

## Key bindings (imgo)

The same list lives inside the `?` help panel.

| Key | Action |
|---|---|
| `Space` | Original ↔ fit-to-window |
| `0` | Original 1:1 |
| `+` / `-` | Zoom around the **image centre**; `Shift`+`+` / `Shift`+`-` zoom around the cursor (window centre in cursor-lock mode) |
| Wheel | Zoom around the cursor (window centre in cursor-lock mode) |
| Drag | Pan |
| Click (press → release < 4 px) | Enter 1:1 + cursor-locked panning; click again or `ESC` / `q` to leave |
| `r` / `t` | Rotate 90° clockwise / counter-clockwise (re-fits in fit mode) |
| `w` / `e` | Mirror horizontally / flip vertically |
| `i` | Negative (invert); display-only |
| `<` / `>` | Brightness − / + (`Shift+,` / `Shift+.`; 0.1–3.0×, step 0.1, echoed in a tooltip) |
| `←` / `→` | Previous / next image in the directory |
| `k` / `j` | Previous / next image (vim keys, same as `←` / `→`) |
| `PgUp` / `PgDn` | Previous / next sibling directory with images (lands on the first image; a 2 s tooltip follows each jump) |
| `\` | Slideshow toggle (2 s default; the status bar shows `[slide mode-2.5s]` and darkens) |
| `[` / `]` | Slideshow interval − / + 0.5 s (0.5–10 s, while the slideshow runs) |
| `p` | Pause / resume animation playback |
| `,` / `.` | Step one animation frame back / forward (pauses first, wraps at the ends) |
| `d` | Move the current image to the trash (advances; deleting the last image closes the window) |
| `m` / `M` | Copy / move the image into the `imgo/` subdirectory beside it (collisions get `-1`, `-2`; `M` advances like delete; the tooltip reads `copied to imgo/<name>`, or `moved to …` for `M`) |
| `o` | Open with an external program (`IMGO_OPEN` list; type to locate, a unique match launches, `Enter` opens, `Esc` / `q` cancels) |
| `/` | Directory file list (a second `/` focuses the filter box, so a double press opens and searches; `j`/`k`/arrows move, `PgUp`/`PgDn`, `Ctrl+B`/`Ctrl+F` page, `Enter` switches, `Esc`/`q` closes) |
| `c` | Copy the image pixel under the cursor as hex (`#RRGGBB`); a tooltip shows the swatch and value; disabled in cursor-lock / slideshow |
| `v` | Measure: drag a rectangle (50 % black mask); **on release** the reading `image 100x100   screen 150x150   (150%)` is copied and shown, then measure mode exits and the box clears (`image` = exact source pixels, `screen` = on-screen pixels, parentheses = zoom); `Esc`/`q` exits; disabled in cursor-lock / slideshow |
| `Ctrl+P` / `Ctrl+U` | Copy the absolute path / `file://` URI to the clipboard (the URI is broadcast as `text/uri-list`; the tooltip echoes it) |
| `f` | Fullscreen (on Sway this is delegated to the compositor, see [Fullscreen under Wayland / Sway](#fullscreen-under-wayland--sway)) |
| `?` | Toggle the help panel |
| `q` / `ESC` | Quit (in order: leave cursor lock → stop the slideshow → quit) |

## Build & install

### Requirements

- Go 1.26.4+ (see `go.mod`).
- CLI: pure Go, no extra dependencies, cross-compiles to any target.
- GUI: Linux / macOS need cgo and the system graphics / audio dev libraries (Debian / Ubuntu: `libgl1-mesa-dev libxcursor-dev libxi-dev libxinerama-dev libxrandr-dev libxxf86vm-dev libasound2-dev pkg-config`; the full list is in the [Ebitengine docs](https://ebitengine.org/en/getting_started.html)). Windows needs no cgo and cross-compiles from any host.

### Compile

```sh
# 1. CLI previewer (pure Go, zero C dependencies, cross-compiles anywhere)
CGO_ENABLED=0 go build -ldflags="-s -w" -o imgc .

# 2. GUI viewer (build on the target platform; Linux / macOS need cgo + graphics libs)
go build -tags gui -ldflags="-s -w" -o imgo .

# 3. Cross-compile the CLI (any OS / arch)
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o imgc-darwin_arm64 .
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o imgc-linux_arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o imgc-windows_amd64.exe .

# 4. Cross-compile the GUI to Windows (Ebitengine uses purego there, no cgo)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags gui \
    -ldflags="-s -w -H=windowsgui" -o imgo-windows_amd64.exe .
#    The macOS GUI needs cgo, so it must be built on macOS (or via osxcross).

# 5. One-shot build (recommended): cross-compile every supported target into
#    ./dist (imgc for six platforms, imgo for Windows + the host), then offer to
#    move this platform's imgc + imgo into ~/bn (default yes) and, on Linux, to
#    register imgo.desktop (default no). POSIX sh on Linux / macOS — use
#    setup.cmd on Windows.
./setup.sh
```

> The `./setup.sh` desktop entry and MIME associations are Linux-only; Windows /
> macOS packaging (`.exe` icons, a macOS `.app` bundle / Info.plist) has to be
> done separately — Windows users can start from `setup.cmd`.

**Build tags** — `imgo.go` / `font.go` / `anim.go` / `measure.go` and their tests carry `//go:build gui` and are compiled only with `-tags gui`; `imgo_stub.go` stands in the opposite way so the CLI build never pulls in Ebitengine. The two binaries share the same `main`: the GUI build opens a window by default, the CLI build previews in the terminal. A few platform-specific files are split by OS (`*_unix.go` / `*_windows.go` / `*_darwin.go` and the like) and selected by build tag.

**Version stamping** — `imgc -version` / `imgo -version` print the `main.buildVersion` variable, injected with `-ldflags="-X main.buildVersion=<ver>"`; the printed program name comes from `main.appName` (`imgc` for the CLI, `imgo` for the GUI build, set the same way). A plain `go build` keeps the `0.1.0` default, while `setup.sh` / `setup.cmd` and the release workflow inject the build date automatically (`yy.mm.dd`, e.g. `26.10.18`), so a tagged release reports `imgo 26.10.18`.

```sh
# stamp a version by hand
CGO_ENABLED=0 go build -ldflags="-s -w -X main.buildVersion=26.10.18" -o imgc .
go build -tags gui -ldflags="-s -w -X main.buildVersion=26.10.18 -X main.appName=imgo" -o imgo .
```

### Releases

Pushing a `v*` tag triggers [`.github/workflows/release.yml`](.github/workflows/release.yml): on Ubuntu it runs `gofmt` / `go vet` / cross-compiles imgc (six targets) + imgo (Linux, Windows) with the GUI tests under Xvfb; on macOS it builds imgo (arm64 / amd64). It then creates a Release with every artifact plus `SHA256SUMS.txt`. Every binary is stamped with the build date as its version (`yy.mm.dd`, e.g. `26.10.18`).

```sh
git tag v0.1.0
git push origin v0.1.0
```

You can also run `workflow_dispatch` from the Actions tab to build and keep the artifacts without publishing a Release.

## Usage

### Examples

```sh
# Terminal preview (imgc)
imgc -W 106 -H 47 image.png                    # set the preview area in columns / rows
imgc -f sixel -W $FZF_PREVIEW_COLUMNS -H $FZF_PREVIEW_LINES img.jpg
imgc -f symbols -W 80 -H 24 image.png          # force half-block mode
imgc -warmup                                   # probe terminal capabilities in the foreground and cache them
imgc -version                                  # print the version

# Desktop viewing (imgo)
imgo image.png        # open a window (fit to window); ←→ browse, \ slideshow, d delete, click to grab-pan, ? help
imgo meme.gif         # play an animation (GIF / APNG / Animated WebP)
imgo ~/Pictures/      # open a directory at the first image; ←→ walk the whole directory
```

### CLI flags & environment

| Flag / env | Default | Description |
|---|---|---|
| `-f` | `auto` | Output mode: `auto` (detect), `sixel`, `symbols` |
| `-W` `-H` | — | Preview width / height in cells; when both are given the image is sized to the grid |
| `-colors` | `256` | Sixel palette size (2–256) |
| `-dither` | `true` | Floyd–Steinberg dithering; `-dither=false` trades quality for speed |
| `-cw` `-ch` | auto | Set the cell pixel size explicitly (highest priority) |
| `-probe` | `true` | Probe terminal capabilities; `-probe=false` skips live queries such as DA1 |
| `-warmup` | — | Probe in the foreground, write `~/.cache/imgo/cap-*.json`, exit |
| `-version` | — | Print the version |
| `IMGO_SIXEL=1/0` | — | Force Sixel support on / off, skipping the DA1 probe |
| `IMGO_CELL=WxH` | — | Fix the cell pixel size and skip all probing (last resort; normally unnecessary) |
| `IMGO_OPEN=...` | — | GUI: the `o` launcher's program list, comma-separated; each item may carry arguments and the image path is appended; `~` and `$VAR` expand (e.g. `"gimp.appimage,foot lf,~/pg/gimp/AppRun"`) |
| `IMGO_DEBUG=1` | — | GUI debug output: per-frame size / zoom / offset on stderr |

## Workflow integration

### lf

`~/.config/lf/previewer`:

```bash
#!/usr/bin/env bash
f="$1" w="$2" h="$3" x="$4" y="$5"
cache() { echo "$HOME/.local/share/lf/$(printf %s "$f" | md5sum | cut -d' ' -f1)"; }

p_img() { imgc -f sixel -W "$w" -H "$h" "$1"; exit 0; }

p_txt() { bat --color=always --style=numbers --pager=never "$@" "$f" 2>/dev/null || cat "$f"; }
case "${f##*.}" in
    pdf)  c="$(cache).jpg"; [ ! -f "$c" ] && pdftoppm -jpeg -f 1 -singlefile "$f" "$(cache)"; p_img "$c" ;;
    epub) c="$(cache)"; [ ! -f "$c" ] && epub-thumbnailer "$f" "$c" 1024; p_img "$c" ;;
    avi|mp4|wmv|ts|dat|3gp|ogv|mpg|mpeg|vob|fl*|m2v|mov|webm|mts|m4v|ra|rm*|qt|divx)
        c="$(cache).jpg"; [ ! -f "$c" ] && ffmpegthumbnailer -i "$f" -o "$c" -s 0 -q 5; p_img "$c" ;;
    bmp|jpg|jpeg|png|xpm|webp|gif|jfif) p_img "$f" ;;
    ino) p_txt --language=cpp ;;
    *) p_txt ;;
esac
```

### fzf

```sh
fd -t f -e jpg -e png -e webp -e gif -e avif -e svg | \
  fzf --preview="imgc -f sixel -W \$FZF_PREVIEW_COLUMNS -H \$FZF_PREVIEW_LINES {}" \
      --preview-window="bottom:60%"
```

## Technical notes

### Cell pixel resolution

Sixel draws in physical pixels, so to fill `-W × H` cells exactly `imgc` must know how many pixels a cell is — and that depends on the terminal, font, size and DPI. It resolves the value in this order:

| Priority | Source | Notes |
|---|---|---|
| 1 | `-cw` / `-ch` | Command-line flags, highest priority |
| 2 | `IMGO_CELL=WxH` | Environment variable; fixes the value and skips all probing |
| 3 | `TIOCGWINSZ` ioctl | Reads `ws_xpixel` / `ws_ypixel` from `/dev/tty` and divides. It sends no escape sequence and reads no input, so it is safe inside an fzf / lf preview; foot / kitty / wezterm / konsole / tmux 3.x all fill the field |
| 4 | `~/.cache/imgo/cap-*.json` | Cache written by `imgc -warmup` |
| 5 | `CSI 16t` | Live query to the foreground terminal (the only step that reads tty input) |
| 6 | `8x16` | Fallback when everything else fails (matches chafa's default) |

So foot / kitty / tmux users are zero-config; only terminals that neither fill `ws_xpixel` nor answer `CSI 16t` need a manual value. Do not hard-code pixel values in integration scripts — after changing the font size, re-measure like so:

```sh
imgc -cw 12 -ch 29 -W 106 -H 47 img.png   # try candidate values
```

### fzf / lf background previews: signals and input races

- **Terminal I/O in a background process group** — POSIX raises `SIGTTOU` when a background process calls `tcsetattr` on `/dev/tty` (default action: stop the process — the classic "fzf spins forever"), and `SIGTTIN` when it reads the tty (`read` returns EIO). `imgc` ignores both at startup. (`TIOCGWINSZ` is an ioctl, not a read or write, so cell-size probing stays safe in a preview process.)
- **Input races** — fzf itself reads the keyboard from `/dev/tty`, so replies to `DA1` / `CSI 16t` can be consumed by fzf as keystrokes (garbage like `?62;4;...c` in its query box). Preview scripts **must pass `-f sixel`** to skip live probing. Cell sizing is unaffected: it goes through the ioctl, which writes no bytes and reads no input.

### tmux and Sixel passthrough

tmux 3.8+ parses Sixel natively — do **not** wrap it in a `\x1bPtmux;` passthrough (most tmux setups still have `allow-passthrough` off).

### Fullscreen under Wayland / Sway

Ebitengine's built-in fullscreen is unreliable under Wayland / Sway: after toggling, its internal size state can go stale (in testing `WindowSize()` still reported 2560×1440 while the window had returned to 1276×698 once), pushing the image off-screen. Therefore:

- with `SWAYSOCK` set, `f` runs `swaymsg fullscreen toggle` instead, leaving fullscreen entirely to the compositor — the configure events Ebitengine receives are always correct;
- elsewhere Ebitengine fullscreen is used, restoring the original window size on exit and debouncing size changes over two frames (only two consecutive identical frames are accepted).

Set `IMGO_DEBUG=1` to print per-frame size / zoom / offset when chasing resize bugs.

## Project layout

A single `main` package with flat source files; GUI code is gated by build tags. There is also one tiny `x11quiet` subpackage (compiled for GUI builds only):

```
imgc.go           CLI entry: flag parsing, size conversion, mode selection
capability.go      Terminal capabilities: cell size / DA1 Sixel probe, cached to disk
platform_unix.go      //go:build !windows — ignore SIGTTOU/SIGTTIN; cell size from the tty's ws_xpixel
platform_windows.go   //go:build windows  — no-op counterpart; cell size via GetCurrentConsoleFontEx
decode.go          Per-format decoders + ffmpeg fallback (shared by CLI / GUI)
render.go          CatmullRom scaling, go-sixel encoding, half-block fallback
imgo.go            //go:build gui    — the Ebitengine viewer (also blank-imports x11quiet)
font.go            //go:build gui    — font discovery (fc-match + per-OS paths + TTC parsing), help panel and status bar
anim.go            //go:build gui    — GIF / APNG / Animated WebP frame decoding (pure Go, no Ebitengine)
measure.go         //go:build gui    — measure mode: drag a rectangle, read the source / screen pixels and copy
trash_freedesktop.go  //go:build gui && !darwin && !windows — trash (freedesktop.org)
trash_darwin.go       //go:build gui && darwin            — trash (~/.Trash, restorable from the Finder)
trash_windows.go      //go:build gui && windows           — trash (Recycle Bin)
imgo_stub.go       //go:build !gui   — stub so the CLI build never pulls in Ebitengine
x11quiet/          //go:build gui    — tiny single-file package: filters xgb's missing-Xauthority notice, keeps all other XGB logs
setup.sh           Build script (POSIX sh, Linux/macOS): cross-compile every supported target into ./dist, then move this platform's imgc + imgo into ~/bn (default yes) and optionally register imgo.desktop (Linux, default no)
setup.cmd          The Windows counterpart of setup.sh
.github/workflows/release.yml  CI: on a v* tag, test + cross-compile + publish a Release
LICENSE            MIT License
```

The test files are named after the behaviour they cover (`anim_test.go`, `trash_test.go`, and so on) and are omitted above. The lf / fzf integration scripts are not given their own directory; they live in [Workflow integration](#workflow-integration).

## License and third-party dependencies

This project is released under the [MIT License](LICENSE).

It depends only on **permissively licensed** open source, with no copyleft, so it can be used in commercial and private projects:

| Dependency | License | Use |
|---|---|---|
| [hajimehoshi/ebiten/v2](https://github.com/hajimehoshi/ebiten), [ebitengine/purego](https://github.com/ebitengine/purego), [ebitengine/hideconsole](https://github.com/ebitengine/hideconsole) | Apache-2.0 | GUI rendering (with `-tags gui` only) |
| [golang.org/x/image](https://pkg.go.dev/golang.org/x/image), x/sys, x/term, x/text, x/net, x/sync | BSD-3-Clause | Image codecs, font rendering, terminal I/O |
| [jezek/xgb](https://github.com/jezek/xgb) | BSD-3-Clause | X11 binding (an Ebitengine dependency; referenced directly only to filter the missing-Xauthority notice) |
| [kettek/apng](https://github.com/kettek/apng) | BSD-3-Clause | APNG decoding |
| [mattn/go-sixel](https://github.com/mattn/go-sixel) | MIT | Sixel stream encoding |
| [srwiley/oksvg](https://github.com/srwiley/oksvg), [srwiley/rasterx](https://github.com/srwiley/rasterx) | BSD-3-Clause | SVG rasterisation |
| [soniakeys/quant](https://github.com/soniakeys/quant) | MIT | Palette quantisation (transitive) |

When redistributing binaries, keep the attributions above. Two extra notes:

- the GUI links Go's built-in **Go Regular** font (`golang.org/x/image/font/gofont`, BSD-3-Clause, © The Go Authors) as the final fallback for panel text;
- other fonts (the system default, CJK fonts) are read at **runtime** by `fc-match`; this repository ships no font files and touches no font EULAs.
