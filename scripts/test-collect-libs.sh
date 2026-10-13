#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/scripts" "$WORK/bundle Test's" "$WORK/ucrt64/bin"
cp "$ROOT/scripts/collect-ffmpeg-libs.sh" "$WORK/scripts/"
printf '#!/usr/bin/env bash\nexit 0\n' > "$WORK/scripts/check-static-ffmpeg.sh"
chmod +x "$WORK/scripts/check-static-ffmpeg.sh"
touch "$WORK/bundle Test's/chaptersmith.exe" "$WORK/ucrt64/bin/zlib1.dll" "$WORK/ucrt64/bin/libwinpthread-1.dll"
export COLLECT_TEST_WORK="$WORK"
uname() { echo MINGW64_NT; }
cygpath() {
  case "$1" in -w) printf 'C:%s\n' "$2";; -u) printf '%s\n' "${2#C:}";; esac
}
ntldd() {
  [[ "$1" == C:* && "${MSYS2_ARG_CONV_EXCL:-}" == '*' ]] || { echo 'expected native path, direct imports only' >&2; return 1; }
  printf '\tkernel32.dll => C:/Windows/System32/kernel32.dll (0x1)\n'
  case "$1" in
    *.exe) printf '\tzlib1.dll => C:%s/ucrt64/bin/zlib1.dll (0x2)\n' "$COLLECT_TEST_WORK" ;;
    *zlib1.dll) printf '\tlibwinpthread-1.dll => C:%s/ucrt64/bin/libwinpthread-1.dll (0x3)\n' "$COLLECT_TEST_WORK" ;;
  esac
}
export -f uname cygpath ntldd
bash "$WORK/scripts/collect-ffmpeg-libs.sh" "$WORK/prefix" "$WORK/bundle Test's"
for lib in zlib1.dll libwinpthread-1.dll; do
  [[ -f "$WORK/bundle Test's/$lib" ]] || { echo "missing transitive dependency $lib" >&2; exit 1; }
done
echo 'test-collect-libs: OK'
