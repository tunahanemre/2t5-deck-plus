#!/usr/bin/env bash
# Build a release zip of 2T5 DECK+ for a GitHub release.
#
# Builds and signs the app with scripts/build-app.sh (nothing is installed),
# stages it as "2T5 DECK+.app" (the name people see in /Applications), zips it
# with ditto (keeps the signature and the bundle layout intact), and prints the
# size and SHA-256 for the release notes.
#
# Output: dist/2T5-DECK-plus-<version>-<commit>-macos-arm64.zip  (dist/ is git-ignored)
# Env:    DECKPLUS_RELEASE_DIR  where to write the zip (default: <repo>/dist)
#         DECKPLUS_SKIP_BUILD=1 reuse the bundle already in app/build/bin
#
# It does not publish anything. Uploading the zip to a GitHub release is a
# separate, deliberate step.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${DECKPLUS_RELEASE_DIR:-$REPO_ROOT/dist}"
APP_BUNDLE="$REPO_ROOT/app/build/bin/2t5-deck-plus.app"

VERSION="$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' "$REPO_ROOT/app/wails.json" | head -n1)"
COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD)"
if [ -n "$(git -C "$REPO_ROOT" status --porcelain --untracked-files=no)" ]; then
  echo "WARNING: the working tree has uncommitted changes; the zip will not match commit $COMMIT." >&2
  COMMIT="${COMMIT}-dirty"
fi

if [ "${DECKPLUS_SKIP_BUILD:-0}" != "1" ]; then
  DECKPLUS_NO_INSTALL=1 DECKPLUS_BUILD_STAMP="$COMMIT" "$REPO_ROOT/scripts/build-app.sh"
fi
if [ ! -d "$APP_BUNDLE" ]; then
  echo "ERROR: no bundle at $APP_BUNDLE" >&2
  exit 1
fi

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/deckplus-release.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT
ditto "$APP_BUNDLE" "$STAGE/2T5 DECK+.app"

echo "==> verifying the staged copy"
codesign --verify --deep --strict "$STAGE/2T5 DECK+.app"
MINOS="$(otool -l "$STAGE/2T5 DECK+.app/Contents/MacOS/2t5-deck-plus" | awk '/LC_BUILD_VERSION/{f=1} f&&/minos/{print $2; exit}')"
ARCH="$(lipo -archs "$STAGE/2T5 DECK+.app/Contents/MacOS/2t5-deck-plus")"
echo "architecture: $ARCH, minimum macOS: $MINOS"

mkdir -p "$OUT_DIR"
ZIP="$OUT_DIR/2T5-DECK-plus-${VERSION}-${COMMIT}-macos-${ARCH}.zip"
rm -f "$ZIP"
ditto -c -k --keepParent "$STAGE/2T5 DECK+.app" "$ZIP"

echo ""
echo "==> DONE"
echo "Zip:    $ZIP"
echo "Size:   $(du -h "$ZIP" | cut -f1)"
echo "SHA256: $(shasum -a 256 "$ZIP" | cut -d' ' -f1)"
