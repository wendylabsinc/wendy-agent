#!/usr/bin/env python3
"""
HelloAudio — plays a WAV file through the device speaker.

Serves a minimal web page with a "Play" button and a POST /play endpoint
that shells out to `aplay` (ALSA) to play assets/sleigh-bells.wav on
whatever playback device the OS picks by default.
"""
import logging
import os
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
    subprocess.Popen(["aplay", str(_sound_file)])
    logger.info("Playing %s", _sound_file.name)


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
        logger.error("aplay not found")
        return JSONResponse(content={"error": "aplay not found on this device"}, status_code=500)
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
