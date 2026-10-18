#!/usr/bin/env python3
"""Serialize rack mutations across repositories with a persistent device lock."""

import argparse
import base64
import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import uuid


# mkdir is atomic across independent runner hosts. The token-named child makes
# release safe if an operator has already removed an old lease and another run
# acquired the device: this process cannot remove the new token's directory.
# WendyOS keeps /data across OTA. A vendor Linux rootfs uses /var/lib instead.
LEASE_SCRIPT = r"""set -eu
operation=$1
token=$2
owner=$3
case "$token" in ''|*[!0-9a-f]*) exit 2 ;; esac
if [ -f /usr/lib/wendyos/version.txt ] || [ -f /etc/wendyos/version.txt ]; then
  mountpoint -q /data || { echo 'WendyOS /data is not mounted; refusing a nonpersistent lease' >&2; exit 1; }
  root=/data/wendy-ci-rack
else
  root=/var/lib/wendy-ci-rack
fi
mkdir -p -m 0700 "$root"
lease="$root/lease"
status=busy
case "$operation" in
  acquire)
    if mkdir -m 0700 "$lease" 2>/dev/null; then
      mkdir -m 0700 "$lease/$token"
      printf '%s\n' "$owner" > "$lease/$token/owner"
      status=acquired
    elif [ -d "$lease/$token" ] && [ "$(cat "$lease/$token/owner" 2>/dev/null || true)" = "$owner" ]; then
      # The previous HostShell response may have been lost after acquisition.
      status=acquired
    else
      owner=unknown
      for file in "$lease"/*/owner; do
        if [ -f "$file" ]; then owner=$(cat "$file"); break; fi
      done
    fi
    ;;
  release)
    if [ -d "$lease/$token" ] && [ "$(cat "$lease/$token/owner" 2>/dev/null || true)" = "$owner" ]; then
      rm "$lease/$token/owner"
      rmdir "$lease/$token"
      rmdir "$lease"
      status=released
    else
      status=not-owner
    fi
    ;;
  *) exit 2 ;;
esac
printf '\nWENDY_LEASE_BEGIN\nstatus=%s\npath=%s\ntoken=%s\nowner=%s\nWENDY_LEASE_END\n' "$status" "$lease" "$token" "$owner"
"""


def parse_verdict(output):
    lines = output.splitlines()
    if lines.count("WENDY_LEASE_BEGIN") != 1 or lines.count("WENDY_LEASE_END") != 1:
        raise ValueError("device lease command did not return a complete verdict")
    start, end = lines.index("WENDY_LEASE_BEGIN"), lines.index("WENDY_LEASE_END")
    if start >= end:
        raise ValueError("device lease verdict markers are out of order")
    verdict = {}
    for line in lines[start + 1:end]:
        key, separator, value = line.partition("=")
        if not separator or key in verdict:
            raise ValueError("invalid device lease verdict")
        verdict[key] = value
    if set(verdict) != {"status", "path", "token", "owner"}:
        raise ValueError("device lease verdict is incomplete")
    if verdict["status"] not in {"acquired", "busy", "released", "not-owner"}:
        raise ValueError("device lease command returned an unknown status")
    if verdict["path"] not in {"/data/wendy-ci-rack/lease", "/var/lib/wendy-ci-rack/lease"}:
        raise ValueError("device lease command returned an unexpected path")
    return verdict


class LeaseConfigurationError(ValueError):
    pass


def remote(wendy, device, operation, token, owner):
    result = subprocess.run(
        [wendy, "device", "shell", "--device", device, "--", "sh", "-c", LEASE_SCRIPT,
         "wendy-ci-lease", operation, token, owner],
        stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=45,
    )
    if result.returncode:
        if "WendyOS /data is not mounted" in result.stdout + result.stderr:
            raise LeaseConfigurationError("WendyOS /data is not mounted; cannot establish a lease that survives OTA")
        raise ValueError(f"authenticated device lease {operation} failed with exit {result.returncode}")
    verdict = parse_verdict(result.stdout)
    if verdict["token"] != token:
        raise ValueError("device lease response has the wrong ownership token")
    return verdict


def describe_owner(encoded):
    try:
        return base64.b64decode(encoded, validate=True).decode("utf-8")
    except (ValueError, UnicodeError):
        return "unknown owner; inspect the persistent lease directory"


class Interrupted(Exception):
    pass


