#!/usr/bin/env bash
# Verify/extract an archive and rebuild CLI + GUI in owned scratch, without keys.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCHIVE="${1:?usage: test-rebuild-sources.sh ARCHIVE [--modified-ffmpeg]}"
MODE="${2:-}"
[[ -z "$MODE" || "$MODE" == --modified-ffmpeg ]] || { echo 'unknown rebuild mode' >&2; exit 1; }
WORK="$(mktemp -d "${TMPDIR:-/tmp}/audiobook-rebuild.XXXXXX")"
trap 'if [[ "${KEEP_REBUILD_WORK:-0}" == 1 ]]; then echo "Retained rebuild: $WORK"; else rm -rf "$WORK"; fi' EXIT
python3 "$ROOT/scripts/source-release.py" extract "$ARCHIVE" "$WORK"
# All following paths are inside the extracted materials. Shared tool/dependency
# caches may be used, but no original checkout source, prefix or embed directory.
unset FFMPEG_PREFIX PKG_CONFIG CGO_CFLAGS CGO_LDFLAGS PKG_CONFIG_PATH GOFLAGS GOENV GOWORK
unset MACOS_SIGN_IDENTITY MACOS_INSTALLER_IDENTITY APPLE_API_KEY_PATH APPLE_API_KEY_ID APPLE_API_ISSUER_ID
export GOENV=off GOWORK=off GOFLAGS=-mod=readonly REQUIRE_SIGNING=false
cd "$WORK/app"
VERSION="$(python3 -c 'import json; print(json.load(open("../release.json"))["version"])')"
export VERSION
if [[ "$MODE" == --modified-ffmpeg ]]; then
  # Compatible muxer metadata change, observable in the actual CLI's M4B output.
  python3 - <<'PY'
from pathlib import Path
path = Path('../ffmpeg/libavformat/movenc.c')
source = path.read_text()
old = 'mov_write_string_tag(pb, "\\251too", LIBAVFORMAT_IDENT, 0, 1);'
assert source.count(old) == 1, 'unsupported FFmpeg muxer source'
path.write_text(source.replace(old, 'mov_write_string_tag(pb, "\\251too", "LGPL-RELINK-PROOF-20261010", 0, 1);'))
PY
fi
NPROC="${NPROC:-2}" bash scripts/build-ffmpeg.sh --source-dir "$WORK/ffmpeg" --prefix "$PWD/vendor/ffmpeg"
go mod download
make build build-gui
EXT=""; case "$(uname -s)" in MSYS*|MINGW*|CYGWIN*) EXT=.exe;; esac
bash scripts/verify-deps.sh dist "dist/chaptersmith$EXT" "dist/chaptersmith-gui$EXT"
"dist/chaptersmith$EXT" -version
if [[ "$(uname -s)" == Darwin ]]; then
  codesign --force --deep --sign - 'dist/ChapterSmith.app'
  codesign --verify --deep --strict 'dist/ChapterSmith.app'
fi
mkdir "$WORK/merge"
cp testdata/formats/mp3/01.mp3 testdata/formats/mp3/02.mp3 "$WORK/merge/"
(cd "$WORK/merge" && "$WORK/app/dist/chaptersmith$EXT" -o out.m4b </dev/null)
[[ -s "$WORK/merge/out.m4b" ]]
if [[ "$MODE" == --modified-ffmpeg ]]; then
  python3 - "$WORK/merge/out.m4b" <<'PY'
from pathlib import Path
import sys
assert b'LGPL-RELINK-PROOF-20261010' in Path(sys.argv[1]).read_bytes(), 'rebuilt CLI did not use modified FFmpeg'
print('Modified FFmpeg marker verified in real CLI M4B output')
PY
fi
bash scripts/test-book-audio.sh -count=1 ./internal/audio -run '^TestBundledAudioCapabilities$'
echo "test-rebuild-sources: OK ${MODE:-unmodified}"
