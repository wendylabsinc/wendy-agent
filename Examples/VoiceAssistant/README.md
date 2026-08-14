# Realtime Voice Assistant

A Raspberry Pi / WendyOS demo that streams microphone audio to OpenAI's
Realtime API and plays the spoken response through a speaker. It uses the GA
Realtime WebSocket interface, `gpt-realtime-2.1`, semantic voice activity
detection, and 24 kHz mono PCM16 audio in both directions.

Interruption/barge-in is enabled by default: speak over the assistant and it
stops immediately, truncating the unheard part of its reply. The assistant can
also control its own speaker volume — say "set the volume to 30 percent" or
"turn it up a bit" and it adjusts the output volume through Realtime function
tools. It can answer questions that need live data too: weather anywhere via
the free Open-Meteo API, and anything else current (news, scores, prices)
via OpenAI web search.

Audio goes through PipeWire whenever the host exposes a session socket (any
WendyOS build with the PipeWire audio stack). That is what makes Bluetooth
speakers and microphones reachable — raw ALSA cannot see them. On hosts
without a PipeWire session the app falls back to the previous
`arecord`/`aplay` ALSA path automatically.

## Hardware

- A WendyOS device — Raspberry Pi 3, 4, and 5 are all supported
- A microphone and speaker: a USB speakerphone covers both, or a connected
  Bluetooth speaker/speakerphone on a PipeWire-enabled WendyOS
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

## 1. Pick the audio devices (optional)

By default the app uses the host's default source and sink (PipeWire mode —
WirePlumber routes these, and a connected Bluetooth speaker set as default
"just works"), or auto-detects the first USB sound card (ALSA fallback mode).
If that is what you want, skip this step.

To choose devices yourself, list them:

```sh
wendy device audio list --device <hostname>.local
```

and export the identifiers before deploying:

```sh
# PipeWire mode: a node name (stable) or numeric id from `audio list`
export AUDIO_INPUT_DEVICE='bluez_input.00_7F_1D_51_A9_6E'
export AUDIO_OUTPUT_DEVICE='bluez_output.00_7F_1D_51_A9_6E.1'

# ALSA fallback mode: an ALSA PCM name
export AUDIO_INPUT_DEVICE='plughw:CARD=Device,DEV=0'
export AUDIO_OUTPUT_DEVICE='plughw:CARD=Device,DEV=0'  # or an HDMI card
```

In ALSA mode use `plughw` rather than `hw` so ALSA can convert a device's
native sample rate to the API's 24 kHz PCM stream when necessary. You can
validate the microphone before deploying with `wendy device audio monitor`.

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
`pactl` on the default sink (PipeWire mode) or `amixer` (ALSA fallback) inside
the container.

You can also drive volume manually with the interactive TUI on the host:

```sh
wendy device audio --device <hostname>.local
# ↑/↓ select · enter set default · ←/→ volume · q quit
```

Agents older than the 2026-08 volume support don't implement `SetAudioVolume`;
the TUI will say so — update the agent (or push a dev build with
`wendy device push-agent`).

## Weather and web search

Ask "what's the weather in Tokyo?" or "will it rain in Portland this
weekend?" and the assistant calls a `get_weather` function tool backed by the
free, keyless [Open-Meteo](https://open-meteo.com) API — current conditions
plus a 3-day forecast, in celsius or fahrenheit as the conversation implies.

For anything else that needs the live internet — "what's in the news today?",
"who won the Giants game?", "what's the bitcoin price?" — the assistant calls
a `web_search` tool. The Realtime API has no built-in web search, so the app
forwards the query to the OpenAI Responses API (`/v1/responses`) with its
built-in `web_search` tool enabled, using the same `OPENAI_API_KEY`, and
speaks the short text answer. Each search bills one Responses API call
(`OPENAI_SEARCH_MODEL`, default `gpt-5-mini`) on top of the Realtime session;
set `WEB_SEARCH_ENABLED=false` to remove the tool entirely. Weather lookups
are free and unaffected by the toggle.

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
| `WEB_SEARCH_ENABLED` | `true` | Set to `false` to remove the paid `web_search` tool |
| `OPENAI_SEARCH_MODEL` | `gpt-5-mini` | Responses API model used for web searches |
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

Tool calling drives everything beyond conversation: the session registers
`set_volume`, `adjust_volume`, `get_volume`, `get_weather`, and `web_search`
function tools. When the model decides to call one, the finished
`function_call` item arrives with its arguments and the app executes it —
`amixer` against the playback card for volume (the `audio` entitlement mounts
`/dev/snd` into the container), a geocode-then-forecast pair of Open-Meteo
requests for weather, or a Responses API call with OpenAI's built-in
`web_search` tool for searches. The result goes back as a
`function_call_output` item followed by `response.create` for a spoken reply
— unless the user is already talking again. Tool calls run as concurrent
asyncio tasks (HTTP requests on worker threads), so the event loop keeps
draining and barge-in stays responsive even mid-search.

This follows OpenAI's current [Realtime WebSocket
guide](https://developers.openai.com/api/docs/guides/realtime-websocket) and
[Realtime conversation event
flow](https://developers.openai.com/api/docs/guides/realtime-conversations).

The API key is appropriate here because this is a server-to-server connection
from a trusted device. Do not copy this pattern into browser code; browser and
mobile clients should use WebRTC and ephemeral credentials instead.
