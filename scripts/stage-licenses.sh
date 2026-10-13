#!/usr/bin/env bash
# stage-licenses.sh DEST — collect every license/notice file shipped with a package:
# application license/guide, LGPL text + FFmpeg/zlib notices, build instructions
# from the FFmpeg prefix, and licenses of Go modules compiled into ./cmd/... .
set -euo pipefail
DEST="${1:?usage: stage-licenses.sh DEST}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FFMPEG_PREFIX="${FFMPEG_PREFIX:-$ROOT/vendor/ffmpeg}"
BUILDING="$FFMPEG_PREFIX/share/BUILDING-FFMPEG.txt"
[[ -f "$BUILDING" ]] || { echo "stage-licenses: $BUILDING not found (run make ffmpeg)" >&2; exit 1; }
for required in "$ROOT/LICENSE" "$ROOT/README.md"; do
  [[ -f "$required" ]] || { echo "stage-licenses: required licensing input $required not found" >&2; exit 1; }
done

mkdir -p "$DEST/go-modules"
cp "$ROOT"/licenses/*.txt "$BUILDING" "$DEST/"
cp "$ROOT/LICENSE" "$DEST/ChapterSmith-LICENSE.txt"
cp "$ROOT/README.md" "$DEST/README.md"
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    cp "${MINGW_PREFIX:-/ucrt64}/share/licenses/libwinpthread/COPYING" "$DEST/WINPTHREADS-COPYING.txt"
    ;;
esac
if [[ -f "$FFMPEG_PREFIX/share/ffmpeg-build.json" ]]; then
  cp "$FFMPEG_PREFIX/share/ffmpeg-build.json" "$DEST/"
fi

mods="$(cd "$ROOT" && go list -mod=readonly -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' ./cmd/... | sort -u)"

count=0
while read -r mod dir; do
  [[ -z "$dir" ]] && continue
  found=""
  for lic in "$dir"/LICENSE* "$dir"/COPYING* "$dir"/NOTICE*; do
    [[ -f "$lic" ]] || continue
    mkdir -p "$DEST/go-modules/$mod"
    cp "$lic" "$DEST/go-modules/$mod/"
    found=1
  done
  if [[ -n "$found" ]]; then count=$((count + 1)); else echo "stage-licenses: WARNING no license file in $mod" >&2; fi
done <<<"$mods"

if [[ "$count" -eq 0 ]]; then
  echo "stage-licenses: no Go module licenses found" >&2
  exit 1
fi

echo "stage-licenses: $count Go module license(s) + notices in $DEST"
