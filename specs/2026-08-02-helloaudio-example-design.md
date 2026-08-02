# HelloAudio example app — design

## Purpose

Add `Examples/HelloAudio`, a minimal WendyOS example app demonstrating audio
playback, alongside the existing `Examples/Hello*` templates. Deployable via
`wendy run` like `HelloPython`/`HelloGPU`/`HelloONNX`.

## Sound sample

[Gong or bell vibrant (short).ogg](https://commons.wikimedia.org/wiki/File:Gong_or_bell_vibrant_(short).ogg)
— CC0 1.0 Universal (public domain dedication), 5.6s, originally sourced from
PDSounds by user Stephan, uploaded to Wikimedia Commons by Ocaasi. Downloaded
and converted to WAV, committed as a static asset (`sounds/gong.wav`).
Attribution + license noted in the README even though CC0 doesn't require it.

## Structure

```
Examples/HelloAudio/
  wendy.json
  Dockerfile
  requirements.txt
  app.py
  sounds/gong.wav
  README.md
```

- **wendy.json** — `appId: sh.wendy.examples.helloaudio`, `platform: linux`,
  `language: python`, `entitlements: [{"type": "audio"}]`. The `audio`
  entitlement (see `go/internal/agent/oci/entitlements.go`) binds `/dev/snd`
  and wires up PipeWire/PulseAudio — no extra device passthrough needed.
- **Dockerfile** — `python:3.11-slim` base; installs `libportaudio2`,
  `libasound2-dev`, `portaudio19-dev`, `alsa-utils` (same packages as the
  `PythonAI` example, the existing audio-entitlement precedent); copies
  `requirements.txt`, `app.py`, `sounds/`; creates a non-root user added to
  the `audio` group; `CMD ["python", "app.py"]`.
- **requirements.txt** — `sounddevice`, `soundfile`.
- **app.py** — one-shot script (no HTTP server, matching `HelloONNX`/
  `HelloGPU` style): prints available audio output devices, loads
  `sounds/gong.wav` via `soundfile`, plays it via `sounddevice.play()` +
  `sounddevice.wait()`, prints a success message, exits 0. Exits non-zero
  with a clear message if no output device is available.
- **README.md** — Quick Start (local `python app.py` + `wendy run`), sample
  attribution/license, security notes (mirrors `HelloPython`'s README
  sections).

## Out of scope

No HTTP server, no recording/microphone capture (that's `PythonAI`'s job),
no dynamic sample selection — this is a single fixed playback demo.
