#!/usr/bin/env bash
# Regression coverage: copied source preservation, static-only output, private
# pkg-config dependencies, prefix isolation, and rejecting dynamic FFmpeg imports.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
fail() { echo "test-static-ffmpeg: $*" >&2; exit 1; }
if [[ "${1:-}" != --deps ]]; then
mkdir -p "$WORK/source" "$WORK/prefix/lib" "$WORK/tools" "$WORK/bundle"
printf 'user modification\n' > "$WORK/source/modified.txt"
if command -v xattr >/dev/null; then xattr -w org.chaptersmith.fixture metadata "$WORK/source/modified.txt"; fi
cat > "$WORK/source/configure" <<'CONFIGURE'
#!/usr/bin/env bash
set -eu
static=false; shared=true
for arg in "$@"; do
  case "$arg" in --prefix=*) prefix="${arg#*=}";; --enable-static) static=true;; --disable-shared) shared=false;; esac
done
[[ "$static" == true && "$shared" == false ]] || { echo 'expected static-only configure' >&2; exit 1; }
printf '#define FFMPEG_LICENSE "LGPL version 2.1 or later"\n' > config.h
cat > Makefile <<MAKE
all:
	@test -f config.h
install:
	@mkdir -p '$prefix/lib/pkgconfig' '$prefix/include'
MAKE
for lib in avcodec avdevice avfilter avformat avutil swresample swscale; do
  cat >> Makefile <<MAKE
	@printf archive > '$prefix/lib/lib$lib.a'
	@printf 'prefix=$prefix\nName: lib$lib\nDescription: regression fixture\nVersion: 8.0\nLibs: -L$prefix/lib -l$lib\nLibs.private: -lz\nCflags: -I$prefix/include\n' > '$prefix/lib/pkgconfig/lib$lib.pc'
MAKE
done
CONFIGURE
chmod +x "$WORK/source/configure"
printf stale > "$WORK/prefix/lib/libavcodec.dylib"
# Refuse an existing shared prefix, without changing either input or prefix.
if bash "$ROOT/scripts/build-ffmpeg.sh" --source-dir "$WORK/source" --prefix "$WORK/prefix" >"$WORK/log" 2>&1; then
  fail 'accepted a stale shared prefix'
fi
grep -qi 'shared.*artifact' "$WORK/log" || { cat "$WORK/log"; fail 'did not diagnose stale shared artifacts'; }
[[ "$(cat "$WORK/prefix/lib/libavcodec.dylib")" == stale ]] || fail 'mutated rejected prefix'
rm "$WORK/prefix/lib/libavcodec.dylib"
bash "$ROOT/scripts/build-ffmpeg.sh" --source-dir "$WORK/source" --prefix "$WORK/prefix" >"$WORK/log" 2>&1 || { cat "$WORK/log"; fail 'static source build failed'; }
[[ "$(cat "$WORK/source/modified.txt")" == 'user modification' ]] || fail 'modified source changes'
[[ ! -e "$WORK/source/config.h" && ! -e "$WORK/source/Makefile" ]] || fail 'configured in user source'
[[ -f "$WORK/prefix/lib/libavcodec.a" ]] || fail 'static archive not installed'
python3 - "$WORK/prefix/share/ffmpeg-source.tar.gz" <<'PYTEST'
import sys, tarfile
with tarfile.open(sys.argv[1]) as archive:
    assert not any(part.startswith('._') for member in archive for part in member.name.split('/')), 'source archive leaked AppleDouble host metadata'
