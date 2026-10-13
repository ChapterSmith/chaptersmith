#!/usr/bin/env bash
# package-macos.sh gui|cli — build the GUI .dmg or the CLI .pkg from dist/.
# Signs with MACOS_SIGN_IDENTITY / MACOS_INSTALLER_IDENTITY and notarizes with
# APPLE_API_KEY_PATH/APPLE_API_KEY_ID/APPLE_API_ISSUER_ID when set; otherwise
# produces an ad-hoc signed app / unsigned installer (error when REQUIRE_SIGNING=true).
set -euo pipefail
KIND="${1:?usage: package-macos.sh gui|cli}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
OUT_DIR="${OUT_DIR:-$DIST_DIR/packages}"
FFMPEG_PREFIX="${FFMPEG_PREFIX:-$ROOT/vendor/ffmpeg}"
export FFMPEG_PREFIX
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
SIGN_IDENTITY="${MACOS_SIGN_IDENTITY:-}"
INSTALLER_IDENTITY="${MACOS_INSTALLER_IDENTITY:-}"
REQUIRE_SIGNING="${REQUIRE_SIGNING:-false}"
case "$(uname -m)" in arm64) ARCH=arm64 ;; x86_64) ARCH=x64 ;; *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; esac
case "$KIND" in gui|cli) ;; *) echo "usage: package-macos.sh gui|cli" >&2; exit 1 ;; esac

require_or_warn() { # require_or_warn VALUE MESSAGE
  if [[ -z "$1" ]]; then
    if [[ "$REQUIRE_SIGNING" == "true" ]]; then echo "ERROR: REQUIRE_SIGNING=true but $2" >&2; exit 1; fi
    echo "WARNING: $2 — producing an unsigned/un-notarized package" >&2
  fi
}
require_or_warn "$SIGN_IDENTITY" "MACOS_SIGN_IDENTITY is not set"
if [[ "$REQUIRE_SIGNING" == true && "$SIGN_IDENTITY" == - ]]; then
  echo "ERROR: REQUIRE_SIGNING=true does not allow ad-hoc signing" >&2; exit 1
fi
require_or_warn "${APPLE_API_KEY_PATH:-}" "notarization credentials (APPLE_API_KEY_PATH) are not set"
if [[ "$KIND" == cli ]]; then require_or_warn "$INSTALLER_IDENTITY" "MACOS_INSTALLER_IDENTITY is not set"; fi
# Partial credentials must fail before building or overwriting an artifact.
if [[ -n "${APPLE_API_KEY_PATH:-}${APPLE_API_KEY_ID:-}${APPLE_API_ISSUER_ID:-}" ]]; then
  for credential in APPLE_API_KEY_PATH APPLE_API_KEY_ID APPLE_API_ISSUER_ID; do
    if [[ -z "${!credential:-}" ]]; then
      echo "ERROR: $credential is not set" >&2; exit 1
    fi
  done
  [[ -r "$APPLE_API_KEY_PATH" ]] || { echo "ERROR: notarization key is not readable" >&2; exit 1; }
  [[ -n "$SIGN_IDENTITY" && "$SIGN_IDENTITY" != - ]] || {
    echo "ERROR: notarization requires MACOS_SIGN_IDENTITY" >&2; exit 1;
  }
  if [[ "$KIND" == cli && -z "$INSTALLER_IDENTITY" ]]; then
    echo "ERROR: notarization requires MACOS_INSTALLER_IDENTITY" >&2; exit 1
  fi
fi

sign() { # sign FILE... — Developer ID + hardened runtime, or ad-hoc
  if [[ -n "$SIGN_IDENTITY" ]]; then
    codesign -f -s "$SIGN_IDENTITY" --options runtime --timestamp "$@"
  else
    codesign -f -s - "$@"
  fi
}

notarize() { # notarize FILE — submit, wait, check status, staple
  [[ -n "${APPLE_API_KEY_PATH:-}" ]] || return 0
  local json; json="$WORK/notarization.json"
  xcrun notarytool submit "$1" --key "$APPLE_API_KEY_PATH" --key-id "$APPLE_API_KEY_ID" \
    --issuer "$APPLE_API_ISSUER_ID" --wait --output-format json > "$json"
  local status; status="$(plutil -extract status raw -o - "$json")"
  if [[ "$status" != "Accepted" ]]; then
    echo "ERROR: notarization of $1 finished with status '$status'" >&2; cat "$json" >&2; exit 1
  fi
  xcrun stapler staple "$1"
}

