#!/usr/bin/env bash
# verify-deps.sh BUNDLE_DIR FILE... — fail if any FILE loads a library that is
# neither part of the OS nor shipped in BUNDLE_DIR (e.g. Homebrew, MSYS2, the
# local vendor/ffmpeg build). Any FFmpeg dynamic import is rejected, including bundled/system FFmpeg.
# Allow-list: only OS library locations and BUNDLE_DIR are accepted.
set -euo pipefail
BUNDLE="${1:?usage: verify-deps.sh BUNDLE_DIR FILE...}"; shift
[[ $# -gt 0 ]] || { echo "verify-deps: no files given" >&2; exit 2; }
BUNDLE="$(cd "$BUNDLE" && pwd -P)"
errors=0
fail() { echo "verify-deps: $*" >&2; errors=$((errors + 1)); }
is_ffmpeg() { [[ "$(tr '[:upper:]' '[:lower:]' <<<"$1")" =~ (^|/)(lib)?(avcodec|avdevice|avfilter|avformat|swresample|swscale|avutil)[-.] ]]; }
# in_bundle DIR — true if DIR (a POSIX path) resolves to BUNDLE or below it.
in_bundle() {
  local d
  [[ -n "$1" ]] || return 1
  d="$(cd "$1" 2>/dev/null && pwd -P)" || return 1
  [[ "$d" == "$BUNDLE" || "$d" == "$BUNDLE"/* ]]
}
# resolved_path LINE — the library path from an ldd/ntldd line
# ("name => /path (0x...)" or "/path (0x...)"); empty if none.
resolved_path() {
  local line="$1"
  if [[ "$line" == *"=> "* ]]; then
    line="${line#*=> }"
  else
    line="${line#"${line%%[![:space:]]*}"}"
    [[ "$line" == /* ]] || return 0
  fi
  printf '%s' "${line% (0x*}"
}

for f in "$@"; do
  [[ -f "$f" ]] || { fail "$f: missing"; continue; }
  case "$(uname -s)" in
    Darwin)
      out="$(otool -L "$f" 2>&1)" || { fail "$f: cannot inspect: $out"; continue; }
      [[ "$out" == *"is not an object file"* ]] && { fail "$f: not a Mach-O file"; continue; }
      while read -r dep; do
        [[ -z "$dep" ]] && continue
        if is_ffmpeg "$dep"; then fail "$f: dynamic FFmpeg import $dep"; continue; fi
        case "$dep" in
          /usr/lib/*|/System/Library/*) ;;
          @rpath/*) [[ -f "$BUNDLE/${dep#@rpath/}" ]] || fail "$f: $dep is not in $BUNDLE" ;;
          *) fail "$f: non-system dependency $dep" ;;
        esac
      done < <(tail -n +2 <<<"$out" | awk '{print $1}')
      ;;
    Linux)
      out="$(env -u LD_LIBRARY_PATH ldd "$f" 2>&1)" || { fail "$f: cannot inspect: $out"; continue; }
      [[ "$out" == *"not a dynamic executable"* ]] && { fail "$f: not a dynamic executable"; continue; }
      while read -r line; do
        [[ -z "$line" ]] && continue
        [[ "$line" == *"not found"* ]] && { fail "$f: $line"; continue; }
        path="$(resolved_path "$line")"
        [[ -z "$path" ]] && continue          # e.g. linux-vdso.so.1 (no file)
        dir="$(dirname "$path")"
        if is_ffmpeg "$(basename "$path")"; then
          fail "$f: dynamic FFmpeg import $path"
          continue
        fi
        case "$path" in
          /lib/*|/lib64/*|/usr/lib/*|/usr/lib64/*) ;;
          *) in_bundle "$dir" || fail "$f: non-system dependency $path" ;;
        esac
      done <<<"$out"
      ;;
    MINGW*|MSYS*|CYGWIN*)
      # Callers pass every bundled DLL. Inspect their direct imports without
      # traversing optional dependencies inside Windows system libraries.
      out="$(MSYS2_ARG_CONV_EXCL='*' ntldd "$(cygpath -w "$f")" 2>&1)" || { fail "$f: cannot inspect: $out"; continue; }
      while read -r line; do
        [[ -z "$line" ]] && continue
        # API-set DLLs are virtual (resolved by the OS loader); ntldd may report them as missing.
        [[ "$(printf '%s' "$line" | tr '[:upper:]' '[:lower:]')" =~ ^[[:space:]]*(api|ext)-ms- ]] && continue
        [[ "$line" == *"not found"* ]] && { fail "$f: $line"; continue; }
        path="$(resolved_path "$line" | tr '\134' '/')"
        [[ -z "$path" ]] && continue
        if is_ffmpeg "$(basename "$path")"; then
          fail "$f: dynamic FFmpeg import $path"
          continue
        fi
        dir="$(cygpath -u "$(dirname "$path")" 2>/dev/null)" || dir=""
        case "$(printf '%s' "$path" | tr '[:upper:]' '[:lower:]')" in
          c:/windows/*) ;;
          *) in_bundle "$dir" || fail "$f: non-system dependency $path" ;;
        esac
      done <<<"$out"
      ;;
    *) fail "unsupported OS $(uname -s)" ;;
  esac
done

if [[ $errors -gt 0 ]]; then echo "verify-deps: $errors problem(s)" >&2; exit 1; fi
echo "verify-deps: OK ($# file(s), bundle $BUNDLE)"
