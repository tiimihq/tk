#!/bin/sh
# render.sh [square]: renders demo/tiimi-benchmark-x.mp4 (1600x900), or with "square"
# demo/tiimi-benchmark-square.mp4 (1080x1080), 14.5 s at 30 fps, from index.html.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out=$(mktemp -d "${TMPDIR:-/tmp}/tiimi-motion.XXXXXX")
size=1600,900 extra= name=tiimi-benchmark-x.mp4
if [ "${1:-}" = square ]; then size=1080,1080 extra='&sq=1' name=tiimi-benchmark-square.mp4; fi
seq 0 434 | xargs -P 8 -n 1 -I{} sh "$here/render-frame.sh" {} "$out" "$size" "$extra"
python3 -I "$here/soundtrack.py" "$out/sound.wav"
ffmpeg -v error -y -framerate 30 -i "$out/%04d.png" -i "$out/sound.wav" -c:v libx264 -pix_fmt yuv420p -crf 18 -preset slow \
  -af "loudnorm=I=-16:TP=-1.5:LRA=11" -c:a aac -b:a 160k -ar 44100 -ac 2 -shortest \
  -movflags +faststart "$here/../$name"
rm -rf "$out"
echo "wrote demo/$name"
