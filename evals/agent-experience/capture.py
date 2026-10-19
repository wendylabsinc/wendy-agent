#!/usr/bin/env python3
"""Capture real Wendy output (MCP tool reply or CLI run) into canned/<set>/, scrubbed."""
import argparse
import hashlib
import json
import subprocess
import sys
from pathlib import Path

import shutil

from canned import scrub, scrub_output
from mcp_client import StdioMCP
from tool_trace import cli_path

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


def capture_mcp(args, canned_dir, provenance):
    # Not `mcp serve --device`: its startup connection is asynchronous, so a call made right after
    # initialize could capture NOT_CONNECTED. device_connect below waits for the connection.
    argv = [args.wendy, "mcp", "serve", "--tool-groups", "all"]
    with StdioMCP(argv, timeout=args.timeout) as mcp:
        mcp.initialize()
        tools = {t["name"]: t for t in mcp.list_tools()}
        if args.tool not in tools:
            print(f"unknown tool {args.tool}", file=sys.stderr)
            return 2
        read_only = (tools[args.tool].get("annotations") or {}).get("readOnlyHint") is True
        if not read_only and not args.yes:
            print(f"{args.tool} is not read-only; pass --yes to run it for real", file=sys.stderr)
            return 2
        if not args.no_connect:
            connected = mcp.call("device_connect", {"device": args.device})
            if connected.get("isError"):
                print(f"device_connect {args.device} failed: {json.dumps(connected)}", file=sys.stderr)
                return 1
        result = mcp.call(args.tool, json.loads(args.args or "{}"))
    if result.get("isError"):
        # Canned calls never return an error to the agent (D7), so an error reply is never saved.
        print(f"{args.tool} returned an error, nothing saved: {json.dumps(result)}", file=sys.stderr)
        return 1
    path = Path(canned_dir) / args.set / f"{args.tool}.json"
    data = {"tool": args.tool, "replies": []}
    if path.is_file():
        data = json.loads(path.read_text())
    data["captured_with"] = provenance
    data["replies"].append(scrub(result))
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2) + "\n")
    print(path)
    print("Replace values that came from your arguments with {{args.NAME}} placeholders.")
    return 0


def capture_cli(args, canned_dir):
    words = args.cli
    prefix = cli_path(words)
    if args.prefix_words:
        prefix = prefix[:args.prefix_words]
    if not prefix:
        print("no subcommand words found in --cli", file=sys.stderr)
        return 2
    if not args.yes:
        print("CLI commands are not classified read-only; pass --yes to run it for real", file=sys.stderr)
        return 2
    run = subprocess.run([args.wendy, "--device", args.device, *words], capture_output=True, text=True, timeout=120)
    path = Path(canned_dir) / args.set / "cli" / ("-".join(prefix) + ".json")
    data = {"argv_prefix": prefix, "stdout": scrub_output(run.stdout), "stderr": scrub_output(run.stderr),
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
    mode.add_argument("--cli", nargs=argparse.REMAINDER, metavar="WORD")
    p.add_argument("--args")
    p.add_argument("--yes", action="store_true")
    p.add_argument("--canned-dir", default=str(DEFAULT_DIR))
    p.add_argument("--prefix-words", type=int)
    p.add_argument("--no-connect", action="store_true",
                   help="MCP mode: skip device_connect, for host-side tools (device_connect boots a stopped VM).")
    p.add_argument("--timeout", type=float, default=120, help="MCP mode: seconds to wait for each reply.")
    args = p.parse_args(argv)
    resolved = shutil.which(args.wendy)
    if resolved is None:
        print(f"cannot resolve --wendy {args.wendy}", file=sys.stderr)
        return 2
    args.wendy = resolved
    try:
        provenance = {"wendy_version": _version(resolved), "wendy_sha256": _sha256(resolved),
                      "device_kind": device_kind(args.device)}
    except (OSError, subprocess.SubprocessError) as e:
        print(f"cannot inspect wendy binary: {e}", file=sys.stderr)
        return 2
    if args.cli:
        return capture_cli(args, args.canned_dir)
    return capture_mcp(args, args.canned_dir, provenance)


if __name__ == "__main__":
    sys.exit(main())
