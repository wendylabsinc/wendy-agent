"""
WendyOS GPIO Control MCP App
=============================

An MCP server that exposes GPIO pin control via the Model Context Protocol.
Deploy with `wendy run` — the `gpio` entitlement mounts /dev/gpiochip* and the
`mcp` entitlement registers this server so its tools are proxied through
`wendy mcp serve`.  The `ui://gpio/control` resource is forwarded as
`ui://app/gpio-control/gpio/control` so the host can render a live pin-control
panel in-chat.

# FastMCP _meta.ui pattern (Tasks 10/11/12 reference)
# ======================================================
# Confirmed against FastMCP 1.27.2 (mcp>=1.9.4):
#
#   @mcp.tool(meta={"ui": {"resourceUri": "ui://gpio/control"}})
#
# The `meta` kwarg is passed directly through to the MCP protocol Tool object
# and appears as `_meta` in the tools/list response consumed by the Wendy
# agent.  Similarly:
#
#   @mcp.resource("ui://gpio/control", mime_type="text/html")
#
# The `mime_type` kwarg is accepted and serialised as `mimeType` in the
# resources/list response.  No low-level fallback is needed — both are
# first-class FastMCP kwargs.

# The gpiod import is intentionally deferred into each tool function so that
# this module compiles (py_compile) and imports cleanly on machines where
# libgpiod / the gpiod Python bindings are not installed.  Tools return a
# structured {"error": "gpio unavailable: ..."} response when the library is
# missing instead of raising an ImportError at startup.
"""

import os
from pathlib import Path

import uvicorn
from mcp.server.fastmcp import FastMCP

MCP_PORT = int(os.environ.get("MCP_PORT", 3002))

# Comma-separated BCM/line offsets, e.g. "17,27,22"
_GPIO_PINS_RAW = os.environ.get("GPIO_PINS", "17,27,22")
_CONFIGURED_PINS: list[int] = [
    int(p.strip()) for p in _GPIO_PINS_RAW.split(",") if p.strip().isdigit()
]

_CHIP_PATH = "/dev/gpiochip0"

mcp = FastMCP("gpio-control")

# Read the UI HTML once at startup so the resource handler is a simple closure.
_GPIO_HTML = (Path(__file__).parent / "static" / "gpio.html").read_text()


# ---------------------------------------------------------------------------
# Resource: UI page
# ---------------------------------------------------------------------------

@mcp.resource("ui://gpio/control", mime_type="text/html;profile=mcp-app")
def gpio_control_ui() -> str:
    """Return the GPIO control panel HTML page."""
    return _GPIO_HTML


# ---------------------------------------------------------------------------
# Tool: list_pins
#   meta={"ui": {"resourceUri": "ui://gpio/control"}} tells the Wendy MCP
#   proxy that this tool is associated with the in-chat UI page, so the host
#   can surface an "Open app" button and render the panel inline.
# ---------------------------------------------------------------------------

@mcp.tool(meta={"ui": {"resourceUri": "ui://gpio/control"}})
def list_pins() -> list:
    """
    Return the current value of all configured GPIO pins.

    Returns a list of {"pin": <n>, "value": 0|1} dicts.  If a pin cannot be
    read (e.g. hardware unavailable) its entry contains {"pin": <n>, "error":
    "<message>"} instead of a "value" key.
    """
    try:
        import gpiod  # noqa: PLC0415 — deferred to avoid import-time failure
    except ImportError as exc:
        return [{"pin": p, "error": f"gpio unavailable: {exc}"} for p in _CONFIGURED_PINS]

    results = []
    for pin in _CONFIGURED_PINS:
        try:
            with gpiod.request_lines(
                _CHIP_PATH,
                consumer="wendy-gpio-read",
                config={pin: gpiod.LineSettings(direction=gpiod.line.Direction.INPUT)},
            ) as req:
                val = req.get_value(pin)
                # gpiod.line.Value is an IntEnum; cast to plain int for JSON.
                results.append({"pin": pin, "value": int(val)})
        except Exception as exc:  # noqa: BLE001
            results.append({"pin": pin, "error": str(exc)})
    return results


# ---------------------------------------------------------------------------
# Tool: read_pin
# ---------------------------------------------------------------------------

@mcp.tool()
def read_pin(pin: int) -> dict:
    """
    Read the current value of a single GPIO pin.

    Args:
        pin: BCM/line offset to read (must be in the configured set).

    Returns {"pin": n, "value": 0|1} or {"error": "<message>"}.
    """
    if pin not in _CONFIGURED_PINS:
        return {"error": f"pin {pin} is not in the configured set {_CONFIGURED_PINS}"}

    try:
        import gpiod  # noqa: PLC0415
    except ImportError as exc:
        return {"error": f"gpio unavailable: {exc}"}

    try:
        with gpiod.request_lines(
            _CHIP_PATH,
            consumer="wendy-gpio-read",
            config={pin: gpiod.LineSettings(direction=gpiod.line.Direction.INPUT)},
        ) as req:
            val = req.get_value(pin)
            return {"pin": pin, "value": int(val)}
    except Exception as exc:  # noqa: BLE001
        return {"error": str(exc)}


# ---------------------------------------------------------------------------
# Tool: set_pin
# ---------------------------------------------------------------------------

@mcp.tool()
def set_pin(pin: int, value: int) -> dict:
    """
    Drive a GPIO pin high (1) or low (0).

    Args:
        pin:   BCM/line offset to drive (must be in the configured set).
        value: 1 for high, 0 for low.

    Returns {"pin": n, "value": v} on success, or {"error": "<message>"}.
    """
    if pin not in _CONFIGURED_PINS:
        return {"error": f"pin {pin} is not in the configured set {_CONFIGURED_PINS}"}
    if value not in (0, 1):
        return {"error": f"value must be 0 or 1, got {value!r}"}

    try:
        import gpiod  # noqa: PLC0415
    except ImportError as exc:
        return {"error": f"gpio unavailable: {exc}"}

    try:
        output_value = gpiod.line.Value.ACTIVE if value == 1 else gpiod.line.Value.INACTIVE
        with gpiod.request_lines(
            _CHIP_PATH,
            consumer="wendy-gpio-write",
            config={pin: gpiod.LineSettings(
                direction=gpiod.line.Direction.OUTPUT,
                output_value=output_value,
            )},
        ) as req:
            req.set_value(pin, output_value)
            return {"pin": pin, "value": value}
    except Exception as exc:  # noqa: BLE001
        return {"error": str(exc)}


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    uvicorn.run(mcp.streamable_http_app(), host="0.0.0.0", port=MCP_PORT)
