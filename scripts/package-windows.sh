#!/usr/bin/env bash
# package-windows.sh gui|cli — build the NSIS installer from dist/ (run in MSYS2 UCRT64).
# Code signing is not configured yet (planned: WINDOWS_SIGN_* secrets).
set -euo pipefail
shopt -s nullglob
KIND="${1:?usage: package-windows.sh gui|cli}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
OUT_DIR="${OUT_DIR:-$DIST_DIR/packages}"
FFMPEG_PREFIX="${FFMPEG_PREFIX:-$ROOT/vendor/ffmpeg}"
export FFMPEG_PREFIX
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
MAKENSIS="${MAKENSIS:-makensis}"
WEBVIEW2_BOOTSTRAPPER_URL="https://go.microsoft.com/fwlink/p/?LinkId=2124703"

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
STAGE="$WORK/stage"; mkdir -p "$STAGE" "$OUT_DIR"
# makensis resolves a relative output path against the script's directory.
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
case "$KIND" in
  gui)
    cp "$DIST_DIR/chaptersmith-gui.exe" "$STAGE/"
    curl -fsSL "$WEBVIEW2_BOOTSTRAPPER_URL" -o "$STAGE/MicrosoftEdgeWebview2Setup.exe"
    OUT="$OUT_DIR/ChapterSmith-$VERSION-windows-x64-setup.exe"
    ;;
  cli)
    cp "$DIST_DIR/chaptersmith.exe" "$STAGE/"
    OUT="$OUT_DIR/ChapterSmith-CLI-$VERSION-windows-x64-setup.exe"
    ;;
  *) echo "usage: package-windows.sh gui|cli" >&2; exit 1 ;;
esac

"$ROOT/scripts/collect-ffmpeg-libs.sh" "$FFMPEG_PREFIX" "$STAGE"
"$ROOT/scripts/stage-licenses.sh" "$STAGE/licenses"
"$ROOT/scripts/verify-deps.sh" "$STAGE" "$STAGE"/chaptersmith*.exe "$STAGE"/*.dll

"$MAKENSIS" -DKIND="$KIND" -DVERSION="${VERSION#v}" -DSTAGE="$(cygpath -w "$STAGE")" \
  -DOUTFILE="$(cygpath -w "$OUT")" -DICON="$(cygpath -w "$ROOT/build/windows/icon.ico")" \
  "$(cygpath -w "$ROOT/scripts/package-windows.nsi")"
echo "==> $OUT"
