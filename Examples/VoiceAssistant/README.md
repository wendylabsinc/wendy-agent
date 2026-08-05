# Realtime Voice Assistant

A Raspberry Pi / WendyOS demo that streams microphone audio to OpenAI's
Realtime API and plays the spoken response through a speaker. It uses the GA
Realtime WebSocket interface, `gpt-realtime-2.1`, semantic voice activity
detection, and 24 kHz mono PCM16 audio in both directions.

Interruption/barge-in is enabled by default: speak over the assistant and it
stops immediately, truncating the unheard part of its reply. The assistant can
also control its own speaker volume — say "set the volume to 30 percent" or
"turn it up a bit" and it adjusts the ALSA mixer through Realtime function
tools.

## Hardware

- A WendyOS device — Raspberry Pi 3, 4, and 5 are all supported
- A microphone and speaker visible to ALSA (a USB speakerphone covers both)
- Internet access from the device

Keep the mic away from the speaker for the cleanest turn detection. The mic
stays live while the assistant speaks so the user can interrupt it. Hardware
echo cancellation is strongly recommended to prevent the speaker from
interrupting itself — USB conference speakerphones (e.g. Anker PowerConf) have
it built in. Without it, set `MUTE_INPUT_DURING_PLAYBACK=true` for half-duplex
operation (this disables interruption).

### Raspberry Pi model notes

| Model | Audio out | Microphone |
| --- | --- | --- |
| Pi 5 | USB or HDMI (no 3.5 mm jack) | USB |
| Pi 3 / 4 | USB, HDMI, or 3.5 mm jack (output only) | USB |

No Pi has an analog audio input, so the microphone is always USB (or a HAT).
ALSA card *numbers* can change across boots as USB devices enumerate, so
prefer the stable name form `plughw:CARD=<name>,DEV=<n>` — the app's
auto-detection (below) does this for you.

## 1. Pick the ALSA devices (optional)

By default the app auto-detects the first USB sound card for both capture and
playback and falls back to the ALSA `default` device. If that is what you want,
skip this step.

To choose devices yourself, list them:

```sh
wendy device audio list --device <hostname>.local
```

and export the identifiers before deploying:

```sh
export AUDIO_INPUT_DEVICE='plughw:CARD=Device,DEV=0'
export AUDIO_OUTPUT_DEVICE='plughw:CARD=Device,DEV=0'  # or an HDMI card
```

Use `plughw` rather than `hw` so ALSA can convert a device's native sample
rate to the API's 24 kHz PCM stream when necessary. You can validate the
microphone before deploying with `wendy device audio monitor`.

## 2. Supply the API key and deploy

Create an API key in the OpenAI dashboard, then:

```sh
cp .env.example .env
# Edit .env to add your key, then:
set -a; source .env; set +a
wendy run --device <hostname>.local
```

`./run.sh --device <hostname>.local` does the same `.env` loading for you.
The `.env` file is gitignored; the key is injected at deployment time rather
than stored in the repository or container image.

When the log says `Ready — listening for your voice`, speak normally. Semantic
VAD ends the turn automatically and the reply plays through the selected
speaker. Press Ctrl-C to stop the attached log stream.

## Volume

At startup the app sets the speaker to `STARTUP_VOLUME_PERCENT` (default 70%)
— WendyOS does not persist a volume across reinstalls, so a fresh device could
otherwise be silent or very quiet. Set it to `off` to leave the mixer alone.

While the assistant is running, just ask it: "set the volume to 30 percent",
"turn it up", "how loud are you?". These are Realtime function tools backed by
`amixer` inside the container.

You can also drive volume manually with the interactive TUI on the host:

```sh
wendy device audio --device <hostname>.local
# ↑/↓ select · enter set default · ←/→ volume · q quit
```

Agents older than the 2026-08 volume support don't implement `SetAudioVolume`;
the TUI will say so — update the agent (or push a dev build with
`wendy device push-agent`).

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `OPENAI_API_KEY` | required | OpenAI API credential |
| `AUDIO_INPUT_DEVICE` | auto-detect (first USB card, else `default`) | ALSA capture PCM |
| `AUDIO_OUTPUT_DEVICE` | auto-detect (first USB card, else `default`) | ALSA playback PCM |
| `STARTUP_VOLUME_PERCENT` | `70` | Speaker volume applied once at startup; `off` disables |
| `OPENAI_REALTIME_MODEL` | `gpt-realtime-2.1` | Realtime model |
| `OPENAI_VOICE` | `marin` | Assistant voice |
| `ASSISTANT_INSTRUCTIONS` | concise voice assistant | System instructions |
| `MUTE_INPUT_DURING_PLAYBACK` | `false` | Set to `true` for half-duplex echo protection (disables interruption) |
| `AUDIO_SAMPLE_RATE` | `24000` | PCM sample rate in Hz (the API expects 24 kHz) |
| `AUDIO_CHUNK_MS` | `100` | Microphone packet duration |
| `LOG_LEVEL` | `INFO` | Python log level |

## Local checks

The unit tests do not contact OpenAI or require audio hardware:

```sh
python3 -m unittest discover -s tests -v
```

To run the same image on a regular Linux host with ALSA:

```sh
docker build -t wendy-voice-assistant .
docker run --rm --network host --device /dev/snd \
  -e OPENAI_API_KEY \
  -e AUDIO_INPUT_DEVICE=default \
  -e AUDIO_OUTPUT_DEVICE=default \
  wendy-voice-assistant
```

## How it works

`arecord` produces 24 kHz mono PCM16 chunks. The Python process base64-encodes
them into `input_audio_buffer.append` events over one authenticated WebSocket.
OpenAI's semantic VAD creates each response. Returned
`response.output_audio.delta` chunks are decoded and piped directly to `aplay`,
so there are no temporary audio files. When semantic VAD detects speech during
a reply, the app drops queued speaker audio and truncates the unheard portion
of the assistant message in the Realtime conversation.

Volume control is Realtime tool calling: the session registers `set_volume`,
`adjust_volume`, and `get_volume` function tools. When the model decides to
call one, the finished `function_call` item arrives with its arguments, the
app runs `amixer` against the playback card (the `audio` entitlement mounts
`/dev/snd` into the container), sends the result back as a
`function_call_output` item, and asks for a spoken confirmation with
`response.create` — unless the user is already talking again.

This follows OpenAI's current [Realtime WebSocket
guide](https://developers.openai.com/api/docs/guides/realtime-websocket) and
[Realtime conversation event
flow](https://developers.openai.com/api/docs/guides/realtime-conversations).

The API key is appropriate here because this is a server-to-server connection
from a trusted device. Do not copy this pattern into browser code; browser and
mobile clients should use WebRTC and ephemeral credentials instead.
