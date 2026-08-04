#!/usr/bin/env bash
set -euo pipefail

demo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
env_file="$demo_dir/.env"
wendy_package="$demo_dir/../../go/cmd/wendy"

if [[ -f "$env_file" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$env_file"
  set +a
fi

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY is missing; add it to $env_file" >&2
  exit 2
fi

# Defaults for the USB Speaker Phone currently attached to pi4. Values in
# .env still win, so input and output can be routed to separate ALSA devices.
export AUDIO_INPUT_DEVICE="${AUDIO_INPUT_DEVICE:-plughw:2,0}"
export AUDIO_OUTPUT_DEVICE="${AUDIO_OUTPUT_DEVICE:-plughw:2,0}"
export MUTE_INPUT_DURING_PLAYBACK="${MUTE_INPUT_DURING_PLAYBACK:-false}"

# The system CLI predates wendy.json env expansion, while the Pi's agent
# predates env propagation on the chunk-diff deploy path. The repository CLI
# plus the older registry path works with both versions without baking the API
# key into the image or exposing it as a command-line argument.
exec go run "$wendy_package" run --prefix "$demo_dir" --chunking off "$@"
