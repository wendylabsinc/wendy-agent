import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest

from fake_mcp_server import TOOLS
from mcp_client import StdioMCP
import eval_wendy

HERE = Path(__file__).resolve().parent
FAKE = str(HERE / "fake_mcp_server.py")
SAFE = [["device", "list"], ["docs"]]
FAKE_REAL = """#!/bin/sh
echo run > "$FAKE_MARKER"
printf '{"argv":['
sep=
for a in "$@"; do printf '%s"%s"' "$sep" "$a"; sep=,; done
printf '],"chat":"%s"}\\n' "$WENDY_CHAT_MCP_COMMAND"
"""


class WrapperTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        (self.root / "tools.json").write_text(json.dumps(TOOLS))
        (self.root / "cli_safe.json").write_text(json.dumps({"read_only": SAFE}))
        self.marker = self.root / "marker"
        self.cli_log = self.root / "cli.jsonl"
        self.proxy_log = self.root / "proxy.jsonl"
        self.shim = eval_wendy.write_shim(self.root)
        fake = self.root / "fake-wendy"
        fake.write_text(FAKE_REAL)
        fake.chmod(0o755)
        self.fake = str(fake)

    def env(self, real=None, **overrides):
        scenario = {"real_wendy": real or self.fake, "tools": str(self.root / "tools.json"),
                    "canned_dir": str(self.root / "canned"), "canned_set": "s", "canned": [],
                    "allow_real": [], "block": [], "context": {}, "proxy_log": str(self.proxy_log),
                    "cli_log": str(self.cli_log), "cli_safe": str(self.root / "cli_safe.json"),
                    "shim": str(self.shim), **overrides}
        path = self.root / "scenario.json"
        path.write_text(json.dumps(scenario))
        return {**os.environ, "WENDY_EVAL_SCENARIO": str(path), "FAKE_MARKER": str(self.marker),
                "PYTHONPATH": "/nonexistent"}

    def run_shim(self, *argv, **overrides):
        return subprocess.run([str(self.shim), *argv], env=self.env(**overrides), cwd="/",
                              capture_output=True, text=True, timeout=20)

    def records(self):
        if not self.cli_log.exists():
            return []
        return [json.loads(line) for line in self.cli_log.read_text().splitlines()]

    def capture(self, words, stdout, code):
        folder = self.root / "canned" / "s" / "cli"
        folder.mkdir(parents=True, exist_ok=True)
        (folder / "c.json").write_text(json.dumps(
            {"argv_prefix": words, "stdout": stdout, "stderr": "", "exit_code": code}))

    def test_shim_file(self):
        text = self.shim.read_text()
        self.assertTrue(text.startswith("#!/bin/sh\nexec "))
        self.assertIn(" -E ", text)
        self.assertTrue(self.shim.stat().st_mode & stat.S_IXUSR)

    def test_shim_routes_mcp_serve_to_proxy(self):
        with StdioMCP([str(self.shim), "mcp", "serve"], env=self.env(real=FAKE), timeout=10) as client:
            client.initialize()
            self.assertTrue(client.list_tools())
            client.call("read_thing", {})
        self.assertTrue(self.proxy_log.exists())
        self.assertEqual(json.loads(self.proxy_log.read_text().splitlines()[0])["tool"], "read_thing")

    def test_safe_cli_passes_through_and_is_recorded(self):
        result = self.run_shim("--json", "device", "list")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["argv"], ["--json", "device", "list"])
        record = self.records()[0]
        self.assertEqual(record["argv"], ["--json", "device", "list"])
        self.assertIs(record["canned"], False)
        self.assertEqual(record["exit_code"], 0)
        self.assertIsInstance(record["t"], float)

    def test_unsafe_cli_is_canned_by_default(self):
        result = self.run_shim("wifi", "connect", "Lab5G")
        self.assertEqual((result.returncode, result.stdout), (0, ""))
        self.assertFalse(self.marker.exists())
        record = self.records()[0]
        self.assertIs(record["canned"], True)
        self.assertIs(record["captured"], False)
        self.capture(["wifi", "connect"], "connected\n", 3)
        result = self.run_shim("wifi", "connect", "Lab5G")
        self.assertEqual((result.returncode, result.stdout), (3, "connected\n"))
        self.assertFalse(self.marker.exists())
        self.assertIs(self.records()[1]["captured"], True)

    def test_whole_word_allowlist(self):
        for argv in (["device", "listx"], ["device", "list-all"], ["device"]):
            self.assertFalse(eval_wendy.cli_allowed(argv, SAFE), argv)
        self.assertTrue(eval_wendy.cli_allowed(["--json", "device", "list", "-x"], SAFE))
        self.assertTrue(eval_wendy.cli_allowed(["--version"], SAFE))
        self.assertTrue(eval_wendy.cli_allowed(["--help"], SAFE))
        self.assertFalse(eval_wendy.cli_allowed(["--version", "x"], SAFE))
        self.assertFalse(eval_wendy.cli_allowed([], SAFE))

    def test_block_matchers_can_a_safe_command(self):
        for matcher in ({"cli": "device list"}, {"shell": r"wendy .*device list"}):
            self.marker.unlink(missing_ok=True)
            result = self.run_shim("device", "list", block=[{**matcher, "agents": ["x"]}])
            self.assertEqual(result.returncode, 0)
            self.assertFalse(self.marker.exists(), matcher)
            self.assertIs(self.records()[-1]["canned"], True)

    def test_chat_is_exec_with_mcp_override(self):
        result = self.run_shim("chat")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["chat"], str(self.shim))
        self.assertEqual(self.records(), [])

    def test_missing_scenario_env_refuses(self):
        env = {k: v for k, v in os.environ.items() if k != "WENDY_EVAL_SCENARIO"}
        result = subprocess.run([str(self.shim), "device", "list"], env=env, capture_output=True,
                                text=True, timeout=20)
        self.assertEqual(result.returncode, 2)
        self.assertIn("eval wrapper used outside a scenario", result.stderr)


if __name__ == "__main__":
    unittest.main()