def acquire(wendy, device, token, owner, wait_seconds, stopped=lambda: False):
    deadline = time.monotonic() + wait_seconds
    last = ""
    while True:
        if stopped():
            raise Interrupted("cancelled while waiting for the device lease")
        try:
            verdict = remote(wendy, device, "acquire", token, owner)
            if verdict["status"] == "acquired":
                return verdict
            last = f"busy at {verdict['path']}; held by {describe_owner(verdict['owner'])}"
        except LeaseConfigurationError:
            raise
        except (ValueError, OSError, subprocess.SubprocessError) as error:
            # Acquisition may have succeeded before a response was lost. Retry
            # with the same token; never remove an unidentified lock.
            last = str(error)
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise ValueError(f"device lease wait expired: {last}")
        print(f"Waiting for {device}: {last}", file=sys.stderr, flush=True)
        time.sleep(min(10, remaining))


def run_locked(args):
    token = uuid.uuid4().hex
    owner = base64.b64encode(args.owner.encode()).decode()
    child, interrupted = None, False
    marker = Path(args.release_marker) if args.release_marker else None
    # A previous attempt's marker is not evidence about this child process.
    if marker is not None:
        marker.unlink(missing_ok=True)

    def cancel(signum, _frame):
        nonlocal interrupted
        interrupted = True
        if child is not None and child.poll() is None:
            try:
                os.killpg(child.pid, signum)
            except ProcessLookupError:
                pass

    previous = {sig: signal.signal(sig, cancel) for sig in (signal.SIGINT, signal.SIGTERM)}
    acquired = None
    release = False
    try:
        acquired = acquire(args.wendy, args.device, token, owner, args.wait_seconds, lambda: interrupted)
        if interrupted:
            raise Interrupted("cancelled immediately after acquiring the device lease")
        print(f"Acquired {args.device} lease {token} for {args.owner}", flush=True)
        child = subprocess.Popen(args.command, stdin=subprocess.DEVNULL, start_new_session=True)
        deadline = time.monotonic() + args.command_timeout_seconds
        while child.poll() is None:
            if interrupted or time.monotonic() >= deadline:
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                    # integration-run forwards cancellation to a separate test
                    # group and allows 20 seconds for its cleanup trap.
                    child.wait(timeout=40)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait(timeout=10)
                raise Interrupted("device command cancelled or exceeded its deadline")
            try:
                child.wait(timeout=1)
            except subprocess.TimeoutExpired:
                pass
        # The child creates its marker only after preparation completed and its
        # suite returned normally through cleanup. A marker cannot override
        # cancellation, timeout, or an interrupted child process.
        safe_failure = marker is not None and marker.is_file() and not marker.is_symlink()
        release = not interrupted and (child.returncode == 0 or (child.returncode > 0 and safe_failure))
        return child.returncode if child.returncode >= 0 else 1
    finally:
        try:
            if acquired is not None:
                if release:
                    try:
                        verdict = remote(args.wendy, args.device, "release", token, owner)
                        if verdict["status"] != "released":
                            raise ValueError("device lease ownership changed before release")
                        print(f"Released {args.device} lease {token}", flush=True)
                    except (ValueError, OSError, subprocess.SubprocessError):
                        print(f"Lease cleanup needs inspection: {args.device} {acquired['path']} token={token} owner={args.owner}", file=sys.stderr)
                        raise
                else:
                    print(f"Lease retained after uncertain completion: {args.device} {acquired['path']} token={token} owner={args.owner}. "
                          "Verify the owning run has ended and no update/reboot is active before manual recovery; leases are never stolen by age.",
                          file=sys.stderr, flush=True)
            else:
                print(f"No lease confirmed for {args.device}; this attempt's token={token}, owner={args.owner}. "
                      "If an acquisition response was lost, inspect the device lease before recovery.", file=sys.stderr)
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wendy", required=True)
    parser.add_argument("--device", required=True)
    parser.add_argument("--owner", required=True)
    parser.add_argument("--wait-seconds", type=float, default=900)
    parser.add_argument("--command-timeout-seconds", type=float, default=7200)
    parser.add_argument("--release-marker", default="",
                        help="Child-created cleanup receipt permits release after normal nonzero exit")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.command[:1] == ["--"]:
        args.command = args.command[1:]
    if (not args.command or not args.owner or any(ord(c) < 32 for c in args.owner) or
            not math.isfinite(args.wait_seconds) or not 0 <= args.wait_seconds <= 3600 or
            not math.isfinite(args.command_timeout_seconds) or not 0 < args.command_timeout_seconds <= 14400):
        parser.error("a command and printable owner are required; wait must be 0..3600 seconds and command timeout 0..14400 seconds")
    try:
        return run_locked(args)
    except (Interrupted, OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"ERROR: rack device lease: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
