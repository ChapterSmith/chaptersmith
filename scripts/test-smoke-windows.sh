#!/usr/bin/env bash
# Host-compatible checks: stop at the first platform call after artifact selection.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/CLI packages"
OUT="$WORK/CLI packages"
cygpath() { echo 'selection reached platform tools' >&2; exit 73; }
export -f cygpath
expect_error() {
  local expected="$1"; shift
  if env -u VERSION LOCALAPPDATA='C:\Test' "$@" bash "$ROOT/scripts/smoke-windows.sh" "$OUT" >"$WORK/log" 2>&1; then
    echo "expected error: $expected" >&2; exit 1
  fi
  grep -Fq "$expected" "$WORK/log" || { cat "$WORK/log" >&2; exit 1; }
  if grep -Fq 'selection reached platform tools' "$WORK/log"; then echo 'invalid artifacts reached platform tools' >&2; exit 1; fi
}
expect_selection() {
  local status=0
  env -u VERSION LOCALAPPDATA='C:\Test' "$@" bash "$ROOT/scripts/smoke-windows.sh" "$OUT" >"$WORK/log" 2>&1 || status=$?
  if [[ $status != 73 ]] || ! grep -Fq 'selection reached platform tools' "$WORK/log"; then
    cat "$WORK/log" >&2; echo 'valid artifacts failed selection' >&2; exit 1
  fi
}
expect_error 'expected one CLI installer'
touch "$OUT/ChapterSmith-CLI-1.0-windows-x64-setup.exe"
expect_error 'missing matching GUI installer'
touch "$OUT/ChapterSmith-2.0-windows-x64-setup.exe"
expect_error 'missing matching GUI installer'
touch "$OUT/ChapterSmith-1.0-windows-x64-setup.exe"
expect_error 'expected one GUI installer'
expect_error 'missing CLI installer' VERSION=9.0
expect_selection VERSION=1.0
# Remove only this fixture file, then require the unique matching pair.
unlink "$OUT/ChapterSmith-2.0-windows-x64-setup.exe"
expect_selection
touch "$OUT/ChapterSmith-CLI-2.0-windows-x64-setup.exe"
expect_error 'expected one CLI installer'
expect_selection VERSION=1.0
echo 'test-smoke-windows: OK'
