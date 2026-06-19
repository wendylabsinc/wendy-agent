#!/usr/bin/env python3
"""Integration test for wendy.json top-level files on Linux (WDY-1532)."""

import sys
from pathlib import Path

ROOT = Path("/app")
FAILURES = []


def check(label: str, condition: bool, detail: str = "") -> None:
    status = "OK " if condition else "FAIL"
    message = f"{status}  {label}"
    if detail:
        message += f" — {detail}"
    print(message, flush=True)
    if not condition:
        FAILURES.append(label)


def assert_readonly(path: Path) -> None:
    try:
        path.write_text("tampered", encoding="utf-8")
        check(f"{path.relative_to(ROOT)} is read-only", False, "write unexpectedly succeeded")
    except OSError:
        check(f"{path.relative_to(ROOT)} is read-only", True)


config_path = ROOT / "config" / "runtime.json"
check("config/runtime.json exists", config_path.exists(), str(config_path))
if config_path.exists():
    content = config_path.read_text(encoding="utf-8").strip()
    check("config/runtime.json contains expected key", '"ci-filesync-test"' in content, repr(content[:120]))
    assert_readonly(config_path)

message_path = ROOT / "assets" / "message.txt"
check("assets/message.txt exists from explicit to", message_path.exists(), str(message_path))
if message_path.exists():
    content = message_path.read_text(encoding="utf-8").strip()
    check("assets/message.txt has expected content", content == "hello from wendy file sync", repr(content))
    assert_readonly(message_path)

model_dir = ROOT / "models" / "detector"
check("models/detector is a directory", model_dir.is_dir(), str(model_dir))
if model_dir.is_dir():
    model_file = model_dir / "model.txt"
    check("models/detector/model.txt exists", model_file.exists(), str(model_file))
    if model_file.exists():
        content = model_file.read_text(encoding="utf-8").strip()
        check("models/detector/model.txt has expected content", "detector-model-ci" in content, repr(content[:80]))

    probe = model_dir / "_wendy_write_probe"
    try:
        probe.write_text("probe", encoding="utf-8")
        check("models/detector directory is read-only", False, "write unexpectedly succeeded")
    except OSError:
        check("models/detector directory is read-only", True)

print("", flush=True)
if FAILURES:
    print(f"FAIL: {len(FAILURES)} check(s) failed:", flush=True)
    for failure in FAILURES:
        print(f"  - {failure}", flush=True)
    sys.exit(1)

print("PASS: wendy.json file sync verified", flush=True)
