#!/usr/bin/env python3
"""Print --ask-pass when a focused Ansible target lacks passwordless SSH."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("-i", "--inventory", action="append", default=[])
    parser.add_argument("--limit", default="")
    parser.add_argument("--mode", choices=("auto", "true", "false"), default="auto")
    return parser.parse_args()


def inventory_args(inventories: list[str]) -> list[str]:
    args: list[str] = []
    for inventory in inventories:
        args.extend(["-i", inventory])
    return args


def list_hosts(inventories: list[str], limit: str) -> list[str]:
    command = ["ansible", *inventory_args(inventories), "ci", "--list-hosts"]
    if limit:
        command.extend(["--limit", limit])

    result = subprocess.run(command, text=True, capture_output=True, check=True)
    hosts: list[str] = []
    for line in result.stdout.splitlines():
        stripped = line.strip()
        if stripped and not stripped.startswith("hosts ("):
            hosts.append(stripped)
    return hosts


def host_vars(inventories: list[str], host: str) -> dict[str, object]:
    command = ["ansible-inventory", *inventory_args(inventories), "--host", host]
    result = subprocess.run(command, text=True, capture_output=True, check=True)
    return json.loads(result.stdout)


def has_passwordless_ssh(host: str, variables: dict[str, object]) -> bool:
    ssh_host = str(variables.get("ansible_host") or host)
    ssh_user = str(variables.get("ansible_user") or os.environ.get("USER") or "")

    destination = ssh_host if not ssh_user else f"{ssh_user}@{ssh_host}"
    command = [
        "ssh",
        "-o", "BatchMode=yes",
        "-o", "PasswordAuthentication=no",
        "-o", "KbdInteractiveAuthentication=no",
        "-o", "NumberOfPasswordPrompts=0",
        "-o", "ControlMaster=no",
        "-o", "ControlPath=none",
        "-o", "StrictHostKeyChecking=accept-new",
        "-o", "ConnectTimeout=5",
        destination,
        "exit",
    ]
    result = subprocess.run(command, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return result.returncode == 0


def main() -> int:
    args = parse_args()

    if args.mode == "true":
        print("--ask-pass")
        return 0
    if args.mode == "false":
        return 0

    # Avoid surprising password prompts for broad multi-host operations. In auto
    # mode, fallback only applies to focused runs that set LIMIT/--limit.
    if not args.limit:
        return 0

    try:
        hosts = list_hosts(args.inventory, args.limit)
        needs_password = any(not has_passwordless_ssh(host, host_vars(args.inventory, host)) for host in hosts)
    except subprocess.CalledProcessError as error:
        print(error.stderr, file=sys.stderr, end="")
        return error.returncode

    if needs_password:
        print("--ask-pass")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
