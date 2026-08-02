# HelloAudio

Plays a WAV sound through the device speaker on a WendyOS device (e.g. a
Raspberry Pi 5). A minimal FastAPI app plays `assets/sleigh-bells.wav` once
on startup via `aplay`, and serves a page with a button to replay it.

## Run with Wendy

```bash
wendy run
```

This builds the container, deploys it to your default device, and opens
`http://<device>:3004` in your browser once the app is ready (see the
`postStart` hook in `wendy.json`).

## Endpoints

- `GET /` — web page with a "Play Sound" button
- `POST /play` — plays `assets/sleigh-bells.wav` via `aplay`
- `GET /health` — health check

## Run locally

```bash
pip install -r requirements.txt
python app.py
```

Requires ALSA (`aplay`) and a working audio output device on the host.

## Sound attribution

`assets/sleigh-bells.wav` is ["Sleigh bells.wav"](https://commons.wikimedia.org/wiki/File:Sleigh_bells.wav)
by The Midnite Wolf, via Wikimedia Commons, dedicated to the public domain
under [CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/).
