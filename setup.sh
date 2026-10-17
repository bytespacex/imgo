#!/bin/sh
# Cross-compile every supported target into ./dist, then offer to move this
# platform's imgc + imgo into ~/bn and (on Linux) register imgo.desktop.
#
# POSIX sh, so it runs on any Linux / macOS without zsh. Windows: use setup.cmd.
set -e

dist="$(pwd)/dist"
rm -rf "$dist"
mkdir -p "$dist"

host_os="$(go env GOOS)"
host_arch="$(go env GOARCH)"

echo "Products will be generated into ./dist ($dist)"
echo

# imgc is pure Go: every platform cross-compiles.
echo "  imgc ..."
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
    os=${target%%/*}
    arch=${target#*/}
    ext=
    if [ "$os" = windows ]; then ext=.exe; fi
    name="imgc-${os}_${arch}${ext}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -trimpath -ldflags="-s -w" -o "$dist/$name" .
    echo "    $name"
done

# imgo: Windows cross-builds (Ebitengine uses purego there, no cgo) plus the host
# platform natively. A Linux / macOS GUI needs cgo, so it cannot be cross-built.
echo "  imgo ..."
for arch in amd64 arm64; do
    name="imgo-windows_${arch}.exe"
    CGO_ENABLED=0 GOOS=windows GOARCH="$arch" \
        go build -trimpath -tags gui -ldflags="-s -w -H=windowsgui" -o "$dist/$name" .
    echo "    $name"
done
if [ "$host_os" != windows ]; then
    name="imgo-${host_os}_${host_arch}"
    go build -trimpath -tags gui -ldflags="-s -w" -o "$dist/$name" .
    echo "    $name"
fi
echo

# Move this platform's pair into ~/bn and drop dist (default: yes).
printf 'Move this platform (%s/%s) imgc + imgo to ~/bn and delete dist/? [Y/n] ' "$host_os" "$host_arch"
read -r ans
case "$ans" in
n|N)
    echo "Kept $dist"
    ;;
*)
    ext=
    if [ "$host_os" = windows ]; then ext=.exe; fi
    mkdir -p "$HOME/bn"
    mv "$dist/imgc-${host_os}_${host_arch}${ext}" "$HOME/bn/imgc${ext}"
    if [ -f "$dist/imgo-${host_os}_${host_arch}${ext}" ]; then
        mv "$dist/imgo-${host_os}_${host_arch}${ext}" "$HOME/bn/imgo${ext}"
    fi
    rm -rf "$dist"
    ls -lh "$HOME/bn/imgc${ext}" "$HOME/bn/imgo${ext}" 2>/dev/null || true
    ;;
esac

# Desktop entry + default file associations (Linux only; default: no).
if [ "$(uname -s)" = Linux ]; then
    printf 'Associate image formats (desktop entry + default handler)? [y/N] '
    read -r ans
    case "$ans" in
    y|Y)
        apps="$HOME/.local/share/applications"
        mkdir -p "$apps"
        cat > "$apps/imgo.desktop" <<EOF
[Desktop Entry]
Terminal=false
Exec=imgo %F
Icon=image-x-generic
Type=Application
Categories=Graphics;Viewer;
StartupNotify=true
Name=imgo
GenericName=Image Viewer
MimeType=image/png;image/jpeg;image/gif;image/webp;image/bmp;image/tiff;image/x-icon;image/svg+xml;image/avif;image/heic;image/heif;image/jxl;
EOF
        update-desktop-database "$apps" 2>/dev/null || true
        # set imgo as the default handler for all image types
        for m in image/png image/jpeg image/gif image/webp image/bmp image/tiff \
                 image/x-icon image/svg+xml image/avif image/heic image/heif image/jxl; do
            xdg-mime default imgo.desktop "$m"
        done
        echo "Installed: $apps/imgo.desktop (default handler set for all image types)"
        ;;
    *)
        echo "Skipped format association"
        ;;
    esac
else
    echo "Desktop entry: skipped (Linux only)"
fi
