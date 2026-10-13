#!/usr/bin/env bash
# Build the LGPL FFmpeg audio feature set as static archives.
# --source-dir consumes a compatible source tree without modifying it. All
# configuration/compilation occurs in an owned copy and separate build directory.
set -euo pipefail
# BSD tar must not turn host extended attributes into AppleDouble source files.
export COPYFILE_DISABLE=1
FFMPEG_VERSION=n8.0
FFMPEG_REVISION=140fd653aed8cad774f991ba083e2d01e86420c7
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PREFIX="$PROJECT_ROOT/vendor/ffmpeg"
SOURCE_DIR=""
NPROC="${NPROC:-$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) PREFIX="${2:?--prefix needs a path}"; shift 2;;
    --prefix=*) PREFIX="${1#*=}"; shift;;
    --source-dir) SOURCE_DIR="${2:?--source-dir needs a path}"; shift 2;;
    --source-dir=*) SOURCE_DIR="${1#*=}"; shift;;
    *) echo "Unknown argument: $1" >&2; exit 1;;
  esac
done
for cmd in git make pkg-config tar python3; do
  command -v "$cmd" >/dev/null || { echo "ERROR: $cmd is required" >&2; exit 1; }
done
# Canonicalize without creating anything in an input source tree.
if [[ -n "$SOURCE_DIR" ]]; then
  SOURCE_DIR="$(cd "$SOURCE_DIR" && pwd -P)"
  [[ -f "$SOURCE_DIR/configure" ]] || { echo 'ERROR: source has no configure' >&2; exit 1; }
fi
# realpath -m is not available on macOS. Resolve existing ancestors with cd.
absolute_path() {
  local p="$1" parent leaf
  if [[ -d "$p" ]]; then (cd "$p" && pwd -P); return; fi
  parent="$(dirname "$p")"; leaf="$(basename "$p")"
  printf '%s/%s\n' "$(absolute_path "$parent")" "$leaf"
}
PREFIX="$(absolute_path "$PREFIX")"
BUILD_ROOT="$(absolute_path "$PROJECT_ROOT/vendor/ffmpeg-build")"
[[ "$PREFIX" != "$BUILD_ROOT" && "$PREFIX" != "$BUILD_ROOT/"* && "$BUILD_ROOT" != "$PREFIX/"* ]] || {
  echo 'ERROR: prefix and build directories must not overlap' >&2; exit 1;
}
if [[ -n "$SOURCE_DIR" ]]; then
  for output in "$PREFIX" "$BUILD_ROOT"; do
    [[ "$output" != "$SOURCE_DIR" && "$output" != "$SOURCE_DIR/"* && "$SOURCE_DIR" != "$output/"* ]] || {
      echo 'ERROR: source, prefix and build directories must not overlap' >&2; exit 1;
    }
  done
fi
# Refuse stale shared artifacts before any write. A new prefix is the safe way
# to switch from the former shared build; never delete a caller-owned prefix.
"$SCRIPT_DIR/check-static-ffmpeg.sh" --no-shared "$PREFIX"
mkdir -p "$BUILD_ROOT" "$PREFIX"
WORK="$(mktemp -d "$BUILD_ROOT/static.XXXXXX")"
FFMPEG_SRC="$WORK/source"
mkdir -p "$FFMPEG_SRC" "$WORK/build"
if [[ -n "$SOURCE_DIR" ]]; then
  # Dereference symlinks: neither make nor configure can write through a copied
  # source symlink back into the caller's source/build directory.
  (cd "$SOURCE_DIR" && tar -chf - --exclude=.git .) | (cd "$FFMPEG_SRC" && tar -xf -)
  SOURCE_REVISION="$(git -C "$SOURCE_DIR" rev-parse HEAD 2>/dev/null || echo unversioned)"
  git -C "$SOURCE_DIR" diff --binary HEAD > "$WORK/source-changes.patch" 2>/dev/null || true
  git -C "$SOURCE_DIR" status --porcelain > "$WORK/source-status.txt" 2>/dev/null || true
  # A previously configured input must be cleaned only in our owned copy.
  if [[ -f "$FFMPEG_SRC/ffbuild/config.mak" ]]; then make -C "$FFMPEG_SRC" distclean; fi
else
  git clone --depth 1 --branch "$FFMPEG_VERSION" https://github.com/FFmpeg/FFmpeg.git "$FFMPEG_SRC"
  SOURCE_REVISION="$(git -C "$FFMPEG_SRC" rev-parse HEAD)"
  [[ "$SOURCE_REVISION" == "$FFMPEG_REVISION" ]] || { echo 'ERROR: FFmpeg tag does not match pinned revision' >&2; exit 1; }
