#!/usr/bin/env python3
"""Update a nightly device and verify the nightly agent after the OS reboot."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
spec = importlib.util.spec_from_file_location("integration_run", HERE / "integration-run.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


def check(args):
    args.evidence.mkdir(parents=True, exist_ok=True)
    marker = args.evidence / "lease-releasable"
    marker.unlink(missing_ok=True)
    cleanup = args.evidence / "fixture-cleanup.json"
    cleanup.unlink(missing_ok=True)
    cli = str(Path(args.wendy).resolve())
    info = runner.discovery_module().cli_json(cli, "device", "info", "--device", args.device)
    (args.evidence / "before.json").write_text(json.dumps(info, indent=2) + "\n")
    if runner.is_os_candidate("nightly", info):
        runner.execute([cli, "os", "update", "--nightly", "--device", args.device],
                       args.evidence / "os-update.log", 3300)
    prepare = [sys.executable, str(HERE / "integration-prepare-agent.py"), "--wendy", cli,
               "--device", args.device, "--evidence", str(args.evidence / "agent.json")]
    runner.execute(prepare + ["--nightly"], args.evidence / "prepare.log", 1200)
    os.environ["WENDY_CI_LOG_DIR"] = str(args.evidence / "tests")
    os.environ["WENDY_CI_CLEANUP_RECEIPT"] = str(cleanup)
    for script, timeout in (("test-ci.sh", 3000), ("test-templates.sh", 1800)):
        try:
            runner.execute(["bash", str(ROOT / "go/scripts" / script), "-w", cli, "-h", args.device],
                           args.evidence / f"{script}.log", timeout)
        except subprocess.CalledProcessError as error:
            if (error.returncode > 0 and script == "test-ci.sh" and cleanup.is_file()
                    and not cleanup.is_symlink() and cleanup.stat().st_size > 0):
                marker.write_text("Preparation completed and tests exited normally.\n")
            raise
    marker.write_text("Preparation completed and tests exited normally.\n")
    runner.execute(prepare + ["--verify-only"], args.evidence / "verify.log", 120)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wendy", required=True)
    parser.add_argument("--device", required=True)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    try:
        check(args)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"ERROR: nightly device verification failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
