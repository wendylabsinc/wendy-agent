"""
WendyOS Security Camera MCP App
================================

An MCP server that exposes a live security camera feed via the Model Context
Protocol.  Deploy with `wendy run` — the `mcp` entitlement in wendy.json
registers this server so its tools are proxied through `wendy mcp serve`, and
the `ui://camera/feed` resource is forwarded as `ui://app/security-cam/camera/feed`
so the host can render it in-chat via `app_open`.

# FastMCP _meta.ui pattern (Tasks 10/11/12 reference)
# ======================================================
# Confirmed against FastMCP 1.27.2 (mcp>=1.9.4):
#
#   @mcp.tool(meta={"ui": {"resourceUri": "ui://camera/feed"}})
#
# The `meta` kwarg is passed directly through to the MCP protocol Tool object
# and appears as `_meta` in the tools/list response consumed by the Wendy
# agent.  Similarly:
#
#   @mcp.resource("ui://camera/feed", mime_type="text/html")
#
# The `mime_type` kwarg is accepted and serialised as `mimeType` in the
# resources/list response.  No low-level fallback is needed — both are
# first-class FastMCP kwargs.
"""

import base64
import glob
import os
from pathlib import Path

import cv2
import uvicorn
from mcp.server.fastmcp import FastMCP

MCP_PORT = int(os.environ.get("MCP_PORT", 3000))

mcp = FastMCP("security-cam")

# Read the UI HTML once at startup so the resource handler is a simple closure.
_CAM_HTML = (Path(__file__).parent / "static" / "cam.html").read_text()


# ---------------------------------------------------------------------------
# Resource: UI page
# ---------------------------------------------------------------------------

@mcp.resource("ui://camera/feed", mime_type="text/html;profile=mcp-app")
def camera_feed_ui() -> str:
    """Return the live camera viewer HTML page."""
    return _CAM_HTML


# ---------------------------------------------------------------------------
# Tool: list_cameras
# ---------------------------------------------------------------------------

@mcp.tool()
def list_cameras() -> list:
    """List available camera device paths on the host (e.g. /dev/video0)."""
    return sorted(glob.glob("/dev/video*"))


# ---------------------------------------------------------------------------
# Tool: snapshot
#   meta={"ui": {"resourceUri": "ui://camera/feed"}} tells the Wendy MCP
#   proxy that this tool is associated with the in-chat UI page, so the host
#   can surface an "Open app" button and render the feed inline.
# ---------------------------------------------------------------------------

@mcp.tool(meta={"ui": {"resourceUri": "ui://camera/feed"}})
def snapshot() -> dict:
    """
    Capture a single JPEG frame from the default camera (index 0).

    Returns {"mime": "image/jpeg", "b64": "<base64-encoded JPEG>"}
    on success, or {"error": "<message>"} on failure.
    """
    cap = cv2.VideoCapture(0)
    if not cap.isOpened():
        return {"error": "Failed to open camera at index 0"}

    try:
        ok, frame = cap.read()
        if not ok or frame is None:
            return {"error": "Failed to read a frame from the camera"}

        ok, buf = cv2.imencode(".jpg", frame)
        if not ok:
            return {"error": "Failed to encode frame as JPEG"}

        return {
            "mime": "image/jpeg",
            "b64": base64.b64encode(buf.tobytes()).decode("ascii"),
        }
    finally:
        cap.release()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    uvicorn.run(mcp.streamable_http_app(), host="0.0.0.0", port=MCP_PORT)
