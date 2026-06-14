"""
WendyOS System Monitor MCP App
==============================

A lightweight MCP App: it streams small *scalar* device metrics (CPU, memory,
disk, load, uptime) and renders them as live gauges in-chat — no large payloads
(contrast with the camera sample, which returns multi-MB JPEGs). Deploy with
`wendy run`; the `mcp` entitlement registers it with the agent and the
`ui://sysmon/app` resource is forwarded as `ui://app/sysmon/sysmon/app` for
in-chat rendering.

Same FastMCP pattern as the other samples (confirmed against FastMCP 1.27.2):
  @mcp.resource("ui://sysmon/app", mime_type="text/html;profile=mcp-app")
  @mcp.tool(meta={"ui": {"resourceUri": "ui://sysmon/app"}})
"""

import os
import time
from pathlib import Path

import psutil
import uvicorn
from mcp.server.fastmcp import FastMCP

MCP_PORT = int(os.environ.get("MCP_PORT", 3003))

mcp = FastMCP("sysmon")

# Read the UI HTML once at startup so the resource handler is a simple closure.
_UI_HTML = (Path(__file__).parent / "static" / "sysmon.html").read_text()


@mcp.resource("ui://sysmon/app", mime_type="text/html;profile=mcp-app")
def sysmon_ui() -> str:
    """Return the system-monitor dashboard HTML page."""
    return _UI_HTML


@mcp.tool(meta={"ui": {"resourceUri": "ui://sysmon/app"}})
def stats() -> dict:
    """Return a snapshot of device metrics. All values are scalars (a few hundred
    bytes), so this is cheap to poll continuously for a live dashboard."""
    vm = psutil.virtual_memory()
    du = psutil.disk_usage("/")
    try:
        load1 = os.getloadavg()[0]
    except OSError:
        load1 = 0.0
    return {
        "cpu_percent": round(psutil.cpu_percent(interval=None), 1),
        "cpu_count": psutil.cpu_count() or 0,
        "mem_percent": round(vm.percent, 1),
        "mem_used_mb": vm.used // (1024 * 1024),
        "mem_total_mb": vm.total // (1024 * 1024),
        "disk_percent": round(du.percent, 1),
        "disk_used_gb": round(du.used / (1024 ** 3), 1),
        "disk_total_gb": round(du.total / (1024 ** 3), 1),
        "load1": round(load1, 2),
        "processes": len(psutil.pids()),
        "uptime_s": int(time.time() - psutil.boot_time()),
    }


if __name__ == "__main__":
    uvicorn.run(mcp.streamable_http_app(), host="0.0.0.0", port=MCP_PORT)
