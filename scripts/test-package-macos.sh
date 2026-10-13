#!/usr/bin/env bash
# Fast checks for release credential validation; no keychain or package mutation.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
expect_error() {
  local message="$1"; shift
  if env -u MACOS_SIGN_IDENTITY -u MACOS_INSTALLER_IDENTITY -u APPLE_API_KEY_PATH \
    -u APPLE_API_KEY_ID -u APPLE_API_ISSUER_ID "$@" \
    /bin/bash "$ROOT/scripts/package-macos.sh" gui >"$WORK/log" 2>&1; then
    echo "expected failure: $message" >&2; exit 1
  fi
  grep -Fq "$message" "$WORK/log" || { cat "$WORK/log" >&2; exit 1; }
}
expect_error 'MACOS_SIGN_IDENTITY is not set' REQUIRE_SIGNING=true
expect_error 'ad-hoc signing' REQUIRE_SIGNING=true MACOS_SIGN_IDENTITY=-
expect_error 'APPLE_API_KEY_ID is not set' MACOS_SIGN_IDENTITY=placeholder APPLE_API_KEY_PATH="$WORK/key.p8"
expect_error 'APPLE_API_ISSUER_ID is not set' MACOS_SIGN_IDENTITY=placeholder APPLE_API_KEY_PATH="$WORK/key.p8" APPLE_API_KEY_ID=placeholder
expect_error 'notarization key is not readable' MACOS_SIGN_IDENTITY=placeholder APPLE_API_KEY_PATH="$WORK/missing.p8" APPLE_API_KEY_ID=placeholder APPLE_API_ISSUER_ID=placeholder
echo 'test-package-macos: OK'
