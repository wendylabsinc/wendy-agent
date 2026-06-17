#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
SCENE_PLAN="${1:-$PROJECT_DIR/scene-plan.tsv}"
OUT_DIR="$PROJECT_DIR/stitch/output"
BUILD_DIR="$PROJECT_DIR/stitch/build"
OUT_FILE="${OUT_FILE:-$OUT_DIR/screencast.mp4}"
WIDTH="${SCREENCAST_WIDTH:-1440}"
HEIGHT="${SCREENCAST_HEIGHT:-900}"
FPS="${SCREENCAST_FPS:-10}"
CRF="${SCREENCAST_CRF:-18}"

require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: required tool not found: $1" >&2
    exit 2
  fi
}

concat_path() {
  python3 - "$1" <<'PY'
import os
import sys
path = os.path.abspath(sys.argv[1]).replace("'", "'\\''")
print(f"file '{path}'")
PY
}

require_tool ffmpeg
require_tool ffprobe
require_tool python3

if [[ ! -f "$SCENE_PLAN" ]]; then
  echo "error: scene plan not found: $SCENE_PLAN" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR/video" "$BUILD_DIR/audio"

PLAN_REPORT="$BUILD_DIR/render-plan.tsv"
python3 "$SCRIPT_DIR/plan-durations.py" "$SCENE_PLAN" --format tsv --strict > "$PLAN_REPORT"
python3 "$SCRIPT_DIR/plan-durations.py" "$SCENE_PLAN" --format render --strict > "$BUILD_DIR/render-plan.internal.tsv"

declare -a SCENES VIDEO_RELS VOICEOVER_RELS DURATIONS STATUSES
while IFS=$'\t' read -r scene video_rel voiceover_rel render_seconds status; do
  SCENES+=("$scene")
  VIDEO_RELS+=("$video_rel")
  if [[ "$voiceover_rel" == "-" ]]; then
    VOICEOVER_RELS+=("")
  else
    VOICEOVER_RELS+=("$voiceover_rel")
  fi
  DURATIONS+=("$render_seconds")
  STATUSES+=("$status")
done < "$BUILD_DIR/render-plan.internal.tsv"

if [[ "${#SCENES[@]}" -eq 0 ]]; then
  echo "error: no scenes found in $SCENE_PLAN" >&2
  exit 1
fi

for index in "${!SCENES[@]}"; do
  scene="${SCENES[$index]}"
  video="$PROJECT_DIR/${VIDEO_RELS[$index]}"
  voiceover_rel="${VOICEOVER_RELS[$index]}"
  duration="${DURATIONS[$index]}"
  status="${STATUSES[$index]}"

  if [[ "$status" == *"video_shorter_than_render"* ]]; then
    echo "Rendering scene $scene video (${duration}s, padding final frame)"
  else
    echo "Rendering scene $scene video (${duration}s)"
  fi

  ffmpeg -nostdin -y -i "$video" \
    -vf "fps=$FPS,scale=$WIDTH:$HEIGHT:flags=lanczos,setsar=1,format=yuv420p,tpad=stop_mode=clone:stop_duration=$duration,trim=duration=$duration,setpts=PTS-STARTPTS" \
    -an -c:v libx264 -preset medium -crf "$CRF" -pix_fmt yuv420p -movflags +faststart \
    "$BUILD_DIR/video/$scene.mp4" >/tmp/screencast-render-video.log 2>&1

  if [[ -n "$voiceover_rel" ]]; then
    voiceover="$PROJECT_DIR/$voiceover_rel"
    echo "Rendering scene $scene audio with VO"
    ffmpeg -nostdin -y -i "$voiceover" -t "$duration" \
      -af "apad=pad_dur=$duration,atrim=0:$duration,asetpts=PTS-STARTPTS" \
      -ar 48000 -ac 2 -c:a pcm_s16le "$BUILD_DIR/audio/$scene.wav" >/tmp/screencast-render-audio.log 2>&1
  else
    echo "Rendering scene $scene silent audio"
    ffmpeg -nostdin -y -f lavfi -i "anullsrc=channel_layout=stereo:sample_rate=48000" -t "$duration" \
      -c:a pcm_s16le "$BUILD_DIR/audio/$scene.wav" >/tmp/screencast-render-audio.log 2>&1
  fi
done

: > "$BUILD_DIR/video-list.txt"
: > "$BUILD_DIR/audio-list.txt"
for scene in "${SCENES[@]}"; do
  concat_path "$BUILD_DIR/video/$scene.mp4" >> "$BUILD_DIR/video-list.txt"
  concat_path "$BUILD_DIR/audio/$scene.wav" >> "$BUILD_DIR/audio-list.txt"
done

ffmpeg -nostdin -y -f concat -safe 0 -i "$BUILD_DIR/video-list.txt" -c copy "$BUILD_DIR/video-full.mp4" >/tmp/screencast-concat-video.log 2>&1
ffmpeg -nostdin -y -f concat -safe 0 -i "$BUILD_DIR/audio-list.txt" -c:a pcm_s16le "$BUILD_DIR/audio-full.wav" >/tmp/screencast-concat-audio.log 2>&1

ffmpeg -nostdin -y -i "$BUILD_DIR/video-full.mp4" -i "$BUILD_DIR/audio-full.wav" \
  -map 0:v:0 -map 1:a:0 -c:v copy -c:a aac -b:a 192k -shortest -movflags +faststart \
  "$OUT_FILE" >/tmp/screencast-mux-final.log 2>&1

cp "$PLAN_REPORT" "$PROJECT_DIR/stitch/duration-report.tsv"

final_duration="$(ffprobe -v error -show_entries format=duration -of default=nk=1:nw=1 "$OUT_FILE")"
final_stream="$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height,r_frame_rate -of csv=p=0 "$OUT_FILE")"

echo "wrote $OUT_FILE"
echo "$final_duration"
echo "$final_stream"
