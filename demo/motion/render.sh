#!/bin/sh
# render.sh: renders demo/tiimi-benchmark-x.mp4 (14.5 s, 1600x900, 30 fps) from index.html.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out=$(mktemp -d "${TMPDIR:-/tmp}/tiimi-motion.XXXXXX")
seq 0 434 | xargs -P 8 -n 1 -I{} sh "$here/render-frame.sh" {} "$out"
python3 -I "$here/soundtrack.py" "$out/sound.wav"
ffmpeg -v error -y -framerate 30 -i "$out/%04d.png" -i "$out/sound.wav" -c:v libx264 -pix_fmt yuv420p -crf 18 -preset slow \
  -af "loudnorm=I=-16:TP=-1.5:LRA=11" -c:a aac -b:a 160k -ar 44100 -ac 2 -shortest \
  -movflags +faststart "$here/../tiimi-benchmark-x.mp4"
rm -rf "$out"
echo "wrote demo/tiimi-benchmark-x.mp4"
