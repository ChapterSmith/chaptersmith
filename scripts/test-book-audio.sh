#!/usr/bin/env bash
# Compile tests against only the selected static LGPL FFmpeg prefix.
set -euo pipefail
cd "$(dirname "$0")/.."
export FFMPEG_PREFIX="${FFMPEG_PREFIX:-$PWD/vendor/ffmpeg}"
export GOFLAGS=-mod=mod CGO_ENABLED=1
bash scripts/prepare-static-ffmpeg.sh
wrapper="$PWD/vendor/ffmpeg-tools/pkg-config"
case "$(uname -s)" in
  MSYS*|MINGW*|CYGWIN*)
    FFMPEG_PREFIX="$(cygpath -m "$FFMPEG_PREFIX")"
    wrapper="$(cygpath -m "$wrapper.exe")"
    ;;
  Darwin) export MACOSX_DEPLOYMENT_TARGET=13.0;;
  Linux)
    # Match make test: Wails defaults to GTK4, while our Linux build uses GTK3.
    # Merge caller tags and retain all package/test arguments, including -args.
    tags=gtk3
    args=(-tags=gtk3)
    while [[ $# -gt 0 ]]; do
      case "$1" in
        -tags|--tags)
          [[ $# -gt 1 ]] || { echo 'test-book-audio: -tags needs a value' >&2; exit 2; }
          tags+=",$2"; shift 2;;
        -tags=*|--tags=*) tags+=",${1#*=}"; shift;;
        -args|--args) args+=("$@"); break;;
        *) args+=("$1"); shift;;
      esac
    done
    args[0]="-tags=$tags"
    set -- "${args[@]}"
    ;;
esac
export PKG_CONFIG="\"$wrapper\""
export PKG_CONFIG_PATH="$FFMPEG_PREFIX/lib/pkgconfig"
export CGO_CFLAGS="-I$FFMPEG_PREFIX/include"
export CGO_LDFLAGS=""
if [[ "$(uname -s)" == Darwin ]]; then
  export CGO_CFLAGS="$CGO_CFLAGS -mmacosx-version-min=13.0"
fi
exec go test "$@"
