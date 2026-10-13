#!/usr/bin/env bash
# package-linux.sh gui|cli — build an AppImage with static FFmpeg.
# GTK/WebKit (GUI) come from the system. Needs appimagetool
# (set APPIMAGE_EXTRACT_AND_RUN=1 where FUSE is unavailable, e.g. CI/Docker).
set -euo pipefail
KIND="${1:?usage: package-linux.sh gui|cli}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
OUT_DIR="${OUT_DIR:-$DIST_DIR/packages}"
FFMPEG_PREFIX="${FFMPEG_PREFIX:-$ROOT/vendor/ffmpeg}"
export FFMPEG_PREFIX
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
APPIMAGETOOL="${APPIMAGETOOL:-appimagetool}"
[[ "$VERSION" != */* && "$VERSION" != *$'\n'* ]] || { echo "invalid VERSION" >&2; exit 1; }
[[ -d "$DIST_DIR" && -d "$FFMPEG_PREFIX" ]] || { echo "missing build or FFmpeg directory" >&2; exit 1; }
DIST_DIR="$(cd "$DIST_DIR" && pwd -P)"
FFMPEG_PREFIX="$(cd "$FFMPEG_PREFIX" && pwd -P)"
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
case "$(uname -m)" in x86_64) ARCH=x86_64 ;; aarch64|arm64) ARCH=aarch64 ;; *) echo "unsupported arch" >&2; exit 1 ;; esac

case "$KIND" in
  gui) BIN=chaptersmith-gui; NAME=ChapterSmith;     TITLE="ChapterSmith";     TERMINAL=false; CATEGORIES="AudioVideo;Audio;" ;;
  cli) BIN=chaptersmith;     NAME=ChapterSmith-CLI; TITLE="ChapterSmith CLI"; TERMINAL=true;  CATEGORIES="Utility;" ;;
  *) echo "usage: package-linux.sh gui|cli" >&2; exit 1 ;;
esac

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
APPDIR="$WORK/$NAME.AppDir"
mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/lib" "$OUT_DIR"
cp "$DIST_DIR/$BIN" "$APPDIR/usr/bin/$BIN"
"$ROOT/scripts/collect-ffmpeg-libs.sh" "$FFMPEG_PREFIX" "$APPDIR/usr/lib"
"$ROOT/scripts/stage-licenses.sh" "$APPDIR/usr/share/licenses/chaptersmith"
"$ROOT/scripts/verify-deps.sh" "$APPDIR/usr/lib" "$APPDIR/usr/bin/$BIN"

cat > "$APPDIR/AppRun" <<EOF
#!/bin/sh
HERE="\$(dirname "\$(readlink -f "\$0")")"
exec "\$HERE/usr/bin/$BIN" "\$@"
EOF
chmod 755 "$APPDIR/AppRun"

cat > "$APPDIR/$NAME.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=$TITLE
Exec=$BIN
Icon=chaptersmith
Terminal=$TERMINAL
Categories=$CATEGORIES
EOF
cp "$ROOT/logo.png" "$APPDIR/chaptersmith.png"

OUT="$OUT_DIR/$NAME-$VERSION-linux-$ARCH.AppImage"
ARCH="$ARCH" "$APPIMAGETOOL" --no-appstream "$APPDIR" "$WORK/package.AppImage"
chmod 755 "$WORK/package.AppImage"
mv -f "$WORK/package.AppImage" "$OUT"
echo "==> $OUT"
