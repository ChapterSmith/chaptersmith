#!/usr/bin/env bash
# smoke-windows.sh OUT_DIR — install both packages silently (CLI twice, into a
# directory with a space), check PATH handling, dependencies, run one merge,
# uninstall. Run in MSYS2 UCRT64 on a CI runner.
set -euo pipefail
OUT_DIR="${1:?usage: smoke-windows.sh OUT_DIR}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Select this release explicitly; never test an arbitrary stale installer.
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
shopt -s nullglob
if [[ -n "${VERSION:-}" ]]; then
  CLI_SETUP="$OUT_DIR/ChapterSmith-CLI-$VERSION-windows-x64-setup.exe"
  GUI_SETUP="$OUT_DIR/ChapterSmith-$VERSION-windows-x64-setup.exe"
  [[ -f "$CLI_SETUP" ]] || { echo "smoke-windows: missing CLI installer for VERSION=$VERSION" >&2; exit 1; }
  [[ -f "$GUI_SETUP" ]] || { echo "smoke-windows: missing matching GUI installer for VERSION=$VERSION" >&2; exit 1; }
else
  cli_installers=("$OUT_DIR"/ChapterSmith-CLI-*-windows-x64-setup.exe)
  [[ ${#cli_installers[@]} -eq 1 ]] || {
    echo 'smoke-windows: expected one CLI installer; set VERSION to select a release' >&2; exit 1;
  }
  CLI_SETUP="${cli_installers[0]}"
  CLI_NAME="${CLI_SETUP##*/}"
  GUI_SETUP="$OUT_DIR/ChapterSmith-${CLI_NAME#ChapterSmith-CLI-}"
  [[ -f "$GUI_SETUP" ]] || { echo 'smoke-windows: missing matching GUI installer' >&2; exit 1; }
  gui_installers=()
  for installer in "$OUT_DIR"/ChapterSmith-*-windows-x64-setup.exe; do
    [[ "${installer##*/}" == ChapterSmith-CLI-* ]] || gui_installers+=("$installer")
  done
  [[ ${#gui_installers[@]} -eq 1 ]] || {
    echo 'smoke-windows: expected one GUI installer; set VERSION to select a release' >&2; exit 1;
  }
fi
LOCALAPPDATA_U="$(cygpath -u "$LOCALAPPDATA")"
CLI_DIR_W="$LOCALAPPDATA\\Programs\\ChapterSmith CLI Test's"
CLI_DIR="$(cygpath -u "$CLI_DIR_W")"

run_wait() { # run_wait EXE ARG... — start a Windows program and wait for it
  local exe; exe="$(cygpath -w "$1")"; shift
  # One raw argument string: NSIS requires /D= and _?= last and unquoted,
  # including when their directory contains spaces. Pass data through the
  # environment so apostrophes or PowerShell syntax cannot become script code.
  SMOKE_EXE="$exe" SMOKE_ARGS="$*" MSYS2_ENV_CONV_EXCL='SMOKE_EXE;SMOKE_ARGS' \
    powershell -NoProfile -Command '$ErrorActionPreference = "Stop"; try { $p = Start-Process -FilePath $env:SMOKE_EXE -ArgumentList $env:SMOKE_ARGS -Wait -PassThru; exit $p.ExitCode } catch { Write-Error $_ -ErrorAction Continue; exit 1 }'
}
user_path() { powershell -NoProfile -Command '$k = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment"); try { $k.GetValue("Path", "", [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) } finally { $k.Dispose() }'; }
path_count() { user_path | tr ';' '\n' | tr -d '\r' | grep -Fxc "$CLI_DIR_W" || true; }

powershell -NoProfile -ExecutionPolicy Bypass -File "$(cygpath -w "$ROOT/scripts/test-windows-path.ps1")"

run_wait "$GUI_SETUP" /S
GUI_DIR="$LOCALAPPDATA_U/Programs/ChapterSmith"
"$ROOT/scripts/check-static-ffmpeg.sh" --no-shared "$GUI_DIR"
"$ROOT/scripts/verify-deps.sh" "$GUI_DIR" "$GUI_DIR/chaptersmith-gui.exe" "$GUI_DIR"/*.dll
[[ -f "$GUI_DIR/licenses/BUILDING-FFMPEG.txt" ]]
# A dependency scan cannot detect a missing/unusable WebView2 runtime. Start the
# packaged GUI, require a live window, and stop its process tree even on failure.
SMOKE_GUI="$(cygpath -w "$GUI_DIR/chaptersmith-gui.exe")" MSYS2_ENV_CONV_EXCL='SMOKE_GUI' \
  powershell -NoProfile -Command '
    $ErrorActionPreference = "Stop"
    $p = $null
    $logPrefix = Join-Path $env:TEMP "chaptersmith-gui-smoke"
    try {
      $p = Start-Process -FilePath $env:SMOKE_GUI -PassThru -RedirectStandardOutput "$logPrefix.out" -RedirectStandardError "$logPrefix.err"
      $deadline = [DateTime]::UtcNow.AddSeconds(30)
      do {
        $p.Refresh()
        if ($p.HasExited) { throw "GUI exited during startup with code $($p.ExitCode)" }
        if ($p.MainWindowHandle -ne 0) { break }
        Start-Sleep -Milliseconds 500
      } while ([DateTime]::UtcNow -lt $deadline)
      if ($p.MainWindowHandle -eq 0) { throw "GUI did not create a window within 30 seconds" }
    } catch {
      Get-Content "$logPrefix.out", "$logPrefix.err" -ErrorAction SilentlyContinue
      throw
    } finally {
      if ($null -ne $p -and -not $p.HasExited) {
        & "$env:SystemRoot\System32\taskkill.exe" /PID $p.Id /T /F | Out-Null
      }
    }'


# /D= must be the last argument and is not quoted (NSIS rule).
run_wait "$CLI_SETUP" /S "/D=$CLI_DIR_W"
run_wait "$CLI_SETUP" /S "/D=$CLI_DIR_W"
[[ "$(path_count)" == "1" ]] || { echo "CLI dir is in user PATH $(path_count) times, want 1" >&2; exit 1; }
"$ROOT/scripts/check-static-ffmpeg.sh" --no-shared "$CLI_DIR"
"$ROOT/scripts/verify-deps.sh" "$CLI_DIR" "$CLI_DIR/chaptersmith.exe" "$CLI_DIR"/*.dll
"$CLI_DIR/chaptersmith.exe" -version | grep -q "LGPLv2.1"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cp "$ROOT"/testdata/formats/mp3/0{1,2}.mp3 "$ROOT/testdata/formats/cover.jpg" "$WORK/"
(cd "$WORK" && "$CLI_DIR/chaptersmith.exe" -o out.m4b </dev/null)
[[ -s "$WORK/out.m4b" ]]

run_wait "$CLI_DIR/uninstall.exe" /S "_?=$CLI_DIR_W"
[[ "$(path_count)" == "0" ]] || { echo "uninstall left CLI dir in user PATH" >&2; exit 1; }
[[ ! -f "$CLI_DIR/chaptersmith.exe" ]] || { echo "CLI uninstall left executable" >&2; exit 1; }
run_wait "$GUI_DIR/uninstall.exe" /S "_?=$(cygpath -w "$GUI_DIR")"
[[ ! -f "$GUI_DIR/chaptersmith-gui.exe" ]] || { echo "GUI uninstall left executable" >&2; exit 1; }
echo "smoke-windows: OK"
