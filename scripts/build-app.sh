#!/usr/bin/env bash
# Build, sign, and install 2T5 DECK+ (the Wails app).
#
# Names: wails builds app/build/bin/2t5-deck-plus.app (the wails.json "name"; the
# executable inside is 2t5-deck-plus); the INSTALLED copy is named
# "2T5 DECK+.app" (the "+" and the space are meant, everything below is quoted).
# Env: DECKPLUS_INSTALL_PATH, DECKPLUS_NO_INSTALL=1, DECKPLUS_BUILD_STAMP; the old
# LD_DECK_* names of the same three are still read when the new one is unset.
#
# Signs with the persistent self-signed "2T5 DECK+ Dev" code-signing
# cert (login keychain) so the designated requirement anchors to the
# certificate identity + CFBundleIdentifier, NOT a cdhash — that's what lets
# Accessibility TCC grants survive rebuilds (see docs/specs/2026-07-01-ld-
# resolve-app-design.md §7). `wails build` alone ad-hoc-signs the bundle
# (cdhash DR, confirmed via `codesign -d -r-` on a plain wails-built bundle)
# so this script always re-signs from scratch after copying in the sidecar
# and default configs.
#
# Does NOT launch the app. Building/signing/installing only — the live E2E
# run (Task 7) is a separate, explicitly human-supervised step.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

WAILS_BIN="${WAILS_BIN:-$HOME/go/bin/wails}"
CERT_NAME="2T5 DECK+ Dev"
BUNDLE_ID="com.2t5.deck-plus"

APP_BUNDLE="$REPO_ROOT/app/build/bin/2t5-deck-plus.app"
# DECKPLUS_INSTALL_PATH installs somewhere else; DECKPLUS_NO_INSTALL=1 builds,
# signs and verifies only (step 5 is skipped, nothing outside the repo is
# touched). Both default to the old behaviour.
# Default install location: the studio's applications volume when it is mounted, otherwise /Applications.
# studio default; override with DECKPLUS_INSTALL_PATH
if [ -d "/Volumes/2T5-WS-01/05-APPLICATIONs" ]; then
  DEFAULT_INSTALL_PATH="/Volumes/2T5-WS-01/05-APPLICATIONs/2T5 DECK+.app"
else
  DEFAULT_INSTALL_PATH="/Applications/2T5 DECK+.app"
fi
INSTALL_PATH="${DECKPLUS_INSTALL_PATH:-${LD_DECK_INSTALL_PATH:-$DEFAULT_INSTALL_PATH}}"
NO_INSTALL="${DECKPLUS_NO_INSTALL:-${LD_DECK_NO_INSTALL:-0}}"
BUILD_STAMP="${DECKPLUS_BUILD_STAMP:-${LD_DECK_BUILD_STAMP:-}}"
case "$(basename "$INSTALL_PATH")" in
  "2T5 DECK+"*.app | 2t5-deck-plus*.app) ;;
  *) echo "ERROR: install path must end in '2T5 DECK+*.app' or '2t5-deck-plus*.app' (got $INSTALL_PATH)" >&2; exit 1 ;;
esac

if [ ! -x "$WAILS_BIN" ]; then
  echo "ERROR: wails CLI not found at $WAILS_BIN (set WAILS_BIN to override)" >&2
  exit 1
fi

# --- 1. Build ---------------------------------------------------------
echo "==> wails build"
# DECKPLUS_BUILD_STAMP=<text> stamps the build id the preset canary compares
# (main.buildStamp), so a scratch copy and the installed copy of the SAME build
# share one id. Unset = the old behaviour (the id comes from the executable's
# mtime and size).
WAILS_ARGS=()
if [ -n "$BUILD_STAMP" ]; then
  WAILS_ARGS=(-ldflags "-X main.buildStamp=${BUILD_STAMP}")
fi
(cd "$REPO_ROOT/app" && "$WAILS_BIN" build ${WAILS_ARGS[@]+"${WAILS_ARGS[@]}"})

if [ ! -d "$APP_BUNDLE" ]; then
  echo "ERROR: expected bundle not found at $APP_BUNDLE after wails build" >&2
  exit 1
fi

# --- 2. Bundle sidecar + default configs into Contents/Resources ------
# These are the DEFAULTS app/paths.go's seed-if-missing logic copies into
# ~/Library/Application Support/2t5-deck-plus/ on first run — never a live
# runtime path a user edits directly. Copied in BEFORE signing so the outer
# bundle's codesign seals them as part of its Sealed Resources; the sidecar
# .py files are plain data as far as codesign is concerned (no Mach-O of
# their own), so they need no separate signature.
echo "==> bundling sidecar/ + config/ into Contents/Resources"
RESOURCES_DIR="$APP_BUNDLE/Contents/Resources"
mkdir -p "$RESOURCES_DIR/sidecar" "$RESOURCES_DIR/config"
rsync -a --delete --exclude '__pycache__' "$REPO_ROOT/sidecar/" "$RESOURCES_DIR/sidecar/"
cp "$REPO_ROOT"/config/*.json "$RESOURCES_DIR/config/"

# --- 3. Sign inside-out, no --deep, no hardened runtime ----------------
echo "==> codesign inner executable"
codesign --force --sign "$CERT_NAME" --identifier "$BUNDLE_ID" \
  "$APP_BUNDLE/Contents/MacOS/2t5-deck-plus"

echo "==> codesign outer bundle"
codesign --force --sign "$CERT_NAME" --identifier "$BUNDLE_ID" \
  "$APP_BUNDLE"

# --- 4. Verify the designated requirement is cert-anchored -------------
echo "==> verifying designated requirement"
DR_OUTPUT="$(codesign -d -r- "$APP_BUNDLE" 2>&1)"
echo "$DR_OUTPUT"

if echo "$DR_OUTPUT" | grep -q 'cdhash'; then
  echo "ERROR: designated requirement is cdhash-anchored, not cert-anchored." >&2
  echo "  This means the sign step above did not use the '$CERT_NAME' identity" >&2
  echo "  (or fell back to ad-hoc). Accessibility grants will NOT survive rebuilds." >&2
  exit 1
fi
if ! echo "$DR_OUTPUT" | grep -qE 'anchor|certificate leaf|certificate root'; then
  echo "ERROR: designated requirement does not look cert-anchored (no 'anchor'/'certificate leaf'/'certificate root' found)." >&2
  echo "$DR_OUTPUT" >&2
  exit 1
fi

echo "==> codesign --verify --deep --strict"
codesign --verify --deep --strict "$APP_BUNDLE"
echo "verify: OK"

if [ "$NO_INSTALL" = "1" ]; then
  echo ""
  echo "==> DONE (DECKPLUS_NO_INSTALL=1 / LD_DECK_NO_INSTALL=1: built, signed and verified; nothing installed)"
  echo "Built: $APP_BUNDLE"
  exit 0
fi

# --- 5. Install ----------------------------------------------------------
echo "==> installing to $INSTALL_PATH"
mkdir -p "$(dirname "$INSTALL_PATH")"
rm -rf "$INSTALL_PATH"
cp -R "$APP_BUNDLE" "$INSTALL_PATH"

echo ""
echo "==> DONE"
echo "Installed: $INSTALL_PATH"
echo "Designated requirement:"
codesign -d -r- "$INSTALL_PATH"
