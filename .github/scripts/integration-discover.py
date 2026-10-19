#!/usr/bin/env python3
"""Build the integration matrix from devices accessible to the runner."""

import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import re
import subprocess
import sys


REQUIRED = ("raspberry-pi", "jetson", "qualcomm", "generic-linux-x86")
REQUIRED_CONTAINERD = (1, 2)
MAX_DEVICES = 20
HOSTNAME = re.compile(
    r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?"
    r"(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*\.?"
)


def log(message):
    print(message, file=sys.stderr, flush=True)


def platform(device_type, os_name="", architecture=""):
    if device_type == "arduino-uno-q":
        return "qualcomm"
    for prefix, family in (("raspberry-pi", "raspberry-pi"),
                           ("jetson", "jetson"),
                           ("dragonwing", "qualcomm"),
                           ("qualcomm", "qualcomm")):
        if device_type == prefix or device_type.startswith(prefix + "-"):
            return family
    # Linux agents report /etc/os-release ID (ubuntu, debian, etc.), not
    # necessarily "linux". Agent targets other than Darwin/Windows are Linux;
    # WendyOS x86 images are deliberately not the generic distro coverage slot.
    if (os_name.lower() not in ("", "unknown", "darwin", "macos", "windows", "wendyos")
            and architecture.lower() in ("amd64", "x86_64")):
        return "generic-linux-x86"
    return None


def containerd_server_major(output):
    """Read the connected server version, never the installed ctr client version."""
    versions = re.findall(r"(?m)^Server:\s*\r?\n\s*Version:\s*v?([0-9]+)\.[^\s]+", output)
    if len(versions) != 1:
        raise ValueError("containerd did not report exactly one server version")
    major = int(versions[0])
    if major not in REQUIRED_CONTAINERD:
        raise ValueError(f"unsupported containerd server major: {major}")
    return major


def runtime_major(wendy, host):
    # HostShell inherits the running agent's environment, including any custom
    # containerd socket. This queries the daemon used by that agent, not a local
    # ctr binary's build version or an unrelated default daemon.
    result = subprocess.run(
        [wendy, "device", "shell", "--device", host, "--", "sh", "-c",
         'set -eu; printf "\\nWENDY_CONTAINERD_BEGIN\\n"; '
         'ctr --address "${WENDY_CONTAINERD_ADDR:-/run/containerd/containerd.sock}" version; '
         'printf "\\nWENDY_CONTAINERD_END\\n"'],
        stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=45, check=True,
    )
    lines = result.stdout.splitlines()
    if lines.count("WENDY_CONTAINERD_BEGIN") != 1 or lines.count("WENDY_CONTAINERD_END") != 1:
        raise ValueError("containerd server probe did not complete")
    start, end = lines.index("WENDY_CONTAINERD_BEGIN"), lines.index("WENDY_CONTAINERD_END")
    if start >= end:
        raise ValueError("containerd server probe markers are out of order")
    return containerd_server_major("\n".join(lines[start + 1:end]))


def cli_json(wendy, *args):
    result = subprocess.run(
        [wendy, *args, "--json"], stdin=subprocess.DEVNULL,
        capture_output=True, text=True, timeout=45, check=True,
    )
    return json.loads(result.stdout)


def candidates(discovery):
    hosts = set()
    for device in discovery.get("lanDevices") or []:
        if not device.get("isWendyDevice"):
            continue
        host = device.get("hostname", "")
        if not isinstance(host, str) or len(host) > 253 or not HOSTNAME.fullmatch(host):
            log(f"SKIPPED invalid hostname: {host!r}")
            continue
        hosts.add(host.lower())
    return sorted(hosts)


