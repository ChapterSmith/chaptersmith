#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/scripts" "$WORK/dist" "$WORK/bin"
cp "$ROOT/scripts/package-windows.sh" "$WORK/scripts/"
touch "$WORK/dist/chaptersmith.exe"
for helper in collect-ffmpeg-libs stage-licenses verify-deps; do
  printf '#!/usr/bin/env bash\nexit 0\n' > "$WORK/scripts/$helper.sh"
  chmod +x "$WORK/scripts/$helper.sh"
done
cat > "$WORK/bin/makensis" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  case "$arg" in -DOUTFILE=*) output="${arg#-DOUTFILE=}" ;; esac
done
# NSIS resolves relative output paths from the installer script's directory.
cd scripts
touch "$output"
STUB
chmod +x "$WORK/bin/makensis"
cygpath() { printf '%s\n' "$2"; }
export -f cygpath
(
  cd "$WORK"
  OUT_DIR='dist/installer packages' VERSION=1.2.3 MAKENSIS="$WORK/bin/makensis" \
    bash scripts/package-windows.sh cli
)
test -f "$WORK/dist/installer packages/ChapterSmith-CLI-1.2.3-windows-x64-setup.exe"
echo 'test-package-windows: OK'
