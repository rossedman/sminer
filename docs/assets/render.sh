#!/usr/bin/env bash
# Render the graphics in docs/assets/src/*.html to PNGs in docs/assets/, at
# 2x for sharp text. Needs Chrome or Chromium; fonts are vendored, so no
# network is needed.
#
# Usage: docs/assets/render.sh [name…]   (default: all)
#        CHROME=/path/to/chrome docs/assets/render.sh
set -euo pipefail

cd "$(dirname "$0")"

find_chrome() {
  if [ -n "${CHROME:-}" ]; then echo "$CHROME"; return; fi
  local mac="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
  if [ -x "$mac" ]; then echo "$mac"; return; fi
  for c in google-chrome chromium chromium-browser; do
    if command -v "$c" >/dev/null; then command -v "$c"; return; fi
  done
  echo "render.sh: no Chrome or Chromium found; set CHROME=/path/to/chrome" >&2
  exit 1
}
chrome=$(find_chrome)
extra=()
# Chrome refuses to sandbox as root (containers, CI).
if [ "$(id -u)" = 0 ]; then extra+=(--no-sandbox); fi

# name width height (CSS pixels; the PNG is twice that)
targets="
banner 1280 400
social-preview 1280 640
how-it-works 1280 720
routing 1280 600
lifecycle 1280 560
"

echo "$targets" | while read -r name w h; do
  [ -n "$name" ] || continue
  if [ $# -gt 0 ] && ! printf '%s\n' "$@" | grep -qx "$name"; then continue; fi
  "$chrome" "${extra[@]}" --headless=new --disable-gpu --hide-scrollbars --no-first-run \
    --allow-file-access-from-files \
    --force-device-scale-factor=2 --window-size="$w,$h" \
    --virtual-time-budget=4000 --run-all-compositor-stages-before-draw \
    --screenshot="$PWD/$name.png" "file://$PWD/src/$name.html" 2>/dev/null
  echo "rendered $name.png"
done
