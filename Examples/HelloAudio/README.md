# HelloAudio

Plays a WAV sound through the device speaker on a WendyOS device (e.g. a
Raspberry Pi 5). A minimal FastAPI app plays `assets/sleigh-bells.wav` once
on startup via `pw-play` (PipeWire), falling back to `aplay` (ALSA) if
PipeWire isn't available, and serves a page with a button to replay it.

PipeWire is preferred because the `audio` entitlement connects the container
to the host's user-session PipeWire socket, which routes through WirePlumber
and can reach whatever sink is actually active — including a paired
Bluetooth speaker. The raw ALSA fallback (`/dev/snd`) only reaches the
device's built-in/wired outputs.

## Run with Wendy

```bash
wendy run
```

This builds the container, deploys it to your default device, and opens
`http://<device>:3004` in your browser once the app is ready (see the
`postStart` hook in `wendy.json`).

## Endpoints

- `GET /` — web page with a "Play Sound" button
- `POST /play` — plays `assets/sleigh-bells.wav` via `pw-play`, falling back
  to `aplay`; returns a 500 with the player's error output if playback fails
  to start (e.g. no PipeWire socket or no usable output device)
- `GET /health` — health check

## Run locally

```bash
pip install -r requirements.txt
python generate_sound.py
python app.py
```

Requires PipeWire (`pw-play`) or ALSA (`aplay`) and a working audio output
device on the host.

## The sound

`assets/sleigh-bells.wav` is synthesized by `generate_sound.py` (at image
build time, or by hand for local runs) — a deterministic, stdlib-only
sleigh-bell jingle. Nothing is downloaded or committed to the repo.
