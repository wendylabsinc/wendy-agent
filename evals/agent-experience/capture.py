#!/usr/bin/env python3
"""Capture real Wendy output (MCP tool reply or CLI run) into canned/<set>/, scrubbed."""
import argparse
import hashlib
import json
import subprocess
import sys
from pathlib import Path

from canned import scrub
from mcp_client import StdioMCP

DEFAULT_DIR = Path(__file__).resolve().parent / "canned"


def device_kind(selector):
    if selector.startswith("vm:"):
        return "simulator"
    if selector.startswith("cloud://"):
        return "cloud"
    return "physical"


def _version(wendy):
    out = subprocess.run([wendy, "--version"], capture_output=True, text=True, timeout=30)
    return (out.stdout or out.stderr).strip()


def _sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def capture_mcp(args, canned_dir):
    argv = [args.wendy, "mcp", "serve", "--tool-groups", "all", "--device", args.device]
    with StdioMCP(argv, timeout=120) as mcp:
        mcp.initialize()
        tools = {t["name"]: t for t in mcp.list_tools()}
        if args.tool not in tools:
            print(f"unknown tool {args.tool}", file=sys.stderr)
            return 2
        read_only = (tools[args.tool].get("annotations") or {}).get("readOnlyHint") is True
        if not read_only and not args.yes:
            print(f"{args.tool} is not read-only; pass --yes to run it for real", file=sys.stderr)
            return 2
        result = mcp.call(args.tool, json.loads(args.args or "{}"))
    path = Path(canned_dir) / args.set / f"{args.tool}.json"
    data = {"tool": args.tool, "replies": []}
    if path.is_file():
        data = json.loads(path.read_text())
    data["captured_with"] = {"wendy_version": _version(args.wendy), "wendy_sha256": _sha256(args.wendy),
                             "device_kind": device_kind(args.device)}
    data["replies"].append(scrub(result))
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2) + "\n")
    print(path)
    print("Replace values that came from your arguments with {{args.NAME}} placeholders.")
    return 0


def capture_cli(args, canned_dir):
    words = args.cli
    run = subprocess.run([args.wendy, *words], capture_output=True, text=True, timeout=120)
    path = Path(canned_dir) / args.set / "cli" / ("-".join(words[:2]) + ".json")
    data = {"argv_prefix": words[:2], "stdout": scrub(run.stdout), "stderr": scrub(run.stderr),
            "exit_code": run.returncode}
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2) + "\n")
    print(path)
    return 0


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--wendy", required=True)
    p.add_argument("--device", required=True)
    p.add_argument("--set", required=True)
    mode = p.add_mutually_exclusive_group(required=True)
    mode.add_argument("--tool")
    mode.add_argument("--cli", nargs="+", metavar="WORD")
    p.add_argument("--args")
    p.add_argument("--yes", action="store_true")
    p.add_argument("--canned-dir", default=str(DEFAULT_DIR))
    args = p.parse_args(argv)
    if args.cli:
        return capture_cli(args, args.canned_dir)
    return capture_mcp(args, args.canned_dir)


if __name__ == "__main__":
    sys.exit(main())
