#!/usr/bin/env bash
# Native Ubuntu 22.04 build + AppImage smoke test. Docker is required. The source
# is read-only and all Linux build outputs live in a disposable named volume.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VOLUME="abm-lgpl-linux-test-$$"
CONTAINER="$VOLUME"
cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  docker volume rm "$VOLUME" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker volume create "$VOLUME" >/dev/null
docker run --rm -i --name "$CONTAINER" -v "$ROOT:/src:ro" -v "$VOLUME:/work" ubuntu:22.04 bash -es <<'LINUX'
set -euo pipefail
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
  git make gcc pkg-config nasm patchelf zlib1g-dev curl file binutils \
  ca-certificates rsync libgtk-3-dev libwebkit2gtk-4.1-dev
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) GOARCH=amd64; NODEARCH=x64 ;;
  aarch64) GOARCH=arm64; NODEARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
curl -fsSL "https://go.dev/dl/go1.27.2.linux-$GOARCH.tar.gz" | tar -C /usr/local -xz
mkdir -p /usr/local/node
curl -fsSL "https://nodejs.org/dist/v22.21.1/node-v22.21.1-linux-$NODEARCH.tar.gz" | \
  tar -C /usr/local/node --strip-components=1 -xz
export PATH=/usr/local/go/bin:/usr/local/node/bin:$PATH GOFLAGS=-mod=mod
curl -fsSL -o /usr/local/bin/appimagetool \
  "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-$ARCH.AppImage"
chmod +x /usr/local/bin/appimagetool
# Exclude generated assets so this also verifies a clean checkout.
rsync -a --exclude vendor --exclude dist --exclude .git \
  --exclude frontend/node_modules --exclude cmd/chaptersmith-gui/embed /src/ /work/src/
cd /work/src
./scripts/build-ffmpeg.sh --prefix=/work/src/vendor/ffmpeg
export CGO_ENABLED=1 CGO_CFLAGS=-I/work/src/vendor/ffmpeg/include
export PKG_CONFIG_PATH=/work/src/vendor/ffmpeg/lib/pkgconfig
export CGO_LDFLAGS='-Wl,-rpath,$ORIGIN/lib -Wl,-rpath,$ORIGIN/../lib'
go version
mkdir -p dist
go build -o dist/chaptersmith ./cmd/chaptersmith
(cd frontend && npm ci && npm run build)
mkdir -p cmd/chaptersmith-gui/embed
cp -R frontend/dist/. cmd/chaptersmith-gui/embed/
go build -tags gtk3 -o dist/chaptersmith-gui ./cmd/chaptersmith-gui
CGO_LDFLAGS="$CGO_LDFLAGS -Wl,-rpath,/work/src/vendor/ffmpeg/lib" \
  go test -count=1 -v ./internal/app
export APPIMAGE_EXTRACT_AND_RUN=1 VERSION=v0.0.0-docker OUT_DIR='dist/packages with spaces'
./scripts/package-linux.sh cli
./scripts/package-linux.sh gui
./scripts/smoke-linux.sh "$OUT_DIR"
./scripts/smoke-linux.sh "$OUT_DIR" cli
unset VERSION
./scripts/smoke-linux.sh "$OUT_DIR"
LINUX
