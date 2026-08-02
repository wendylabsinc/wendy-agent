# Hello Audio

A minimal Python example that plays a bundled sound sample through the
device's speaker, demonstrating WendyOS's `audio` entitlement.

## Features

- Enumerates available audio output devices
- Plays a bundled WAV sample with `sounddevice` + `soundfile`
- Dockerized for deployment via `wendy run`
- Non-root user for security

## Quick Start

### Run Locally

Requires PortAudio (`brew install portaudio` on macOS, or
`apt-get install portaudio19-dev` on Linux):

```bash
pip install -r requirements.txt
python app.py
```

You should hear a short gong/bell sound play through your default output
device.

### Run with Wendy

```bash
wendy run
```

WendyOS grants the container access to `/dev/snd` and PipeWire/PulseAudio
via the `audio` entitlement declared in `wendy.json` — no extra
configuration needed.

### Run with Docker

```bash
docker build -t hello-audio .
docker run --device /dev/snd hello-audio
```

## Sound Sample

`sounds/gong.wav` is converted from
[Gong or bell vibrant (short).ogg](https://commons.wikimedia.org/wiki/File:Gong_or_bell_vibrant_(short).ogg),
sourced from PDSounds by Stephan and uploaded to Wikimedia Commons by
Ocaasi. Licensed under
[CC0 1.0 Universal](https://creativecommons.org/publicdomain/zero/1.0/)
(public domain dedication) — free to use, modify, and redistribute without
attribution, though it's credited here as good practice.

## File Structure

```
.
├── app.py              # Main script: enumerates devices, plays the sample
├── sounds/gong.wav     # Bundled CC0 sound sample
├── requirements.txt    # Python dependencies
├── Dockerfile          # Docker configuration
├── wendy.json          # Wendy app manifest (requests the audio entitlement)
└── README.md           # This file
```

## Security Notes

- The Docker container runs as a non-root user, added to the `audio` group
  so it can reach `/dev/snd`
- Only necessary files are copied into the container
