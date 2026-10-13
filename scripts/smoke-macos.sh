#!/usr/bin/env bash
# smoke-macos.sh OUT_DIR — check mounted GUI and CLI package, launch the GUI,
# and merge fixtures. CI installs the PKG; local runs only expand its payload.
set -euo pipefail
OUT_DIR="${1:?usage: smoke-macos.sh OUT_DIR}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$(uname -s)" == Darwin ]] || { echo 'smoke-macos: requires macOS' >&2; exit 1; }
# Avoid ls/head pipelines and reject ambiguous artifacts rather than testing an old build.
shopt -s nullglob
dmgs=("$OUT_DIR"/ChapterSmith-*-macos-*.dmg)
pkgs=("$OUT_DIR"/ChapterSmith-CLI-*-macos-*.pkg)
[[ ${#dmgs[@]} == 1 && ${#pkgs[@]} == 1 ]] || {
  echo 'smoke-macos: expected exactly one DMG and one PKG in OUT_DIR' >&2; exit 1;
}
DMG="${dmgs[0]}"; PKG="${pkgs[0]}"
python3 "$ROOT/scripts/test-dmg-layout.py" "$DMG"
SIGNED=false
if [[ -n "${MACOS_SIGN_IDENTITY:-}" && "${MACOS_SIGN_IDENTITY}" != - ]]; then SIGNED=true; fi
if [[ "${REQUIRE_SIGNING:-false}" == true && "$SIGNED" != true ]]; then
  echo 'smoke-macos: REQUIRE_SIGNING=true requires Developer ID signing' >&2; exit 1
fi
WORK="$(mktemp -d)"
MNT="$WORK/mount"; mkdir -p "$MNT"
MOUNTED=false; GUI_PID=""
cleanup() {
  if [[ -n "$GUI_PID" ]]; then kill "$GUI_PID" 2>/dev/null || true; wait "$GUI_PID" 2>/dev/null || true; fi
  if [[ "$MOUNTED" == true ]]; then hdiutil detach "$MNT" >/dev/null || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

verify_signature() {
  local file="$1" details
  codesign --verify --strict --verbose=2 "$file"
  if [[ "$SIGNED" == true ]]; then
    details="$(codesign --display --verbose=4 "$file" 2>&1)"
    grep -q '^Authority=Developer ID Application:' <<<"$details" || {
      echo "smoke-macos: missing Developer ID signature: $file" >&2; exit 1;
    }
  fi
}

hdiutil attach "$DMG" -nobrowse -readonly -mountpoint "$MNT" >/dev/null
MOUNTED=true
APP="$MNT/ChapterSmith.app"
GUI="$APP/Contents/MacOS/chaptersmith-gui"
"$ROOT/scripts/check-static-ffmpeg.sh" --no-shared "$APP/Contents/Frameworks"
"$ROOT/scripts/verify-deps.sh" "$APP/Contents/MacOS" "$GUI"
codesign --verify --deep --strict --verbose=2 "$APP"
verify_signature "$APP"
verify_signature "$GUI"
[[ -f "$APP/Contents/Resources/licenses/BUILDING-FFMPEG.txt" ]]
if [[ "$SIGNED" == true ]]; then
  verify_signature "$DMG"
  xcrun stapler validate "$DMG"
  spctl --assess --type execute --verbose "$APP"
  pkgutil --check-signature "$PKG"
  xcrun stapler validate "$PKG"
  spctl --assess --type install --verbose "$PKG"
fi

# Direct launch lets the test observe its own process instead of finding/killing
# an unrelated user application. Five seconds catches loader/signature crashes.
"$GUI" >"$WORK/gui.log" 2>&1 &
GUI_PID=$!
sleep 5
if ! kill -0 "$GUI_PID" 2>/dev/null; then
  cat "$WORK/gui.log" >&2
  echo 'smoke-macos: GUI exited during startup' >&2; exit 1
fi
kill "$GUI_PID"; wait "$GUI_PID" 2>/dev/null || true; GUI_PID=""

if [[ "${CI:-false}" == true ]]; then
  # Installation remains covered on disposable CI hosts, never on a local host.
  sudo installer -pkg "$PKG" -target /
  LIBDIR=/usr/local/lib/chaptersmith
  CLI=/usr/local/bin/chaptersmith
  # Reinstall must remove stale libraries from a previous release.
  sudo touch "$LIBDIR/stale-library.dylib"
  sudo installer -pkg "$PKG" -target /
  [[ ! -e "$LIBDIR/stale-library.dylib" ]]
else
  pkgutil --expand-full "$PKG" "$WORK/expanded"
  LIBDIR="$WORK/expanded/Payload/usr/local/lib/chaptersmith"
  [[ -d "$LIBDIR" ]] || { echo 'smoke-macos: CLI package payload missing' >&2; exit 1; }
  WRAPPER="$WORK/expanded/Payload/usr/local/bin/chaptersmith"
  [[ -x "$WRAPPER" ]]
  grep -Fq 'exec /usr/local/lib/chaptersmith/chaptersmith "$@"' "$WRAPPER"
  /bin/sh -n "$WRAPPER"
  # The installed wrapper uses an absolute path; use its actual target in the
  # expanded tree without modifying /usr/local.
  CLI="$LIBDIR/chaptersmith"
fi
"$ROOT/scripts/check-static-ffmpeg.sh" --no-shared "$LIBDIR"
"$ROOT/scripts/verify-deps.sh" "$LIBDIR" "$LIBDIR/chaptersmith"
verify_signature "$LIBDIR/chaptersmith"
[[ -f "$LIBDIR/licenses/BUILDING-FFMPEG.txt" ]]
"$CLI" -version | grep -q 'LGPLv2.1'
mkdir -p "$WORK/merge"
cp "$ROOT"/testdata/formats/mp3/0{1,2}.mp3 "$ROOT/testdata/formats/cover.jpg" "$WORK/merge/"
(cd "$WORK/merge" && "$CLI" -o out.m4b </dev/null)
[[ -s "$WORK/merge/out.m4b" ]]
echo 'smoke-macos: OK'
