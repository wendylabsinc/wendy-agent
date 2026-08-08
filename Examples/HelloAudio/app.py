#!/usr/bin/env python3
"""
HelloAudio — plays a WAV file through the device speaker.

Serves a minimal web page with a "Play" button and a POST /play endpoint
that plays assets/sleigh-bells.wav via `pw-play` (PipeWire), falling back
to `aplay` (ALSA) if PipeWire isn't available. PipeWire is preferred
because it routes through the host's WirePlumber session graph, which can
reach devices — like a paired Bluetooth speaker — that raw ALSA can't.
"""
import asyncio
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


class PlaybackError(Exception):
    pass


async def _play() -> None:
    """Start the player and confirm it survives its first second.

    A player that can't reach an output (no PipeWire socket, ALSA device
    busy/unsupported) exits almost immediately, so a short grace-period
    check turns "silently spawned a dead process" into a real error. A
    failure after the grace period goes undetected — acceptable for a demo.
    """
    player = "pw-play" if shutil.which("pw-play") else "aplay"
    proc = subprocess.Popen(
        [player, str(_sound_file)],
        stderr=subprocess.PIPE,
        text=True,
    )
    await asyncio.sleep(1.0)
    if proc.poll() is not None and proc.returncode != 0:
        err = proc.stderr.read().strip() if proc.stderr else ""
        raise PlaybackError(f"{player} exited with code {proc.returncode}: {err or 'no error output'}")
    logger.info("Playing %s via %s", _sound_file.name, player)


@app.on_event("startup")
async def play_on_startup():
    if _sound_file.exists():
        try:
            await _play()
        except (FileNotFoundError, PlaybackError) as e:
            logger.error("Startup playback failed: %s", e)


@app.post("/play")
async def play_sound():
    if not _sound_file.exists():
        return JSONResponse(content={"error": "sound file not found"}, status_code=404)
    try:
        await _play()
        return JSONResponse(content={"status": "playing", "file": _sound_file.name})
    except FileNotFoundError:
        logger.error("no audio player (pw-play/aplay) found")
        return JSONResponse(content={"error": "no audio player found on this device"}, status_code=500)
    except PlaybackError as e:
        logger.error("Failed to play sound: %s", e)
        return JSONResponse(content={"error": str(e)}, status_code=500)
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
