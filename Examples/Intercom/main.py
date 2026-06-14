"""
WendyOS Intercom MCP App
========================

An MCP server that implements a push-to-talk intercom: the host-side UI
(served as a ui:// resource) captures microphone audio in the browser and
sends it base64-encoded to the `play_audio` tool, which plays the clip on
the WendyOS device speaker via ALSA (`aplay`).

Deploy with `wendy run` — the `audio` entitlement wires /dev/snd (and
PipeWire if present), and the `mcp` entitlement registers this server so its
tools are proxied through `wendy mcp serve`.

# FastMCP _meta.ui pattern (mirrors SecurityCam / Task 10)
# ==========================================================
# Confirmed against FastMCP 1.27.2 (mcp>=1.9.4):
#
#   @mcp.tool(meta={"ui": {"resourceUri": "ui://intercom/app"}})
#
# The `meta` kwarg flows through to the MCP protocol Tool object and appears
# as `_meta` in the tools/list response consumed by the Wendy agent.
#
#   @mcp.resource("ui://intercom/app", mime_type="text/html",
#                 meta={"ui": {"permissions": ["microphone"]}})
#
# FastMCP 1.27.2 DOES support `meta=` on resources (confirmed by inspecting
# FastMCP._resource_manager).  The `permissions` list signals to the MCP
# Apps host that this UI resource requires microphone access, so the host
# can grant the permission to the sandboxed iframe before loading it.
"""

import base64
import os
import re
import shutil
import subprocess
import tempfile
from pathlib import Path

import uvicorn
from mcp.server.fastmcp import FastMCP

MCP_PORT = int(os.environ.get("MCP_PORT", 3001))

mcp = FastMCP("intercom")

# Read the UI HTML once at startup.
_INTERCOM_HTML = (Path(__file__).parent / "static" / "intercom.html").read_text()


# ---------------------------------------------------------------------------
# Resource: UI page
#   meta={"ui": {"permissions": ["microphone"]}} signals to the MCP Apps
#   host that this UI page needs microphone access inside its sandboxed
#   iframe.  FastMCP 1.27.2 serialises the `meta` kwarg as `_meta` in the
#   resources/list response.
# ---------------------------------------------------------------------------

@mcp.resource(
    "ui://intercom/app",
    mime_type="text/html",
    meta={"ui": {"permissions": ["microphone"]}},
)
def intercom_ui() -> str:
    """Return the push-to-talk intercom HTML page."""
    return _INTERCOM_HTML


# ---------------------------------------------------------------------------
# Tool: list_audio_devices
# ---------------------------------------------------------------------------

@mcp.tool()
def list_audio_devices() -> list:
    """
    List available ALSA playback devices on the host.

    Runs `aplay -l` and returns a list of device description strings.
    Returns an empty list if `aplay` is not installed or the command fails.
    """
    if not shutil.which("aplay"):
        return []
    try:
        result = subprocess.run(
            ["aplay", "-l"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        devices = []
        for line in result.stdout.splitlines():
            # Lines describing cards look like:
            #   card 0: PCH [HDA Intel PCH], device 0: ALC892 Analog [...]
            if line.startswith("card "):
                devices.append(line.strip())
        return devices
    except Exception as exc:
        return [f"error: {exc}"]


# ---------------------------------------------------------------------------
# Tool: play_audio
#   meta={"ui": {"resourceUri": "ui://intercom/app"}} tells the Wendy MCP
#   proxy that this tool is associated with the in-chat UI page so the host
#   can surface an "Open app" button.
# ---------------------------------------------------------------------------

@mcp.tool(meta={"ui": {"resourceUri": "ui://intercom/app"}})
def play_audio(data_b64: str, format: str = "wav") -> dict:
    """
    Play audio on the WendyOS device speaker.

    Parameters
    ----------
    data_b64 : str
        Base64-encoded audio data.
    format : str
        Audio format hint (e.g. "wav", "webm", "ogg", "mp3").
        Used to choose the right suffix so the player can identify the codec.
        Defaults to "wav".

    Returns
    -------
    {"ok": true} on success, {"error": "<message>"} on failure.

    The `audio` entitlement in wendy.json wires /dev/snd (ALSA) and
    PipeWire/PulseAudio socket if present.  Playback preference order:
      1. aplay  — for wav (fast, no extra deps)
      2. paplay — for wav via PulseAudio/PipeWire
      3. ffplay  — for any format (requires ffmpeg)
    """
    # Sanitise the format string to a safe suffix (letters/digits only).
    safe_fmt = re.sub(r"[^a-zA-Z0-9]", "", format) or "wav"

    # Decode the base64 payload.
    try:
        audio_bytes = base64.b64decode(data_b64)
    except Exception as exc:
        return {"error": f"base64 decode failed: {exc}"}

    if not audio_bytes:
        return {"error": "empty audio data"}

    # Write to a temp file; the suffix helps players identify the codec.
    tmp = tempfile.NamedTemporaryFile(suffix=f".{safe_fmt}", delete=False)
    try:
        tmp.write(audio_bytes)
        tmp.flush()
        tmp.close()
        return _play_file(tmp.name, safe_fmt)
    finally:
        try:
            os.unlink(tmp.name)
        except OSError:
            pass


def _play_file(path: str, fmt: str) -> dict:
    """Try available players in order of preference."""
    # 1. aplay — best for wav, no extra libs required.
    if fmt == "wav" and shutil.which("aplay"):
        return _run_player(["aplay", "-q", path])

    # 2. paplay — PulseAudio/PipeWire, also good for wav.
    if fmt == "wav" and shutil.which("paplay"):
        return _run_player(["paplay", path])

    # 3. ffplay — handles virtually any format.
    if shutil.which("ffplay"):
        return _run_player(
            ["ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", path]
        )

    # 4. Fall back to aplay even for non-wav if it's the only option.
    if shutil.which("aplay"):
        return _run_player(["aplay", "-q", path])

    return {
        "error": (
            "No audio player found. Install alsa-utils (aplay), "
            "pulseaudio-utils (paplay), or ffmpeg (ffplay)."
        )
    }


def _run_player(cmd: list) -> dict:
    """Run a player command and return success/error dict."""
    try:
        result = subprocess.run(cmd, capture_output=True, timeout=30)
        if result.returncode == 0:
            return {"ok": True}
        stderr = result.stderr.decode("utf-8", errors="replace").strip()
        return {"error": f"player exited {result.returncode}: {stderr or '(no output)'}"}
    except subprocess.TimeoutExpired:
        return {"error": "player timed out after 30 s"}
    except FileNotFoundError:
        return {"error": f"player not found: {cmd[0]}"}
    except Exception as exc:
        return {"error": str(exc)}


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    uvicorn.run(mcp.streamable_http_app(), host="0.0.0.0", port=MCP_PORT)
