#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
WIDTH="${SCREENCAST_WIDTH:-1440}"
HEIGHT="${SCREENCAST_HEIGHT:-900}"
FPS="${SCREENCAST_FPS:-10}"
CRF="${SCREENCAST_CRF:-18}"
DURATION="${SLIDE_SECONDS:-8}"

require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: required tool not found: $1" >&2
    exit 2
  fi
}

rasterize() {
  local svg="$1"
  local png="$2"
  if command -v rsvg-convert >/dev/null 2>&1; then
    rsvg-convert -w "$WIDTH" -h "$HEIGHT" -o "$png" "$svg"
  elif command -v magick >/dev/null 2>&1; then
    magick -background '#171717' -size "${WIDTH}x${HEIGHT}" "$svg" "$png"
  elif command -v convert >/dev/null 2>&1; then
    convert -background '#171717' -size "${WIDTH}x${HEIGHT}" "$svg" "$png"
  elif command -v sips >/dev/null 2>&1; then
    sips -s format png "$svg" --out "$png" >/dev/null
  else
    echo "error: install rsvg-convert, ImageMagick, or use macOS sips to rasterize SVG" >&2
    exit 2
  fi
}

require_tool ffmpeg
mkdir -p "$PROJECT_DIR/recordings"

for svg in "$PROJECT_DIR"/slides/*.svg; do
  [[ -e "$svg" ]] || continue
  base="$(basename "$svg" .svg)"
  png="$PROJECT_DIR/slides/$base.png"
  mp4="$PROJECT_DIR/recordings/$base.mp4"
  rasterize "$svg" "$png"
  ffmpeg -nostdin -y \
    -loop 1 -i "$png" \
    -f lavfi -i anullsrc=channel_layout=stereo:sample_rate=48000 \
    -t "$DURATION" \
    -vf "fps=$FPS,scale=$WIDTH:$HEIGHT:flags=lanczos,setsar=1,format=yuv420p" \
    -c:v libx264 -preset medium -crf "$CRF" \
    -c:a aac -b:a 192k \
    -shortest -movflags +faststart \
    "$mp4" >/tmp/screencast-create-slide.log 2>&1
  echo "wrote $mp4"
done