PYTEST
FFMPEG_PREFIX="$WORK/prefix" bash "$ROOT/scripts/ffmpeg-pkg-config.sh" --libs libavcodec > "$WORK/libs"
grep -q -- '-lz' "$WORK/libs" || fail 'lost Libs.private dependency'
expected_prefix="$WORK/prefix"
case "$(uname -s)" in MSYS*|MINGW*|CYGWIN*) expected_prefix="$(cygpath -m "$expected_prefix")";; esac
grep -Fq "$expected_prefix/lib/libavcodec.a" "$WORK/libs" || fail 'did not bind exact archive'
mv "$WORK/prefix/lib/libavcodec.a" "$WORK/prefix/lib/missing.a"
if FFMPEG_PREFIX="$WORK/prefix" bash "$ROOT/scripts/ffmpeg-pkg-config.sh" --libs libavcodec >"$WORK/log" 2>&1; then fail 'allowed missing archive fallback'; fi
mv "$WORK/prefix/lib/missing.a" "$WORK/prefix/lib/libavcodec.a"
# Native Windows pkg-config derives prefix from the .pc file's own location and
# ignores the recorded one, so a foreign prefix cannot be expressed there.
case "$(uname -s)" in
  MSYS*|MINGW*|CYGWIN*) ;;
  *)
    printf '\nprefix=/wrong/tree\n' >> "$WORK/prefix/lib/pkgconfig/libavcodec.pc"
    if FFMPEG_PREFIX="$WORK/prefix" bash "$ROOT/scripts/ffmpeg-pkg-config.sh" --libs libavcodec >"$WORK/log" 2>&1; then fail 'accepted foreign prefix'; fi
    ;;
esac
fi
mkdir -p "$WORK/tools" "$WORK/bundle"
# Static flags must never leak into unrelated Wails/system package queries.
mkdir -p "$WORK/gui-pc"
cat > "$WORK/gui-pc/gtk+-3.0.pc" <<'PC'
Name: GTK fixture
Description: GUI dependency forwarding regression
Version: 3.0
Libs: -lgtk-3
Libs.private: -lmust_not_link_statically
PC
PKG_CONFIG_PATH="$WORK/gui-pc" FFMPEG_PREFIX="$WORK/absent" bash "$ROOT/scripts/ffmpeg-pkg-config.sh" --libs gtk+-3.0 > "$WORK/gui-libs"
[[ "$(cat "$WORK/gui-libs")" == '-lgtk-3 ' || "$(cat "$WORK/gui-libs")" == '-lgtk-3' ]] || fail 'changed system GUI linkage'
# OS inspection tools are external; use representative import tables so these
# rejection branches are testable on every host, including OS/bundled imports.
printf executable > "$WORK/bundle/app"
printf shared > "$WORK/bundle/libavcodec.62.dylib"
cat > "$WORK/tools/uname" <<'TOOL'
#!/bin/sh
echo "$TEST_OS"
TOOL
cat > "$WORK/tools/otool" <<'TOOL'
#!/bin/sh
printf '%s:\n\t%s (compatibility version 62.0.0, current version 62.0.0)\n' "$2" "$TEST_IMPORT"
TOOL
cat > "$WORK/tools/ldd" <<'TOOL'
#!/bin/sh
printf 'libavcodec.so.62 => %s (0x1234)\n' "$TEST_IMPORT"
TOOL
cat > "$WORK/tools/cygpath" <<'TOOL'
#!/bin/sh
printf '%s\n' "$2"
TOOL
cat > "$WORK/tools/ntldd" <<'TOOL'
#!/bin/sh
printf 'avcodec-62.dll => %s (0x1234)\n' "$TEST_IMPORT"
TOOL
chmod +x "$WORK/tools/"*
for os in Darwin Linux MINGW64_NT; do
  case "$os" in Darwin) dep='@rpath/libavcodec.62.dylib';; Linux) dep='/usr/lib/libavcodec.so.62';; *) dep='C:/Windows/System32/avcodec-62.dll';; esac
  if PATH="$WORK/tools:$PATH" TEST_OS="$os" TEST_IMPORT="$dep" bash "$ROOT/scripts/verify-deps.sh" "$WORK/bundle" "$WORK/bundle/app" >"$WORK/log" 2>&1; then fail "accepted $os dynamic FFmpeg import"; fi
  grep -q 'dynamic FFmpeg' "$WORK/log" || { cat "$WORK/log"; fail "wrong $os rejection"; }
done
echo 'test-static-ffmpeg: OK'
