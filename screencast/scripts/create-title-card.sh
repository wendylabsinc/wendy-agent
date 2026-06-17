#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
SVG="${1:-$PROJECT_DIR/title-card.svg}"
PNG="${2:-$PROJECT_DIR/title-card.png}"
MP4="${3:-$PROJECT_DIR/recordings/00-title-card.mp4}"
METADATA="${TITLE_CARD_METADATA:-$PROJECT_DIR/title-card.env}"
DURATION="${TITLE_CARD_SECONDS:-2}"
WIDTH="${SCREENCAST_WIDTH:-1440}"
HEIGHT="${SCREENCAST_HEIGHT:-900}"
FPS="${SCREENCAST_FPS:-10}"
CRF="${SCREENCAST_CRF:-18}"

if [[ ! -f "$SVG" ]]; then
  echo "error: title-card SVG not found: $SVG" >&2
  exit 1
fi

is_set() {
  eval "[[ \${$1+x} ]]"
}

TITLE_CARD_FIELDS=(TITLE SUBTITLE AUTHOR PLACE DATE WEBSITE CONTACT CONTACT_LABEL)
for field in "${TITLE_CARD_FIELDS[@]}"; do
  if is_set "$field"; then
    eval "TITLE_CARD_ENV_$field=\${$field}"
    eval "TITLE_CARD_HAS_ENV_$field=1"
  fi
done

if [[ -f "$METADATA" ]]; then
  # shellcheck disable=SC1090
  set -a
  source "$METADATA"
  set +a
fi

for field in "${TITLE_CARD_FIELDS[@]}"; do
  has_env="TITLE_CARD_HAS_ENV_$field"
  if is_set "$has_env"; then
    eval "$field=\$TITLE_CARD_ENV_$field"
    export "$field"
  fi
done

: "${TITLE:=Engineering screencast}"
: "${SUBTITLE:=A short walkthrough of the change}"
: "${AUTHOR:=Your Name}"
: "${PLACE:=Remote}"
: "${DATE:=$(date +%Y-%m-%d)}"
: "${WEBSITE:=example.dev}"
: "${CONTACT:=you@example.dev}"
: "${CONTACT_LABEL:=Contact}"

mkdir -p "$(dirname "$PNG")" "$(dirname "$MP4")"
rendered_svg="$(mktemp "${TMPDIR:-/tmp}/screencast-title-card.XXXXXX.svg")"
trap 'rm -f "$rendered_svg"' EXIT

python3 - "$SVG" "$rendered_svg" <<'PY'
import html
import os
import re
import sys
from pathlib import Path

source = Path(sys.argv[1])
destination = Path(sys.argv[2])
fields = [
    "TITLE",
    "SUBTITLE",
    "AUTHOR",
    "PLACE",
    "DATE",
    "WEBSITE",
    "CONTACT",
    "CONTACT_LABEL",
]
text = source.read_text(encoding="utf-8")
for field in fields:
    text = text.replace("{{" + field + "}}", html.escape(os.environ.get(field, ""), quote=False))
leftovers = sorted(set(re.findall(r"{{[^}]+}}", text)))
if leftovers:
    raise SystemExit(f"unresolved title-card placeholders: {', '.join(leftovers)}")
destination.write_text(text, encoding="utf-8")
PY

if command -v rsvg-convert >/dev/null 2>&1; then
  rsvg-convert -w "$WIDTH" -h "$HEIGHT" -o "$PNG" "$rendered_svg"
elif command -v magick >/dev/null 2>&1; then
  magick -background white -size "${WIDTH}x${HEIGHT}" "$rendered_svg" "$PNG"
elif command -v convert >/dev/null 2>&1; then
  convert -background white -size "${WIDTH}x${HEIGHT}" "$rendered_svg" "$PNG"
elif command -v sips >/dev/null 2>&1; then
  sips -s format png "$rendered_svg" --out "$PNG" >/dev/null
else
  echo "error: install rsvg-convert, ImageMagick, or use macOS sips to rasterize SVG" >&2
  exit 2
fi

ffmpeg -nostdin -y \
  -loop 1 -i "$PNG" \
  -f lavfi -i anullsrc=channel_layout=stereo:sample_rate=48000 \
  -t "$DURATION" \
  -vf "fps=$FPS,scale=$WIDTH:$HEIGHT:flags=lanczos,setsar=1,format=yuv420p" \
  -c:v libx264 -preset medium -crf "$CRF" \
  -c:a aac -b:a 192k \
  -shortest -movflags +faststart \
  "$MP4"

echo "wrote $PNG"
echo "wrote $MP4"
