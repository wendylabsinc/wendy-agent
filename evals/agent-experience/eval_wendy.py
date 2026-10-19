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
import signal
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


def _fail(text):
    print(f"eval_wendy: {text}", file=sys.stderr)
    return 2


def _load(path):
    """Return (scenario, None) or (None, message). Nothing runs on a bad config."""
    try:
        scenario = json.loads(Path(path).read_text())
    except (OSError, ValueError) as error:
        return None, f"cannot read the scenario file: {error}"
    if not isinstance(scenario, dict):
        return None, "the scenario file is not an object"
    for key in ("real_wendy", "cli_log", "shim"):
        if not isinstance(scenario.get(key), str) or not scenario[key]:
            return None, f"the scenario has no {key}"
    real = scenario["real_wendy"]
    if not os.path.isabs(real):
        return None, "real_wendy must be an absolute path"
    if os.path.realpath(real) == os.path.realpath(scenario["shim"]):
        return None, "real_wendy points at the shim"
    return scenario, None


_SIGNALS = (signal.SIGTERM, signal.SIGINT, signal.SIGHUP)


def _run_real(real, argv):
    """Run the real binary with no stdin, forwarding termination signals to it."""
    child = subprocess.Popen([real, *argv], stdin=subprocess.DEVNULL)

    def forward(signum, _frame):
        try:
            child.send_signal(signum)
        except OSError:
            pass

    previous = {sig: signal.signal(sig, forward) for sig in _SIGNALS}
    try:
        while True:
            try:
                code = child.wait()
                break
            except KeyboardInterrupt:
                continue
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)
    return code if code >= 0 else 128 - code


def _emit(capture):
    """Write the canned output; a closed stdout or stderr must not cost the exit code."""
    for stream, key in ((sys.stdout, "stdout"), (sys.stderr, "stderr")):
        try:
            stream.write(capture.get(key) or "")
            stream.flush()
        except (OSError, ValueError):
            try:
                os.dup2(os.open(os.devnull, os.O_WRONLY), stream.fileno())
            except (OSError, ValueError):
                pass


def main(argv):
    path = os.environ.get("WENDY_EVAL_SCENARIO")
    if not path:
        print("eval wrapper used outside a scenario", file=sys.stderr)
        return 2
    scenario, problem = _load(path)
    if problem:
        return _fail(problem)
    real = scenario["real_wendy"]
    if argv[:2] == ["mcp", "serve"]:
        return run_proxy(real, argv, scenario, sys.stdin.buffer, sys.stdout.buffer)
    if cli_path(argv)[:1] == ["chat"]:
        os.execve(real, [real, *argv], {**os.environ, "WENDY_CHAT_MCP_COMMAND": scenario["shim"]})
    t = time.time()
    error = None
    try:
        safe = json.loads(Path(scenario["cli_safe"]).read_text()).get("read_only") or []
        run_real = cli_allowed(argv, safe) and not _blocked(argv, scenario.get("block") or [])
    except Exception as exc:  # fail closed, and keep the text from the agent
        run_real, error = False, f"policy failed: {exc!r}"
    if run_real:
        code = None
        try:
            code = _run_real(real, argv)
        finally:
            _log(scenario["cli_log"], {"argv": argv, "canned": False, "captured": None,
                                       "exit_code": code, "t": t})
        return code
    capture = None
    try:
        capture = load_cli_capture(scenario["canned_dir"], scenario["canned_set"], cli_path(argv))
    except Exception as exc:
        error = (error + "; " if error else "") + f"capture lookup failed: {exc!r}"
    code = (capture or {}).get("exit_code") or 0
    record = {"argv": argv, "canned": True, "captured": capture is not None, "exit_code": code, "t": t}
    if error:
        record["error"] = error
    _log(scenario["cli_log"], record)  # log first: a closed stdout can't lose the record
    if capture:
        _emit(capture)
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
