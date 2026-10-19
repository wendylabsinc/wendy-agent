#!/usr/bin/env python3
"""The `wendy` shim for short tool-choice scenarios. It sits first on PATH and the prompt names it.

`mcp serve` goes to the recording proxy; `chat` runs the real binary with its MCP command pointed
back at the shim; every other subcommand is logged and runs for real only when it is on the
read-only allowlist and no block matcher hits. Anything else is canned, with no visible error (D7).
"""
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))  # the shim runs python -E and may start in any directory

import grading  # noqa: E402
from canned import load_cli_capture  # noqa: E402
from mcp_proxy import run_proxy  # noqa: E402
from tool_trace import cli_path  # noqa: E402


def write_shim(directory, python=sys.executable):
    path = Path(directory) / "wendy"
    path.write_text(f'#!/bin/sh\nexec "{python}" -E "{Path(__file__).resolve()}" "$@"\n')
    path.chmod(0o755)
    return path


def cli_allowed(argv, safe):
    if argv in (["--version"], ["--help"]):
        return True
    words = cli_path(argv)
    return any(words[:len(prefix)] == list(prefix) for prefix in safe if prefix)


def _blocked(argv, block):
    entries = [{"channel": "cli", "name": " ".join(cli_path(argv)), "arguments": argv,
                "source": "wrapper", "canned": False, "captured": None},
               {"channel": "shell", "name": "shell", "arguments": "wendy " + shlex.join(argv),
                "source": "wrapper", "canned": False, "captured": None}]
    for matcher in block:
        matcher = {k: v for k, v in matcher.items() if k != "agents"}  # the caller is unknown
        if any(grading.matches(matcher, entry) for entry in entries):
            return True
    return False


def _log(path, record):
    with open(path, "a", encoding="utf-8") as log:
        log.write(json.dumps(record) + "\n")
        log.flush()


def main(argv):
    path = os.environ.get("WENDY_EVAL_SCENARIO")
    if not path:
        print("eval wrapper used outside a scenario", file=sys.stderr)
        return 2
    scenario = json.loads(Path(path).read_text())
    real = scenario["real_wendy"]
    if argv[:2] == ["mcp", "serve"]:
        return run_proxy(real, argv, scenario, sys.stdin.buffer, sys.stdout.buffer)
    if cli_path(argv)[:1] == ["chat"]:
        os.execve(real, [real, *argv], {**os.environ, "WENDY_CHAT_MCP_COMMAND": scenario["shim"]})
    t = time.time()
    try:
        safe = json.loads(Path(scenario["cli_safe"]).read_text()).get("read_only") or []
        run_real = cli_allowed(argv, safe) and not _blocked(argv, scenario.get("block") or [])
    except Exception as error:  # fail closed
        print(f"eval_wendy: policy failed: {error!r}", file=sys.stderr)
        run_real = False
    if run_real:
        sys.stdout.flush()
        code = subprocess.run([real, *argv]).returncode
        code = code if code >= 0 else 128 - code
        _log(scenario["cli_log"], {"argv": argv, "canned": False, "captured": None, "exit_code": code, "t": t})
        return code
    capture = None
    try:
        capture = load_cli_capture(scenario["canned_dir"], scenario["canned_set"], cli_path(argv))
    except Exception as error:
        print(f"eval_wendy: capture lookup failed: {error!r}", file=sys.stderr)
    code = 0
    if capture:
        sys.stdout.write(capture.get("stdout") or "")
        sys.stderr.write(capture.get("stderr") or "")
        sys.stdout.flush()
        code = capture.get("exit_code") or 0
    _log(scenario["cli_log"], {"argv": argv, "canned": True, "captured": capture is not None,
                               "exit_code": code, "t": t})
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
