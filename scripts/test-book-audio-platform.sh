#!/usr/bin/env bash
# Run the real audio test wrapper with a recording Go tool: verify command
# construction without compiling FFmpeg/Wails or requiring a Windows host.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/project/scripts" "$WORK/prefix/lib" "$WORK/prefix/share" "$WORK/tools"
cp "$ROOT/scripts/"{test-book-audio,prepare-static-ffmpeg,check-static-ffmpeg}.sh "$WORK/project/scripts/"
for lib in avcodec avdevice avfilter avformat avutil swresample swscale; do
  printf archive > "$WORK/prefix/lib/lib$lib.a"
done
printf 'Linkage: static\nLGPL version 2.1 or later\n' > "$WORK/prefix/share/BUILDING-FFMPEG.txt"
cat > "$WORK/tools/go" <<'TOOL'
#!/usr/bin/env bash
set -eu
case "$1" in
  build) exit 0;; # Native launcher compilation is already covered by native builds.
  test) printf '%s\n' "$@" > "$TEST_COMMAND";;
  *) exit 91;;
esac
TOOL
cat > "$WORK/tools/uname" <<'TOOL'
#!/bin/sh
printf '%s\n' "$TEST_OS"
TOOL
cat > "$WORK/tools/cygpath" <<'TOOL'
#!/bin/sh
# These scratch paths already work on the host running this command-only fixture.
printf '%s\n' "$2"
TOOL
chmod +x "$WORK/tools/"*
check() {
  local os="$1"; shift
  PATH="$WORK/tools:$PATH" TEST_OS="$os" TEST_COMMAND="$WORK/actual" FFMPEG_PREFIX="$WORK/prefix" \
    bash "$WORK/project/scripts/test-book-audio.sh" "$@"
  diff -u "$WORK/expected" "$WORK/actual" || { echo "test-book-audio-platform: incorrect $os test command" >&2; exit 1; }
}
printf '%s\n' test -tags=gtk3 -race ./... > "$WORK/expected"
check Linux -race ./...
printf '%s\n' test -tags=gtk3,custom -count=1 ./internal/audio > "$WORK/expected"
check Linux -tags=custom -count=1 ./internal/audio
printf '%s\n' test '-tags=gtk3,custom other' -run 'Test name' ./... -args -tags=fixture > "$WORK/expected"
check Linux -tags 'custom other' -run 'Test name' ./... -args -tags=fixture
printf '%s\n' test -race -tags=custom ./... -args 'argument with spaces' > "$WORK/expected"
check Darwin -race -tags=custom ./... -args 'argument with spaces'
check MINGW64_NT -race -tags=custom ./... -args 'argument with spaces'
echo 'test-book-audio-platform: OK'
