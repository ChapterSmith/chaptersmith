#!/usr/bin/env bash
# smoke-linux.sh OUT_DIR [all|cli] — inspect native AppImages and merge fixtures
# with the CLI without FUSE. VERSION selects a release in a shared output folder;
# without VERSION exactly one matching release must be present.
set -euo pipefail
OUT_DIR="${1:?usage: smoke-linux.sh OUT_DIR [all|cli]}"
KIND="${2:-all}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$KIND" == all || "$KIND" == cli ]] || { echo "smoke-linux: expected all or cli" >&2; exit 2; }
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
case "$(uname -m)" in
  x86_64) ARCH=x86_64 ;;
  aarch64|arm64) ARCH=aarch64 ;;
  *) echo "smoke-linux: unsupported architecture" >&2; exit 1 ;;
esac
export APPIMAGE_EXTRACT_AND_RUN=1

# Never silently select an older release or an AppImage for another architecture.
shopt -s nullglob
if [[ -n "${VERSION:-}" ]]; then
  CLI="$OUT_DIR/ChapterSmith-CLI-$VERSION-linux-$ARCH.AppImage"
else
  candidates=("$OUT_DIR"/ChapterSmith-CLI-*-linux-"$ARCH".AppImage)
  [[ ${#candidates[@]} -eq 1 ]] || {
    echo "smoke-linux: expected one native CLI AppImage; set VERSION to select a release" >&2
    exit 1
  }
  CLI="${candidates[0]}"
fi
[[ -f "$CLI" && -x "$CLI" ]] || { echo "smoke-linux: missing executable $CLI" >&2; exit 1; }
GUI="$OUT_DIR/ChapterSmith-${CLI##*/ChapterSmith-CLI-}"
images=("$CLI")
if [[ "$KIND" == all ]]; then
  [[ -f "$GUI" && -x "$GUI" ]] || { echo "smoke-linux: missing matching GUI AppImage $GUI" >&2; exit 1; }
  images+=("$GUI")
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for img in "${images[@]}"; do
  x="$WORK/$(basename "$img")"
  mkdir -p "$x"
  (cd "$x" && env -u APPIMAGE_EXTRACT_AND_RUN "$img" --appimage-extract >/dev/null)
  bins=("$x"/squashfs-root/usr/bin/*)
  [[ ${#bins[@]} -eq 1 && -f "${bins[0]}" ]] || { echo "smoke-linux: expected one packaged binary" >&2; exit 1; }
  "$ROOT/scripts/check-static-ffmpeg.sh" --no-shared "$x/squashfs-root/usr"
  libs=("$x"/squashfs-root/usr/lib/*.so*)
  "$ROOT/scripts/verify-deps.sh" "$x/squashfs-root/usr/lib" "${bins[0]}" "${libs[@]}"
  [[ -s "$x/squashfs-root/usr/share/licenses/chaptersmith/BUILDING-FFMPEG.txt" ]]
done

version_output="$("$CLI" -version)"
[[ "$version_output" == *LGPLv2.1* ]] || { echo "smoke-linux: CLI version lacks LGPL notice" >&2; exit 1; }
mkdir -p "$WORK/merge"
cp "$ROOT"/testdata/formats/mp3/0{1,2}.mp3 "$ROOT/testdata/formats/cover.jpg" "$WORK/merge/"
(cd "$WORK/merge" && "$CLI" -o out.m4b </dev/null)
[[ -s "$WORK/merge/out.m4b" ]]
echo "smoke-linux: OK ($KIND, $ARCH)"
