#!/usr/bin/env bash
# make-testdata.sh — regenerate the tiny audio/image fixtures in testdata/formats.
# Needs a full system ffmpeg (with libmp3lame and libopus; Vorbis uses the native experimental encoder), e.g. `brew install ffmpeg`.
# The LGPL build used by the app has no MP3/Vorbis encoders, so fixtures are committed.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/testdata/formats"
FF=(ffmpeg -hide_banner -loglevel error -y)
mkdir -p "$OUT"/{mp3,ogg,m4a,aac,flac,wav,opus}

for i in 1 2; do
  freq=$((330 * i))
  src=(-f lavfi -i "sine=frequency=${freq}:duration=3" -ac 1 -ar 44100)
  meta=(-metadata "title=Fixture Part $i" -metadata "artist=Fixture Artist" -metadata "album=Fixture Book")
  "${FF[@]}" "${src[@]}" -c:a libmp3lame -b:a 48k "${meta[@]}" "$OUT/mp3/0$i.mp3"
  "${FF[@]}" "${src[@]}" -c:a vorbis -strict -2 -ac 2 -b:a 48k "${meta[@]}" "$OUT/ogg/0$i.ogg"
  "${FF[@]}" "${src[@]}" -c:a aac -b:a 48k "${meta[@]}" "$OUT/m4a/0$i.m4a"
  "${FF[@]}" "${src[@]}" -c:a aac -b:a 48k -f adts "$OUT/aac/0$i.aac"
done

# Additional import/probe fixtures, using the same 330 Hz three-second sine.
src=(-f lavfi -i "sine=frequency=330:duration=3" -ac 1 -ar 44100)
"${FF[@]}" "${src[@]}" -c:a flac "$OUT/flac/01.flac"
"${FF[@]}" "${src[@]}" -c:a pcm_s16le "$OUT/wav/01.wav"
"${FF[@]}" "${src[@]}" -ar 48000 -c:a libopus "$OUT/opus/01.opus"

"${FF[@]}" -f lavfi -i "color=c=steelblue:s=64x64" -frames:v 1 "$OUT/cover.jpg"
"${FF[@]}" -f lavfi -i "color=c=darkorange:s=64x64" -frames:v 1 "$OUT/cover.png"
"${FF[@]}" -i "$OUT/m4a/01.m4a" -c copy \
  -metadata title="Fixture Book" -metadata composer="Fixture Narrator" \
  -metadata genre="Fantasy" -metadata copyright="Fixture copyright" \
  -metadata date="2026" -metadata description="Fixture description" \
  -metadata synopsis="Fixture full synopsis" -metadata show="Fixture Series" \
  "$OUT/tagged.m4a"

ls -l "$OUT"/*/* "$OUT"/cover.*
