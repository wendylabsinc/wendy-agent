# Realtime Voice Assistant

A Raspberry Pi / WendyOS demo that streams microphone audio to OpenAI's
Realtime API and plays the spoken response through a separately selectable
speaker. It uses the GA Realtime WebSocket interface, `gpt-realtime-2.1`,
semantic voice activity detection, and 24 kHz mono PCM16 audio in both
directions.

## Hardware

- A WendyOS device (the example command uses `pi4`)
- A microphone visible to ALSA
- A speaker visible to ALSA; USB, HDMI, or the Pi audio jack can be selected
- Internet access from the device

Keep the mic away from the speaker for the cleanest turn detection. The mic
stays live while the assistant speaks so the user can interrupt it. Hardware
echo cancellation is strongly recommended to prevent the speaker from
interrupting itself.

## 1. Find the ALSA devices

```sh
wendy device audio list --device wendyos-pi4.local
```

Choose the capture and playback identifiers shown by the command. ALSA names
typically look like `plughw:CARD=Device,DEV=0`. If the desired devices are
already the WendyOS defaults, leave both variables unset.

On the `pi4` inspected while building this demo, card 2 is a USB Speaker Phone
with both capture and playback, while cards 0 and 1 are HDMI playback devices.
For that current wiring, start with:

```sh
export AUDIO_INPUT_DEVICE='plughw:2,0'
export AUDIO_OUTPUT_DEVICE='plughw:2,0'  # or plughw:0,0 for HDMI
```

Use `plughw` rather than `hw` so ALSA can convert a device's native sample rate
to the API's 24 kHz PCM stream when necessary.

For other hardware, substitute the card names reported by the list command:

```sh
export AUDIO_INPUT_DEVICE='plughw:CARD=Microphone,DEV=0'
export AUDIO_OUTPUT_DEVICE='plughw:CARD=Speaker,DEV=0'
```

You can validate the microphone before deploying:

```sh
wendy device audio monitor --device wendyos-pi4.local
```

## 2. Supply the API key and deploy

Create an API key in the OpenAI dashboard. Copy the example environment file,
then replace its placeholder key:

```sh
cp .env.example .env
# Edit .env, then:
./run.sh --device wendyos-pi4.local
```

The helper loads `.env`, runs the newer Wendy CLI source in this repository,
and forces the registry deployment path with `--chunking off`. This is
necessary because the currently installed CLI and Pi agent (`2026.07.27`)
predate environment propagation on the chunk-diff deployment path, completed
on `2026.07.29`. The older registry path already supports injected environment
variables. The `.env` file is gitignored, and the key is injected at deployment
time rather than stored in the repository or container image.

With a newer installed Wendy CLI, the equivalent manual flow is:

```sh
set -a
source .env
set +a
wendy run --chunking off --device wendyos-pi4.local
```

When the log says `Ready — listening for your voice`, speak normally. Semantic
VAD ends the turn automatically and the reply plays through
`AUDIO_OUTPUT_DEVICE`. Press Ctrl-C to stop the attached log stream.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `OPENAI_API_KEY` | required | OpenAI API credential |
| `AUDIO_INPUT_DEVICE` | `plughw:2,0` | ALSA capture PCM |
| `AUDIO_OUTPUT_DEVICE` | `plughw:2,0` | ALSA playback PCM |
| `OPENAI_REALTIME_MODEL` | `gpt-realtime-2.1` | Realtime model |
| `OPENAI_VOICE` | `marin` | Assistant voice |
| `ASSISTANT_INSTRUCTIONS` | concise voice assistant | System instructions |
| `MUTE_INPUT_DURING_PLAYBACK` | `false` | Set to `true` for half-duplex echo protection (disables interruption) |
| `AUDIO_CHUNK_MS` | `100` | Microphone packet duration |
| `LOG_LEVEL` | `INFO` | Python log level |

Interruption/barge-in is enabled by default. Set
`MUTE_INPUT_DURING_PLAYBACK=true` only when a device has no echo cancellation
and speaker feedback is worse than losing interruption support.

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

This follows OpenAI's current [Realtime WebSocket
guide](https://developers.openai.com/api/docs/guides/realtime-websocket) and
[Realtime conversation event
flow](https://developers.openai.com/api/docs/guides/realtime-conversations).

The API key is appropriate here because this is a server-to-server connection
from a trusted device. Do not copy this pattern into browser code; browser and
mobile clients should use WebRTC and ephemeral credentials instead.
