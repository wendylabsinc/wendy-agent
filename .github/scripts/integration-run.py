#!/usr/bin/env python3
"""Prepare one candidate, run its fixtures, and record post-test device evidence."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent


def discovery_module():
    spec = importlib.util.spec_from_file_location("discovery", HERE / "integration-discover.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class InterruptedExecution(subprocess.SubprocessError):
    pass


def signal_group(process, signum):
    try:
        os.killpg(process.pid, signum)
    except ProcessLookupError:
        pass


def stop_group(process):
    # Give the shell test runner time to execute its cleanup trap. Afterwards
    # kill the entire group, including grandchildren left by an exited parent.
    signal_group(process, signal.SIGTERM)
    try:
        process.wait(timeout=20)
    except subprocess.TimeoutExpired:
        pass
    finally:
        signal_group(process, signal.SIGKILL)
        process.wait(timeout=10)


def execute(args, log, timeout):
    process = None
    interrupted = None

    def cancel(signum, _frame):
        nonlocal interrupted
        interrupted = signum
        if process is not None:
            signal_group(process, signum)

    previous = {sig: signal.signal(sig, cancel) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        with log.open("w") as output:
            try:
                if interrupted is not None:
                    raise InterruptedExecution(f"command interrupted by signal {interrupted}")
                process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=output,
                                           stderr=subprocess.STDOUT, start_new_session=True)
                deadline = time.monotonic() + timeout
                while process.poll() is None:
                    if interrupted is not None:
                        stop_group(process)
                        raise InterruptedExecution(f"command interrupted by signal {interrupted}")
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        stop_group(process)
                        raise subprocess.TimeoutExpired(args, timeout)
                    try:
                        process.wait(timeout=min(1, remaining))
                    except subprocess.TimeoutExpired:
                        pass
                if interrupted is not None:
                    stop_group(process)
                    raise InterruptedExecution(f"command interrupted by signal {interrupted}")
                if process.returncode:
                    if process.returncode < 0:
                        stop_group(process)
                    raise subprocess.CalledProcessError(process.returncode, args)
            finally:
                output.flush()
                print(log.read_text(), end="", flush=True)
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)


def is_os_candidate(metadata, info):
    return bool(metadata and (info.get("os", "").lower() == "wendyos" or
                             info.get("osVersion", "").lower().startswith("wendyos")))


def suite_receipt(marker, device, source_sha):
    if marker is not None:
        marker.parent.mkdir(parents=True, exist_ok=True)
        marker.write_text(json.dumps({"device": device, "source_sha": source_sha,
                                      "suite_completed": True}) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wendy", required=True)
    parser.add_argument("--device", required=True)
    parser.add_argument("--agents", required=True)
    parser.add_argument("--source-sha", required=True)
    parser.add_argument("--version", default="")
    parser.add_argument("--tests", default="")
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument("--os-metadata", default="")
    parser.add_argument("--os-version", default="")
    parser.add_argument("--os-sha", default="")
    parser.add_argument("--release-marker", type=Path)
    args = parser.parse_args()
    if args.release_marker is not None:
        args.release_marker.unlink(missing_ok=True)
    args.evidence.mkdir(parents=True, exist_ok=True)
    # A re-run must never upload the previous attempt's success receipt.
    result_path = args.evidence / "result.json"
    result_path.unlink(missing_ok=True)
    cleanup_receipt = args.evidence / "fixture-cleanup.json"
    cleanup_receipt.unlink(missing_ok=True)
    module = discovery_module()
    info = module.cli_json(args.wendy, "device", "info", "--device", args.device)
    os_candidate = is_os_candidate(args.os_metadata, info)
    helper = HERE / ("integration-prepare-os.py" if os_candidate else "integration-prepare-agent.py")
    preparation = args.evidence / "candidate.json"
    prepare = [sys.executable, str(helper), "--wendy", args.wendy, "--device", args.device,
               "--evidence", str(preparation)]
    if os_candidate:
        prepare += ["--metadata-dir", args.os_metadata, "--expected-version", args.os_version,
                    "--expected-commit", args.os_sha]
    else:
        prepare += ["--artifact-dir", args.agents, "--expected-commit", args.source_sha]
        if args.version:
            prepare += ["--expected-version", args.version]
    execute(prepare, args.evidence / "prepare.log", 3300 if os_candidate else 1200)
    command = ["bash", str(ROOT / "go/scripts/test-ci.sh"), "-w", args.wendy, "-h", args.device]
    if args.tests:
        for name in args.tests.split(","):
            name = name.strip()
            if not name:
                raise ValueError("Empty requested test name")
            command += ["-t", name]
    os.environ["WENDY_CI_LOG_DIR"] = str(args.evidence / "tests")
    os.environ["WENDY_CI_CLEANUP_RECEIPT"] = str(cleanup_receipt)
    try:
        execute(command, args.evidence / "suite.log", 3000)
    except subprocess.CalledProcessError as error:
        # Failed assertions can release the lease only after the harness has
        # independently verified that no registered fixture apps remain.
        # Signal deaths, missing receipts and cleanup failures keep the lease.
        if (error.returncode > 0 and not cleanup_receipt.is_symlink()
                and cleanup_receipt.is_file() and cleanup_receipt.stat().st_size > 0):
            suite_receipt(args.release_marker, args.device, args.source_sha)
        raise
    suite_receipt(args.release_marker, args.device, args.source_sha)
    verify = (prepare + ["--verify-only"]) if os_candidate else [
        sys.executable, str(helper), "--wendy", args.wendy, "--device", args.device,
        "--evidence", str(preparation), "--verify-only"]
    execute(verify, args.evidence / "verify.log", 120)
    # Read capabilities again after OTA, since an image can change containerd.
    info = module.cli_json(args.wendy, "device", "info", "--device", args.device)
    major = module.runtime_major(args.wendy, args.device)
    result = {
        "hostname": args.device,
        "source_sha": args.source_sha,
        "host_platform": os.environ.get("RUNNER_OS", sys.platform),
        "family": module.platform(info.get("deviceType", ""), info.get("os", ""),
                                  info.get("cpuArchitecture", "")),
        "containerd_major": major,
        "candidate_kind": "os" if os_candidate else "agent",
        "candidate_version": args.version,
        "os_candidate_version": args.os_version,
        "os_candidate_sha": args.os_sha,
        "device_info": info,
        "candidate": json.loads(preparation.read_text()),
        "passed": True,
    }
    spec = importlib.util.spec_from_file_location("qualification", HERE / "integration-results.py")
    qualification = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(qualification)
    qualification.validate_candidate(result, args.source_sha, args.os_version, args.os_sha, args.version)
    result_path.write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"ERROR: hardware qualification failed: {error}", file=sys.stderr)
        sys.exit(1)
