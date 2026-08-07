#!/usr/bin/env python3
"""
HelloAudio — plays a WAV file through the device speaker.

Serves a minimal web page with a "Play" button and a POST /play endpoint
that plays assets/sleigh-bells.wav via `pw-play` (PipeWire), falling back
to `aplay` (ALSA) if PipeWire isn't available. PipeWire is preferred
because it routes through the host's WirePlumber session graph, which can
reach devices — like a paired Bluetooth speaker — that raw ALSA can't.
"""
import logging
import os
import shutil
import subprocess
from pathlib import Path

from fastapi import FastAPI
from fastapi.responses import FileResponse, JSONResponse

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = FastAPI()

_app_dir = Path(__file__).parent
_sound_file = _app_dir / "assets" / "sleigh-bells.wav"


def _play() -> None:
    if shutil.which("pw-play"):
        subprocess.Popen(["pw-play", str(_sound_file)])
        logger.info("Playing %s via pw-play", _sound_file.name)
    else:
        subprocess.Popen(["aplay", str(_sound_file)])
        logger.info("Playing %s via aplay", _sound_file.name)


@app.on_event("startup")
async def play_on_startup():
    if _sound_file.exists():
        _play()


@app.post("/play")
async def play_sound():
    if not _sound_file.exists():
        return JSONResponse(content={"error": "sound file not found"}, status_code=404)
    try:
        _play()
        return JSONResponse(content={"status": "playing", "file": _sound_file.name})
    except FileNotFoundError:
        logger.error("no audio player (pw-play/aplay) found")
        return JSONResponse(content={"error": "no audio player found on this device"}, status_code=500)
    except Exception as e:
        logger.error("Failed to play sound: %s", e)
        return JSONResponse(content={"error": str(e)}, status_code=500)


@app.get("/health")
async def health():
    return JSONResponse(content={"status": "healthy"})


@app.get("/")
async def root():
    return FileResponse(_app_dir / "index.html", media_type="text/html")


if __name__ == "__main__":
    import uvicorn

    port = int(os.environ.get("PORT", 3004))
    uvicorn.run(app, host="0.0.0.0", port=port)
