import json
import os
import signal
import time
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
if [ -n "$FAKE_STDIN" ]; then
  if [ -t 0 ]; then tty=tty; else tty=notty; fi
  echo "$tty:$(cat)" > "$FAKE_STDIN"
fi
if [ -n "$FAKE_SLEEP" ]; then echo $$ > "$FAKE_SLEEP"; exec sleep 30; fi
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
        return {**os.environ, "WENDY_EVAL_SCENARIO": str(path), "FAKE_MARKER": str(self.marker), "FAKE_SLEEP": "",
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

    def test_real_runs_get_no_stdin(self):
        env = {**self.env(), "FAKE_STDIN": str(self.root / "stdin")}
        result = subprocess.run([str(self.shim), "device", "list"], env=env, cwd="/",
                                input="typed\n", capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / "stdin").read_text().strip(), "notty:")

    def test_canned_call_is_logged_even_if_stdout_is_closed(self):
        self.capture(["wifi", "connect"], "connected\n", 3)
        r, w = os.pipe()
        os.close(r)
        proc = subprocess.run([str(self.shim), "wifi", "connect", "X"], env=self.env(), cwd="/",
                              stdout=w, stderr=subprocess.PIPE, text=True, timeout=20)
        os.close(w)
        self.assertEqual(proc.returncode, 3)
        self.assertNotIn("Traceback", proc.stderr)
        self.assertEqual(self.records()[0]["exit_code"], 3)

    def test_interrupted_real_run_is_logged(self):
        sleeper = self.root / "pid"
        env = self.env()
        env["FAKE_SLEEP"] = str(sleeper)
        proc = subprocess.Popen([str(self.shim), "device", "list"], env=env, cwd="/",
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.addCleanup(proc.kill)
        deadline = time.time() + 10
        while not (sleeper.exists() and sleeper.read_text().strip()) and time.time() < deadline:
            time.sleep(0.05)
        pid = int(sleeper.read_text())
        proc.send_signal(signal.SIGTERM)
        proc.communicate(timeout=10)
        self.assertEqual(self.records()[0]["exit_code"], 143)
        with self.assertRaises(OSError):
            for _ in range(50):
                os.kill(pid, 0)
                time.sleep(0.1)
            os.kill(pid, 0)

    def test_internal_errors_stay_out_of_agent_output(self):
        (self.root / "cli_safe.json").write_text("{not json")
        result = self.run_shim("device", "list")
        self.assertEqual((result.returncode, result.stdout, result.stderr), (0, "", ""))
        self.assertFalse(self.marker.exists())
        self.assertIn("error", self.records()[0])

    def test_bad_config_fails_closed(self):
        scenario = self.root / "scenario.json"
        env = self.env()
        scenario.write_text("{corrupt")
        result = subprocess.run([str(self.shim), "device", "list"], env=env, capture_output=True,
                                text=True, timeout=20)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(len(result.stderr.strip().splitlines()), 1)
        result = self.run_shim("device", "list", real_wendy="fake-wendy")
        self.assertEqual(result.returncode, 2)
        link = self.root / "link"
        link.symlink_to(self.shim)
        self.assertEqual(self.run_shim("device", "list", real_wendy=str(link)).returncode, 2)
        self.assertEqual(self.run_shim("device", "list", shim="").returncode, 2)
        self.assertFalse(self.marker.exists())

    def test_risky_shapes_never_run_real(self):
        for argv in (["--device", "X", "wifi", "connect", "Lab5G"], ["wifi", "connect", "--help"],
                     ["mcp", "setup"]):
            result = self.run_shim(*argv)
            self.assertEqual(result.returncode, 0, argv)
            self.assertFalse(self.marker.exists(), argv)
            self.assertIs(self.records()[-1]["canned"], True)


if __name__ == "__main__":
    unittest.main()
