#!/bin/sh
# Build THIS platform's imgc + imgo into ./dist, then offer to move them into
# ~/bn and (on Linux) register imgo.desktop as the default handler for image
# AND pdf formats.
#
# POSIX sh, so it runs on any Linux / macOS without zsh. Windows: use setup.cmd.
set -e

VERSION=$(date +%y.%m.%d)

dist="$(pwd)/dist"
rm -rf "$dist"
mkdir -p "$dist"

host_os="$(go env GOOS)"
host_arch="$(go env GOARCH)"
ext=
if [ "$host_os" = windows ]; then ext=.exe; fi

echo "Building for $host_os/$host_arch into ./dist"
echo "  imgc ..."
CGO_ENABLED=0 GOOS="$host_os" GOARCH="$host_arch" \
    go build -trimpath -ldflags="-s -w -X main.buildVersion=$VERSION" \
    -o "$dist/imgc-${host_os}_${host_arch}${ext}" .
echo "  imgo ..."
if [ "$host_os" = windows ]; then
    # Ebitengine uses purego on Windows, so it cross-builds without cgo.
    CGO_ENABLED=0 GOOS=windows GOARCH="$host_arch" \
        go build -trimpath -tags gui \
        -ldflags="-s -w -X main.buildVersion=$VERSION -X main.appName=imgo -H=windowsgui" \
        -o "$dist/imgo-${host_os}_${host_arch}${ext}" .
else
    # A Linux / macOS GUI needs cgo, so build natively on the host.
    go build -trimpath -tags gui \
        -ldflags="-s -w -X main.buildVersion=$VERSION -X main.appName=imgo" \
        -o "$dist/imgo-${host_os}_${host_arch}${ext}" .
fi
echo

# Move this platform's pair into ~/bn and drop dist (default: yes).
printf 'Move this platform (%s/%s) imgc + imgo to ~/bn and delete dist/? [Y/n] ' \
    "$host_os" "$host_arch"
read -r ans
case "$ans" in
n|N)
    echo "Kept $dist"
    ;;
*)
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
    printf 'Associate image + PDF formats (desktop entry + default handler)? [y/N] '
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
MimeType=image/png;image/jpeg;image/gif;image/webp;image/bmp;image/tiff;image/x-icon;image/svg+xml;image/avif;image/heic;image/heif;image/jxl;application/pdf;
EOF
        update-desktop-database "$apps" 2>/dev/null || true
        # set imgo as the default handler for all image types and PDF
        for m in image/png image/jpeg image/gif image/webp image/bmp image/tiff \
                 image/x-icon image/svg+xml image/avif image/heic image/heif image/jxl \
                 application/pdf; do
            xdg-mime default imgo.desktop "$m"
        done
        echo "Installed: $apps/imgo.desktop (default handler set for all image + PDF types)"
        ;;
    *)
        echo "Skipped format association"
        ;;
    esac
else
    echo "Desktop entry: skipped (Linux only)"
fi