def probe(wendy, host, require_runtime=False):
    try:
        info = cli_json(wendy, "device", "info", "--device", host)
        # Check an authenticated app RPC as well as hardware info. Discovery
        # advertisements and public version information do not prove access.
        cli_json(wendy, "device", "apps", "list", "--device", host)
        family = platform(info.get("deviceType", ""), info.get("os", ""),
                          info.get("cpuArchitecture", ""))
        if require_runtime and (
                family not in REQUIRED or
                info.get("os", "").lower() in ("", "unknown", "darwin", "macos", "windows") or
                info.get("cpuArchitecture", "").lower() not in ("amd64", "x86_64", "arm64", "aarch64")):
            log(f"SKIPPED unsupported candidate platform: {host}")
            return host, None, None
        major = runtime_major(wendy, host) if require_runtime else None
        log(f"ACCESSIBLE: {host} ({family or 'unknown platform'}, containerd server {major or 'not probed'})")
        return host, family, major
    except (subprocess.SubprocessError, ValueError, AttributeError, OSError) as error:
        # Do not echo CLI stderr, which can contain credential diagnostics.
        log(f"SKIPPED inaccessible device: {host} ({type(error).__name__})")
        return None


def select(accessible, require_platforms, defer_runtime_coverage=False):
    if defer_runtime_coverage and not require_platforms:
        raise ValueError("--defer-runtime-coverage requires --require-platforms")
    if require_platforms:
        accessible = [device for device in accessible if device[1] in REQUIRED]
    # Reserve a slot for each required family before filling the bounded matrix.
    selected = []
    missing = []
    for family in REQUIRED:
        host = next((host for host, kind, _ in accessible if kind == family), None)
        if host is None:
            missing.append(family)
        else:
            selected.append(host)
    if require_platforms and missing:
        raise ValueError("no accessible device for required platform(s): " + ", ".join(missing))
    if require_platforms:
        missing_runtime = []
        for major in REQUIRED_CONTAINERD:
            host = next((host for host, _, runtime in accessible if runtime == major), None)
            if host is None:
                missing_runtime.append(str(major))
            elif host not in selected:
                selected.append(host)
        # An OS candidate may change the daemon major. Preserve available
        # runtime diversity in the selected rack, but let final receipts prove
        # the required 1.x and 2.x coverage after those OS updates.
        if missing_runtime and not defer_runtime_coverage:
            raise ValueError("no qualified device for required containerd server major(s): " + ", ".join(missing_runtime))
    for host, _, _ in accessible:
        if host not in selected and len(selected) < MAX_DEVICES:
            selected.append(host)
    log(f"Selected {len(selected)} of {len(accessible)} accessible devices (limit {MAX_DEVICES}).")
    return selected


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wendy", required=True)
    parser.add_argument("--require-platforms", action="store_true")
    parser.add_argument("--defer-runtime-coverage", action="store_true",
                        help="require runtime major coverage in post-update results, not the current rack")
    parser.add_argument("--evidence", type=Path, help="write the qualified inventory as JSON")
    args = parser.parse_args()
    if args.defer_runtime_coverage and not args.require_platforms:
        parser.error("--defer-runtime-coverage requires --require-platforms")
    try:
        if args.evidence:
            args.evidence.parent.mkdir(parents=True, exist_ok=True)
            args.evidence.unlink(missing_ok=True)
        hosts = candidates(cli_json(args.wendy, "discover", "--timeout", "10s"))
        with ThreadPoolExecutor(max_workers=4) as pool:
            accessible = list(filter(None, pool.map(
                lambda host: probe(args.wendy, host, args.require_platforms), hosts)))
        selected = select(accessible, args.require_platforms, args.defer_runtime_coverage)
        if args.evidence:
            args.evidence.write_text(json.dumps({
                "selected": selected,
                "devices": [{"hostname": host, "platform": family, "containerdServerMajor": major}
                            for host, family, major in accessible],
            }, indent=2) + "\n")
        print(json.dumps(selected))
    except (subprocess.SubprocessError, ValueError, AttributeError, OSError) as error:
        log(f"ERROR: integration discovery failed: {error}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
