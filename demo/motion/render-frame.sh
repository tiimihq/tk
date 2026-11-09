#!/bin/sh
# render-frame.sh <frame-number> <out-dir> [WxH] [&sq=1]: one frame of index.html at 30 fps, via headless Chrome.
n=$1; out=$2; size=${3:-1600,900}; extra=${4:-}
t=$(echo "scale=4; $n/30" | bc)
prof="$out/prof-$n"
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --disable-gpu --hide-scrollbars \
  --user-data-dir="$prof" --force-device-scale-factor=1 --window-size="$size" \
  --screenshot="$(printf '%s/%04d.png' "$out" "$n")" "file://$(cd "$(dirname "$0")" && pwd)/index.html?t=$t$extra" >/dev/null 2>&1
rm -rf "$prof"
