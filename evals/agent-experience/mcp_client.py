"""Minimal stdio MCP client: newline-delimited JSON-RPC 2.0, standard library only."""
import json
import os
import select
import subprocess
import time


class StdioMCP:
    def __init__(self, argv, env=None, cwd=None, timeout=30):
        self.argv, self.env, self.cwd, self.timeout = argv, env, cwd, timeout
        self.proc = None
        self._next_id = 0
        self._buffer = b""

    def __enter__(self):
        self.proc = subprocess.Popen(self.argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=subprocess.DEVNULL, env=self.env, cwd=self.cwd)
        return self

    def __exit__(self, *exc):
        self.close()

    def close(self):
        proc, self.proc = self.proc, None
        if proc is None:
            return
        try:
            proc.stdin.close()
        except OSError:
            pass
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        proc.stdout.close()

    def _send(self, message):
        self.proc.stdin.write((json.dumps(message) + "\n").encode())
        self.proc.stdin.flush()

    def _read_line(self, deadline):
        fd = self.proc.stdout.fileno()
        while b"\n" not in self._buffer:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([fd], [], [], remaining)[0]:
                raise TimeoutError(f"no MCP reply within {self.timeout:g}s")
            chunk = os.read(fd, 65536)
            if not chunk:
                raise RuntimeError("MCP server closed its output")
            self._buffer += chunk
        line, self._buffer = self._buffer.split(b"\n", 1)
        return line

    def _request(self, method, params=None):
        self._next_id += 1
        message = {"jsonrpc": "2.0", "id": self._next_id, "method": method}
        if params is not None:
            message["params"] = params
        self._send(message)
        deadline = time.monotonic() + self.timeout
        while True:
            line = self._read_line(deadline).strip()
            if not line:
                continue
            reply = json.loads(line)
            if reply.get("id") != self._next_id or "method" in reply:
                continue  # server notification or request; not our response
            if "error" in reply:
                raise RuntimeError(f"MCP error for {method}: {reply['error']}")
            return reply.get("result", {})

    def initialize(self):
        result = self._request("initialize", {
            "protocolVersion": "2025-06-18", "capabilities": {},
            "clientInfo": {"name": "agent-experience-benchmark", "version": "1"}})
        self._send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        return result

    def list_tools(self):
        tools, cursor = [], None
        while True:
            result = self._request("tools/list", {"cursor": cursor} if cursor else {})
            tools.extend(result.get("tools", []))
            cursor = result.get("nextCursor")
            if not cursor:
                return tools

    def call(self, name, arguments):
        return self._request("tools/call", {"name": name, "arguments": arguments})
