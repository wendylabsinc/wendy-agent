import os
from pathlib import Path
import sys
import time
import unittest

from mcp_client import StdioMCP

FAKE = str(Path(__file__).resolve().parent / "fake_mcp_server.py")


def fake(**env):
    return StdioMCP([sys.executable, FAKE, "mcp", "serve"], env={**os.environ, **env}, timeout=5)


class McpClientTests(unittest.TestCase):
    def test_client_lists_paginated_tools_and_calls(self):
        with fake(FAKE_MCP_PAGES="2") as mcp:
            mcp.initialize()
            names = [t["name"] for t in mcp.list_tools()]
            self.assertEqual(sorted(names), ["container_exec", "read_thing", "wendy_tools", "write_thing"])
            result = mcp.call("read_thing", {"a": 1})
            self.assertIn("read_thing", result["content"][0]["text"])

    def test_client_times_out_on_silent_server(self):
        started = time.monotonic()
        with StdioMCP([sys.executable, FAKE, "mcp", "serve"],
                      env={**os.environ, "FAKE_MCP_SILENT": "1"}, timeout=1) as mcp:
            with self.assertRaises(TimeoutError):
                mcp.initialize()
        self.assertLess(time.monotonic() - started, 3)


if __name__ == "__main__":
    unittest.main()