fi
# Fingerprint the exact prepared snapshot (including untracked source changes).
SOURCE_FINGERPRINT="$(cd "$FFMPEG_SRC" && tar -cf - --exclude=.git . | git hash-object --stdin)"
# Preserve exact complete source before configure/build, independently of checkout
# paths. The installed record below binds this archive to the compiled libraries.
(cd "$FFMPEG_SRC" && tar -czhf "$WORK/ffmpeg-source.tar.gz" --exclude=.git .)
cd "$WORK/build"
echo "=== FFmpeg LGPL Static Build ($SOURCE_REVISION) ==="
echo "Source snapshot: $FFMPEG_SRC"
echo "Build directory: $WORK/build"
echo "Prefix: $PREFIX"
case "$(uname -s)" in MSYS*|MINGW*|CYGWIN*) PREFIX="$(cygpath -m "$PREFIX")";; esac
# FFmpeg options intentionally take comma-separated feature names.
# shellcheck disable=SC2054
CONFIGURE_ARGS=(
    --prefix="${PREFIX}"
    --enable-static --disable-shared
    --disable-programs --disable-doc --disable-network --disable-autodetect
    --disable-everything
    --enable-pic --enable-small --enable-zlib
    --enable-protocol=file
    --enable-demuxer=mp3,aac,ogg,flac,wav,mov,matroska,concat,image2,png_pipe,image_jpeg_pipe
    --enable-muxer=mp4,ipod,adts,flac,wav
    --enable-decoder=mp3,mp3float,aac,aac_fixed,vorbis,opus,flac,pcm_s16le,pcm_s24le,pcm_s32le,pcm_f32le,mjpeg,png
    --enable-encoder=aac,flac,pcm_f32le,pcm_s24le
    --enable-parser=aac,mpegaudio,vorbis,opus,flac,png,mjpeg
    --enable-bsf=aac_adtstoasc
    --enable-filter=abuffer,abuffersink,aformat,aresample,anull,asetnsamples,ebur128,volume
)

OS="$(uname -s)"
case "$OS" in
  Darwin)
    export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-13.0}"
    CONFIGURE_ARGS+=(--extra-cflags="-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
      --extra-ldflags="-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET")
    ;;
  MSYS*|MINGW*|CYGWIN*) CONFIGURE_ARGS+=(--target-os=mingw32 --extra-ldflags=-static-libgcc);;
esac
"$FFMPEG_SRC/configure" "${CONFIGURE_ARGS[@]}"
LICENSE="$(sed -n 's/^#define FFMPEG_LICENSE "\(.*\)"$/\1/p' config.h)"
[[ "$LICENSE" == 'LGPL version 2.1 or later' ]] || {
  echo "ERROR: FFmpeg license is '$LICENSE', expected LGPL version 2.1 or later" >&2; exit 1;
}
make -j"$NPROC"
make install
mkdir -p "$PREFIX/share"
{
  echo "FFmpeg $FFMPEG_VERSION — $LICENSE (LGPL)"
  echo 'Linkage: static'
  echo "Source revision: $SOURCE_REVISION"
  echo "Source snapshot: $FFMPEG_SRC"
  echo "Source snapshot tar fingerprint (Git SHA-1): $SOURCE_FINGERPRINT"
  if [[ -s "$WORK/source-status.txt" ]]; then
    echo 'Original source status (tracked and untracked changes):'
    cat "$WORK/source-status.txt"
  fi
  echo 'The snapshot includes supplied source modifications; it is retained locally.'
  echo "Built on: $(uname -s) $(uname -m)"
  echo "Compiler: $(${CC:-cc} --version | head -1)"
  echo "Make: $(make --version | head -1)"
  echo "pkg-config: $(pkg-config --version)"
  echo 'Configure command:'
  printf '  ./configure'; printf ' %q' "${CONFIGURE_ARGS[@]}"; echo
  echo
  echo 'Rebuild with a compatible modified FFmpeg source and a NEW install prefix:'
  echo '  bash scripts/build-ffmpeg.sh --source-dir /path/to/FFmpeg --prefix /path/to/new-prefix'
  echo '  make FFMPEG_PREFIX=/path/to/new-prefix build build-gui'
  echo '  FFMPEG_PREFIX=/path/to/new-prefix bash scripts/test-book-audio.sh -race ./...'
  echo 'On macOS, locally sign the rebuilt app without maintainer credentials:'
  echo '  codesign --force --deep --sign - "dist/ChapterSmith.app"'
  echo 'See README.md for prerequisites, packaging and rebuild details.'
  echo 'Distribution also requires exact application/FFmpeg sources and dependencies;'
  echo 'this local build record alone is not a complete release source/relink bundle.'
} > "$PREFIX/share/BUILDING-FFMPEG.txt"
"$SCRIPT_DIR/check-static-ffmpeg.sh" "$PREFIX"
cp "$WORK/ffmpeg-source.tar.gz" "$PREFIX/share/ffmpeg-source.tar.gz"
python3 "$SCRIPT_DIR/source-release.py" record "$PREFIX" "$SOURCE_REVISION"
echo "=== Installed static FFmpeg in $PREFIX ==="
