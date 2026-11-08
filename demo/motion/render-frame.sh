#!/bin/sh
# render-frame.sh <frame-number> <out-dir>: one frame of index.html at 30 fps, via headless Chrome.
n=$1; out=$2
t=$(echo "scale=4; $n/30" | bc)
prof="$out/prof-$n"
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --disable-gpu --hide-scrollbars \
  --user-data-dir="$prof" --force-device-scale-factor=1 --window-size=1600,900 \
  --screenshot="$(printf '%s/%04d.png' "$out" "$n")" "file://$(cd "$(dirname "$0")" && pwd)/index.html?t=$t" >/dev/null 2>&1
rm -rf "$prof"
