#!/usr/bin/env bash
# Compatibility entry point: FFmpeg is static, so no FFmpeg libraries are copied.
# Windows still needs any non-system DLL dependencies (e.g. dynamically linked
# zlib) of the staged executables. System GUI/WebView libraries remain dynamic.
set -euo pipefail
PREFIX="${1:?usage: collect-ffmpeg-libs.sh PREFIX DEST}"
DEST="${2:?usage: collect-ffmpeg-libs.sh PREFIX DEST}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
"$ROOT/scripts/check-static-ffmpeg.sh" "$PREFIX"
mkdir -p "$DEST"
"$ROOT/scripts/check-static-ffmpeg.sh" --no-shared "$DEST"
shopt -s nullglob
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    bins=("$DEST"/chaptersmith*.exe)
    [[ ${#bins[@]} -gt 0 ]] || { echo 'ERROR: no staged executables for dependency collection' >&2; exit 1; }
    # Inspect application imports and bundled DLLs, never optional dependencies
    # of Windows system DLLs. ntldd is native and requires an explicit Windows path.
    bins+=("$DEST"/*.dll)
    index=0
    while [[ $index -lt ${#bins[@]} ]]; do
      bin="${bins[$index]}"; index=$((index + 1))
      deps="$(MSYS2_ARG_CONV_EXCL='*' ntldd "$(cygpath -w "$bin")")"
      while IFS= read -r line; do
        [[ "$line" == *"=> "* ]] || continue
        dep="${line#*=> }"; dep="${dep% (0x*}"
        dep="$(printf '%s' "$dep" | tr '\134' '/')"
        case "$(printf '%s' "$dep" | tr '[:upper:]' '[:lower:]')" in
          */ucrt64/bin/*)
            source="$(cygpath -u "$dep")"
            target="$DEST/$(basename "$source")"
            if [[ ! -f "$target" ]]; then
              cp "$source" "$target"
              bins+=("$target")
            fi
            ;;
        esac
      done <<<"$deps"
    done
    ;;
esac
echo "collect-ffmpeg-libs: static FFmpeg; checked $DEST"
