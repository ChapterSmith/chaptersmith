#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bundle Test's" "$WORK/external"
touch "$WORK/bundle Test's/app.exe" "$WORK/bundle Test's/zlib1.dll"
export VERIFY_TEST_BUNDLE="$WORK/bundle Test's"
export VERIFY_TEST_EXTERNAL="$WORK/external"

uname() { echo MINGW64_NT; }
cygpath() {
  case "$1" in
    -w) printf 'C:%s\n' "$2" ;;
    -u) printf '%s\n' "${2#C:}" ;;
  esac
}
ntldd() {
  if [[ "$1" != -R && "$1" != C:* ]]; then
    printf '%s: not found\n' "$1"
    return
  fi
  printf '\tkernel32.dll => C:/Windows/System32/kernel32.dll (0x1)\n'
  printf '\tapi-ms-win-core-synch-l1-2-0.dll => not found\n'
  if [[ "$1" == -R ]]; then
    printf '\t  AzureAttestNormal.dll => not found\n'
  fi
  case "$VERIFY_TEST_CASE" in
    valid) printf '\tzlib1.dll => C:%s/zlib1.dll (0x2)\n' "$VERIFY_TEST_BUNDLE" ;;
    missing) printf '\tzlib1.dll => not found\n' ;;
    external) printf '\tzlib1.dll => C:%s/zlib1.dll (0x2)\n' "$VERIFY_TEST_EXTERNAL" ;;
  esac
}
export -f uname cygpath ntldd

VERIFY_TEST_CASE=valid "$BASH" "$ROOT/scripts/verify-deps.sh" "$WORK/bundle Test's" "$WORK/bundle Test's/app.exe" "$WORK/bundle Test's/zlib1.dll"
for scenario in missing external; do
  if VERIFY_TEST_CASE="$scenario" "$BASH" "$ROOT/scripts/verify-deps.sh" "$WORK/bundle Test's" "$WORK/bundle Test's/app.exe" > "$WORK/log" 2>&1; then
    echo "verify-deps accepted $scenario application dependency" >&2
    exit 1
  fi
done
echo 'test-verify-deps: OK'
