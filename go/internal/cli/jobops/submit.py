"""Create one named, idempotent job and retain its execution metadata."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time


def main():
    request = json.loads(sys.argv[1])
    spec = request["spec"]
    name = spec["name"]
    if not re.fullmatch("[a-z0-9][a-z0-9_-]{0,63}", name):
        raise ValueError("invalid job name")
    working = Path(spec["working_directory"]).resolve(strict=True)
    if not working.is_dir() or working == Path("/"):
        raise ValueError("working_directory must be an existing non-root directory")
    for relative, expected in spec.get("source_hashes", {}).items():
        p = working / relative
        if (
            Path(relative).is_absolute()
            or ".." in Path(relative).parts
            or not p.is_file()
            or p.is_symlink()
        ):
            raise ValueError("source hashes require regular relative files")
        if p.resolve().is_relative_to(working) is False:
            raise ValueError("source escapes working_directory")
        digest = hashlib.sha256()
        with p.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
        if digest.hexdigest() != expected.lower():
            raise ValueError("source hash mismatch: " + relative)
    spec["working_directory"] = str(working)
    encoded = json.dumps(spec, sort_keys=True, separators=(",", ":")).encode()
    digest = hashlib.sha256(encoded).hexdigest()
    parent = working
    for part in [".wendy", "jobs"]:
        parent = parent / part
        if parent.is_symlink():
            raise ValueError("job storage must not contain symlinks")
        parent.mkdir(exist_ok=True)
    root = parent / name
    if root.exists():
        if root.is_symlink() or (root / "spec.json").read_bytes() != encoded:
            raise ValueError("job name already exists with another specification")
        receipt = json.loads((root / "receipt.json").read_text())
        receipt["reused"] = True
        try:
            current = json.loads((root / "status.json").read_text())
            receipt["reported_state"] = current.get("state", "unknown")
            receipt["stage"] = current.get("stage", "unknown")
        except (OSError, ValueError):
            receipt["reported_state"] = "unknown"
        print("WENDY_JOB " + json.dumps(receipt, separators=(",", ":")), flush=True)
        return
    root.mkdir(mode=0o700)
    (root / "spec.json").write_bytes(encoded)
    (root / "runner.py").write_text(request["worker_source"])
    (root / "status.json").write_text(
        json.dumps(
            {"state": "starting", "stage": "starting", "started_unix": time.time()}
        )
    )
    with (root / "runner.log").open("xb") as log:
        process = subprocess.Popen(
            ["python3", "-u", str(root / "runner.py")],
            stdin=subprocess.DEVNULL,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    receipt = {
        "job_path": str(root),
        "name": name,
        "runner_pid": process.pid,
        "spec_sha256": digest,
        "reported_state": "starting",
        "reused": False,
    }
    (root / "receipt.json").write_text(json.dumps(receipt, separators=(",", ":")))
    print("WENDY_JOB " + json.dumps(receipt, separators=(",", ":")), flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(
            "WENDY_JOB " + json.dumps({"error": str(exc)[:800]}, separators=(",", ":")),
            flush=True,
        )
        sys.exit(1)
