#!/usr/bin/env bash
# Validate a static prefix, or reject shared artifacts before installation.
set -euo pipefail
mode=complete
if [[ "${1:-}" == --no-shared ]]; then mode=artifacts; shift; fi
prefix="${1:?usage: check-static-ffmpeg.sh [--no-shared] PREFIX}"
shopt -s nullglob
for lib in avcodec avdevice avfilter avformat swresample swscale avutil; do
  shared=("$prefix/lib/lib$lib"*.dylib "$prefix/lib/lib$lib"*.so* "$prefix/lib/lib$lib"*.dll.a "$prefix/bin/$lib"*.dll "$prefix/lib/$lib"*.lib "$prefix/lib$lib"*.dylib "$prefix/lib$lib"*.so* "$prefix/$lib"*.dll)
  [[ ${#shared[@]} == 0 ]] || { echo "ERROR: shared FFmpeg artifact: ${shared[0]}; use a fresh static prefix" >&2; exit 1; }
  if [[ "$mode" == complete && ! -s "$prefix/lib/lib$lib.a" ]]; then
    echo "ERROR: missing static FFmpeg archive: $prefix/lib/lib$lib.a" >&2; exit 1
  fi
done
if [[ "$mode" == complete ]]; then
  if ! grep -q '^Linkage: static$' "$prefix/share/BUILDING-FFMPEG.txt" ||
    ! grep -q 'LGPL version 2.1 or later' "$prefix/share/BUILDING-FFMPEG.txt"; then
      echo 'ERROR: missing static LGPL FFmpeg build record; run make ffmpeg' >&2; exit 1;
  fi
fi
