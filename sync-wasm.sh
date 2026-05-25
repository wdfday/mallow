#!/usr/bin/env bash
# sync-wasm.sh — build alm-wasm and sync artifacts into mallow-client/vendor/alm-wasm
#
# Usage:
#   ./sync-wasm.sh          # build then sync
#   ./sync-wasm.sh --sync-only  # skip build, just copy existing pkg/ → vendor/

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PKG_DIR="$REPO_ROOT/almanac/crates/alm-wasm/pkg"
VENDOR_DIR="$REPO_ROOT/mallow-client/vendor/alm-wasm"

SYNC_ONLY=false
for arg in "$@"; do
  [[ "$arg" == "--sync-only" ]] && SYNC_ONLY=true
done

if [[ "$SYNC_ONLY" == false ]]; then
  echo "▶ building alm-wasm..."
  (cd "$REPO_ROOT/almanac/crates/alm-wasm" && wasm-pack build --target bundler)
  # wasm-pack writes pkg/.gitignore with '*' — remove it so artifacts stay tracked
  rm -f "$PKG_DIR/.gitignore"
  echo "✓ build done"
fi

echo "▶ syncing $PKG_DIR → $VENDOR_DIR"
mkdir -p "$VENDOR_DIR"
cp -r "$PKG_DIR/." "$VENDOR_DIR/"
echo "✓ sync done"

echo ""
echo "Files in vendor/alm-wasm:"
ls -lh "$VENDOR_DIR"
