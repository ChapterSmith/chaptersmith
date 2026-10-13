#!/usr/bin/env bash
# cgo adapter: apply --static to FFmpeg only (Libs.private), keep Wails' GTK /
# WebKit and other system packages dynamic. Bind every FFmpeg library by path.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
prefix="${FFMPEG_PREFIX:-$ROOT/vendor/ffmpeg}"
real="${FFMPEG_PKG_CONFIG:-pkg-config}"
ffmpeg=false
for arg in "$@"; do
  case "$arg" in libavcodec|libavdevice|libavfilter|libavformat|libavutil|libswresample|libswscale) ffmpeg=true;; esac
done
if [[ "$ffmpeg" == false ]]; then exec "$real" "$@"; fi
"$ROOT/scripts/check-static-ffmpeg.sh" "$prefix"
prefix="$(cd "$prefix" && pwd -P)"
case "$(uname -s)" in MSYS*|MINGW*|CYGWIN*) prefix="$(cygpath -m "$prefix")";; esac
# Native Windows pkg-config splits its search path on ';': a ':' joined list of
# D:/... entries is one unusable directory there.
case "$(uname -s)" in
  MSYS*|MINGW*|CYGWIN*) export PKG_CONFIG_PATH="$prefix/lib/pkgconfig${PKG_CONFIG_PATH:+;$(cygpath -m -p "$PKG_CONFIG_PATH")}";;
  *) export PKG_CONFIG_PATH="$prefix/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}";;
esac
for lib in avcodec avdevice avfilter avformat avutil swresample swscale; do
  actual="$("$real" --print-errors --variable=prefix "lib$lib")"
  case "$(uname -s)" in MSYS*|MINGW*|CYGWIN*) actual="$(cygpath -m "$actual")";; esac
  [[ "$actual" == "$prefix" ]] || { echo "ERROR: wrong FFmpeg prefix for lib$lib: $actual (expected $prefix)" >&2; exit 1; }
done
out="$("$real" --static "$@")"
# pkg-config quotes paths with whitespace using backslashes; preserve that syntax.
# Replacement tokens are quoted for cgo's pkg-config output parser.
for lib in avcodec avdevice avfilter avformat avutil swresample swscale; do
  archive="\"$prefix/lib/lib$lib.a\""
  out="${out//-l$lib /$archive }"
  if [[ "$out" == *"-l$lib" ]]; then out="${out%-l"$lib"}\"$prefix/lib/lib$lib.a\""; fi
done
printf '%s\n' "$out"
