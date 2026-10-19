#!/usr/bin/env python3
"""Fake `wendy` for tests: `mcp serve` speaks newline-delimited JSON-RPC; `--version` prints a version."""
import json
import os
import sys
import time

TOOLS = [{"name": name, "description": f"Fake {name}.",
          "inputSchema": {"type": "object", "properties": {}},
          "annotations": {"readOnlyHint": read_only}}
         for name, read_only in [("read_thing", True), ("write_thing", False),
                                 ("container_exec", False), ("wendy_tools", False)]]


def reply(id, result):
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": id, "result": result}) + "\n")
    sys.stdout.flush()


def handle(message):
    method, id, params = message.get("method"), message.get("id"), message.get("params") or {}
    if id is None:
        return
    if method == "initialize":
        reply(id, {"protocolVersion": params.get("protocolVersion", "2025-06-18"),
                   "capabilities": {"tools": {}}, "serverInfo": {"name": "fake-wendy", "version": "0.0.0"}})
    elif method == "tools/list":
        if os.environ.get("FAKE_MCP_PAGES") == "2":
            half = len(TOOLS) // 2
            if params.get("cursor") == "page2":
                reply(id, {"tools": TOOLS[half:]})
            else:
                reply(id, {"tools": TOOLS[:half], "nextCursor": "page2"})
        else:
            reply(id, {"tools": TOOLS})
    elif method == "tools/call":
        text = json.dumps({"tool": params.get("name"), "arguments": params.get("arguments")})
        reply(id, {"content": [{"type": "text", "text": text}], "isError": False})
    else:
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": id,
                                     "error": {"code": -32601, "message": "method not found"}}) + "\n")
        sys.stdout.flush()


def main():
    if "--version" in sys.argv[1:]:
        print("fake-wendy 0.0.0")
        return 0
    log = os.environ.get("FAKE_MCP_LOG")
    for line in sys.stdin:
        if not line.strip():
            continue
        if log:
            with open(log, "a") as f:
                f.write(line.strip() + "\n")
        if os.environ.get("FAKE_MCP_SILENT") == "1":
            continue
        handle(json.loads(line))
    return 0


if __name__ == "__main__":
    sys.exit(main())
