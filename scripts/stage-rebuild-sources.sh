#!/usr/bin/env bash
# Stage exact committed application + corresponding static FFmpeg source.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec python3 "$ROOT/scripts/source-release.py" stage "$ROOT" \
  "${FFMPEG_PREFIX:-$ROOT/vendor/ffmpeg}" "${1:?usage: stage-rebuild-sources.sh OUTPUT_DIR}" \
  "${VERSION:-$(git -C "$ROOT" describe --tags --always)}" \
  "${SOURCE_PLATFORM:-$(uname -s)-$(uname -m)}"
