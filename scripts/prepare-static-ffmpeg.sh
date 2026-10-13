#!/usr/bin/env bash
# Build the native pkg-config launcher in this checkout (including Windows).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
"$ROOT/scripts/check-static-ffmpeg.sh" "${FFMPEG_PREFIX:-$ROOT/vendor/ffmpeg}"
mkdir -p "$ROOT/vendor/ffmpeg-tools"
exe=""
case "$(uname -s)" in MSYS*|MINGW*|CYGWIN*) exe=.exe;; esac
CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o "$ROOT/vendor/ffmpeg-tools/pkg-config$exe" "$ROOT/scripts/ffmpeg-pkg-config/main.go"