WORK="$(mktemp -d)"
DMG_MOUNT=""
cleanup() {
  if [[ -n "$DMG_MOUNT" ]]; then
    hdiutil detach "$DMG_MOUNT" >/dev/null || return
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT
mkdir -p "$OUT_DIR"

if [[ "$KIND" == gui ]]; then
  APP="$WORK/ChapterSmith.app"
  mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
  cp "$DIST_DIR/chaptersmith-gui" "$APP/Contents/MacOS/chaptersmith-gui"
  cp "$ROOT/build/darwin/icon.icns" "$APP/Contents/Resources/icon.icns"
  sed "s/__VERSION__/${VERSION#v}/g" "$ROOT/build/darwin/Info.plist" > "$APP/Contents/Info.plist"
  "$ROOT/scripts/check-static-ffmpeg.sh" "$FFMPEG_PREFIX"
  "$ROOT/scripts/stage-licenses.sh" "$APP/Contents/Resources/licenses"
  "$ROOT/scripts/verify-deps.sh" "$APP/Contents/MacOS" "$APP/Contents/MacOS/chaptersmith-gui"
  sign "$APP/Contents/MacOS/chaptersmith-gui"
  sign "$APP"
  codesign --verify --deep --strict --verbose=2 "$APP"

  STAGING="$WORK/dmg"; mkdir -p "$STAGING"
  mv "$APP" "$STAGING/"
  chmod -R u+w "$STAGING"; xattr -cr "$STAGING"
  ln -s /Applications "$STAGING/Applications"
  cp "$ROOT/build/darwin/dmg-layout.dsstore" "$STAGING/.DS_Store"
  OUT="$OUT_DIR/ChapterSmith-$VERSION-macos-$ARCH.dmg"
  rm -f "$OUT"
  # HFS+ stores Finder's auto-open folder; APFS images only mounted silently.
  hdiutil makehybrid -hfs -hfs-volume-name "ChapterSmith" \
    -hfs-openfolder "$STAGING" -o "$WORK/install.dmg" "$STAGING"
  hdiutil convert "$WORK/install.dmg" -format UDRW -o "$WORK/writable.dmg"
  mkdir -p "$WORK/mount"
  hdiutil attach "$WORK/writable.dmg" -nobrowse -noautoopen -mountpoint "$WORK/mount"
  DMG_MOUNT="$WORK/mount"
  # makehybrid adds FinderInfo to bundle resources, invalidating code signatures.
  xattr -cr "$DMG_MOUNT/ChapterSmith.app"
  codesign --verify --deep --strict --verbose=2 "$DMG_MOUNT/ChapterSmith.app"
  hdiutil detach "$DMG_MOUNT"
  DMG_MOUNT=""
  hdiutil convert "$WORK/writable.dmg" -format UDZO -o "$OUT"
  if [[ -n "$SIGN_IDENTITY" ]]; then codesign -f -s "$SIGN_IDENTITY" --timestamp "$OUT"; fi
  notarize "$OUT"
else
  PAYLOAD="$WORK/root"
  LIBDIR="$PAYLOAD/usr/local/lib/chaptersmith"
  mkdir -p "$LIBDIR" "$PAYLOAD/usr/local/bin" "$WORK/scripts"
  cp "$DIST_DIR/chaptersmith" "$LIBDIR/chaptersmith"
  "$ROOT/scripts/collect-ffmpeg-libs.sh" "$FFMPEG_PREFIX" "$LIBDIR"
  "$ROOT/scripts/stage-licenses.sh" "$LIBDIR/licenses"
  "$ROOT/scripts/verify-deps.sh" "$LIBDIR" "$LIBDIR/chaptersmith"
  sign "$LIBDIR/chaptersmith"

  # Preserve the CLI installation path across upgrades.
  cat > "$PAYLOAD/usr/local/bin/chaptersmith" <<'EOF'
#!/bin/sh
exec /usr/local/lib/chaptersmith/chaptersmith "$@"
EOF
  chmod 755 "$PAYLOAD/usr/local/bin/chaptersmith"

  # Remove a previous install so libraries from older FFmpeg versions do not linger.
  cat > "$WORK/scripts/preinstall" <<'EOF'
#!/bin/sh
# Installer supplies the destination volume as $3 (also supports other volumes).
rm -rf "${3:?missing installer target volume}/usr/local/lib/chaptersmith"
exit 0
EOF
  chmod 755 "$WORK/scripts/preinstall"

  OUT="$OUT_DIR/ChapterSmith-CLI-$VERSION-macos-$ARCH.pkg"
  rm -f "$OUT"
  # Drop xattrs (e.g. com.apple.provenance) so pkgbuild does not emit ._* AppleDouble files.
  chmod -R u+w "$PAYLOAD"; xattr -cr "$PAYLOAD"
  pkg_sign=()
  if [[ -n "$INSTALLER_IDENTITY" ]]; then pkg_sign=(--sign "$INSTALLER_IDENTITY" --timestamp); fi
  pkgbuild --root "$PAYLOAD" --scripts "$WORK/scripts" \
    --identifier com.profipoint.chaptersmith.cli --version "${VERSION#v}" \
    --install-location / ${pkg_sign[@]+"${pkg_sign[@]}"} "$OUT"
  notarize "$OUT"
fi
echo "==> $OUT"
