#!/usr/bin/env python3
"""Install exact builder candidates and record authenticated boot evidence."""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tempfile
import time
import urllib.request


PUBLIC_BASE = "https://storage.googleapis.com/wendyos-images-public/"
HEX = re.compile(r"[0-9a-f]{64}")
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*")
REQUIRED_FAMILIES = {"raspberry-pi", "jetson", "qualcomm"}

# Values are read as data, never sourced or evaluated as shell code. The final
# marker also detects a CLI HostShell EOF before the remote exit-code frame.
IDENTITY_SCRIPT = r"""set -eu
printf '\nWENDY_CANDIDATE_IDENTITY_BEGIN\n'
if [ -f /etc/wendyos/device-type ]; then
  cat /etc/wendyos/device-type
  printf '\n'
fi
for item in commit build-version agent-sha256; do
  printf '%s=' "$item"
  if [ -f "/usr/lib/wendyos/$item" ]; then cat "/usr/lib/wendyos/$item"; else printf '\n'; fi
done
printf 'installed-agent-sha256='
if [ -f /opt/wendyos/bin/wendy-agent ]; then
  sha256sum /opt/wendyos/bin/wendy-agent | cut -d' ' -f1
else printf '\n'; fi
pid=$(systemctl show wendyos-agent.service --property=MainPID --value 2>/dev/null || true)
printf 'running-agent-sha256='
case "$pid" in ''|0|*[!0-9]*) printf '\n' ;; *) sha256sum "/proc/$pid/exe" | cut -d' ' -f1 ;; esac
printf 'WENDY_CANDIDATE_IDENTITY_END\n'
"""


def command(wendy, args, timeout=60):
    result = subprocess.run([wendy, *args], stdin=subprocess.DEVNULL,
                            capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        # The caller's artifact contains identity and verdicts, not auth stderr.
        raise ValueError(f"wendy {args[0]} {args[1]} failed with exit {result.returncode}")
    return result.stdout


def device_info(wendy, device):
    info = json.loads(command(wendy, ["device", "info", "--device", device, "--json"]))
    if not isinstance(info, dict):
        raise ValueError("device info is not an object")
    return info


def parse_identity(output):
    lines = output.splitlines()
    if lines.count("WENDY_CANDIDATE_IDENTITY_BEGIN") != 1 or lines.count("WENDY_CANDIDATE_IDENTITY_END") != 1:
        raise ValueError("host identity command did not return a complete verdict")
    start, end = lines.index("WENDY_CANDIDATE_IDENTITY_BEGIN"), lines.index("WENDY_CANDIDATE_IDENTITY_END")
    if start >= end:
        raise ValueError("host identity markers are out of order")
    identity = {}
    for line in lines[start + 1:end]:
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator:
            # Older images store the board name as a single plain line.
            if not identity and NAME.fullmatch(line):
                key, value = "BOARD", line
            else:
                raise ValueError("malformed or duplicate host identity field")
        key = key.strip()
        if key in identity or not NAME.fullmatch(key):
            raise ValueError("malformed or duplicate host identity field")
        identity[key] = value.strip().strip('"')
    return identity


def read_identity(wendy, device):
    return parse_identity(command(wendy, ["device", "shell", "--device", device,
                                          "--", "sh", "-c", IDENTITY_SCRIPT]))


def load_entries(directory, version, commit):
    if not re.fullmatch(r"[0-9a-f]{40}", commit) or not NAME.fullmatch(version):
        raise ValueError("expected version or builder commit is invalid")
    paths = sorted(Path(directory).glob("manifest-entry-*.json"))
    if not paths:
        raise ValueError("candidate metadata is missing")
    entries, seen = [], set()
    for path in paths:
        entry = json.loads(path.read_text())
        if not isinstance(entry, dict) or entry.get("version") != version or entry.get("build_sha") != commit:
            raise ValueError(f"{path.name}: candidate version/commit does not match this build")
        if entry.get("pr", 0):
            raise ValueError(f"{path.name}: PR metadata cannot qualify a public channel")
        device, storage = entry.get("device", ""), entry.get("build_storage", "")
        aliases = entry.get("aliases", [])
        if not isinstance(aliases, list) or any(not isinstance(a, str) or not NAME.fullmatch(a) for a in aliases):
            raise ValueError(f"{path.name}: invalid aliases")
        if (not isinstance(device, str) or not NAME.fullmatch(device) or
                not isinstance(storage, str) or storage not in {"sd", "nvme", "emmc", "ufs", "disk"}):
            raise ValueError(f"{path.name}: missing exact board/storage identity")
        if (device, storage) in seen:
            raise ValueError(f"{path.name}: duplicate candidate board/storage")
        seen.add((device, storage))
        entry["metadata_file"] = path.name
        entries.append(entry)
    return entries


def family(board):
    if board.startswith("raspberry-pi-"):
        return "raspberry-pi"
    if board.startswith("jetson-"):
        return "jetson"
    if board == "arduino-uno-q" or board.startswith(("dragonwing-", "qualcomm-")):
        return "qualcomm"
    return None


def runtime_baseline(info):
    # Linux agents return /etc/os-release IDs such as ubuntu and debian.
    # This is the same generic distro classification as integration-discover.
    return (info.get("os", "").lower() not in {"", "unknown", "darwin", "macos", "windows", "wendyos"} and
            info.get("cpuArchitecture", "").lower() in {"amd64", "x86_64"} and
            not info.get("osVersion", "").lower().startswith("wendyos"))


def select_entry(entries, info, identity):
    board = identity.get("BOARD", "") or identity.get("MACHINE", "")
    # Older agents already infer nvme/emmc from MACHINE. Accept that exact
    # information when STORAGE was not written yet, but never choose a default
    # artifact from missing or conflicting media information.
    machine = identity.get("MACHINE", "").lower()
    inferred = {medium for medium in ("nvme", "emmc") if medium in machine}
    media = inferred | {medium for medium in (identity.get("STORAGE", ""), info.get("storageMedium", ""))
                        if medium}
    if len(media) > 1:
        raise ValueError("authenticated storage fields disagree")
    storage = next(iter(media), "")
    if not board or not storage:
        raise ValueError("device does not report an exact WendyOS BOARD and STORAGE")
    if storage not in {"sd", "nvme", "emmc", "ufs", "disk"}:
        raise ValueError("device reports an unsupported storage medium")
    reported = info.get("deviceType", "")
    matches = [e for e in entries if board in [e["device"], *e.get("aliases", [])]
               and e["build_storage"] == storage]
    if len(matches) != 1:
        raise ValueError(f"expected one candidate for {board}/{storage}; found {len(matches)}")
    entry = matches[0]
    if reported not in [board, entry["device"], *entry.get("aliases", [])]:
        raise ValueError("authenticated device type disagrees with rootfs board identity")
    validate_ota(entry)
    return entry


def validate_ota(entry):
    path = entry.get("ota_update_path", "")
    if not isinstance(path, str):
        raise ValueError("candidate has no safe, versioned OTA path")
    parts = PurePosixPath(path).parts
    if (not path or not re.fullmatch(r"[A-Za-z0-9._/-]+", path) or
            path.startswith("/") or any(p in {".", ".."} for p in path.split("/")) or
            entry["version"] not in parts or not path.endswith(".wendy")):
        raise ValueError("candidate has no safe, versioned OTA path")
    digest, size = entry.get("ota_update_checksum", ""), entry.get("ota_update_size")
    if not isinstance(digest, str) or not HEX.fullmatch(digest) or isinstance(size, bool) or not isinstance(size, int) or size <= 0:
        raise ValueError("candidate OTA checksum or size is missing")


def plan(wendy, devices, entries):
    if not isinstance(devices, list) or not devices or any(not isinstance(d, str) or not d for d in devices):
        raise ValueError("required rack device list is empty or invalid")
    if len(devices) != len(set(devices)):
        raise ValueError("required rack contains duplicate devices")
    candidates, covered, x86 = [], set(), False
    for device in devices:
        info = device_info(wendy, device)
        if runtime_baseline(info):
            candidates.append({"device": device, "mode": "linux-runtime-baseline",
                               "agent_version": info.get("version")})
            x86 = True
            continue
        identity = read_identity(wendy, device)
        entry = select_entry(entries, info, identity)
        covered.add(family(entry["device"]))
        x86 = x86 or (entry["device"] == "generic-x86-64" and
                      info.get("cpuArchitecture") in {"amd64", "x86_64"})
        candidates.append({"device": device, "mode": "candidate-os",
                           "board": entry["device"], "storage": entry["build_storage"],
                           "ota_sha256": entry["ota_update_checksum"]})
    missing = REQUIRED_FAMILIES - covered
    if missing or not x86:
        raise ValueError("required rack coverage missing: " + ", ".join(sorted(missing) + ([] if x86 else ["linux-x86-64"])))
    return candidates


def download_verified(entry, destination, opener=urllib.request.urlopen):
    validate_ota(entry)
    digest, size, deadline = hashlib.sha256(), 0, time.monotonic() + 1200
    with opener(PUBLIC_BASE + entry["ota_update_path"], timeout=30) as response, open(destination, "wb") as output:
        if not response.geturl().startswith(PUBLIC_BASE):
            raise ValueError("OTA download redirected outside the candidate bucket")
        while True:
            block = response.read(1024 * 1024)
            if not block:
                break
            size += len(block)
            if size > entry["ota_update_size"] or time.monotonic() > deadline:
                raise ValueError("OTA download exceeded expected size or deadline")
            output.write(block)
            digest.update(block)
    if size != entry["ota_update_size"] or digest.hexdigest() != entry["ota_update_checksum"]:
        raise ValueError("OTA payload size or SHA256 does not match candidate metadata")


def verify_boot(entry, info, identity):
    if identity.get("commit") != entry["build_sha"] or identity.get("build-version") != entry["version"]:
        raise ValueError("running OS does not match the candidate build version and commit")
    selected = select_entry([entry], info, identity)
    bundled = identity.get("agent-sha256", "")
    if not HEX.fullmatch(bundled) or any(identity.get(k) != bundled for k in
            ("installed-agent-sha256", "running-agent-sha256")):
        raise ValueError("running agent differs from the agent bundled in the candidate OS")
    return {"board": selected["device"], "storage": selected["build_storage"],
            "os_version": info.get("osVersion"), "build_version": identity["build-version"],
            "builder_commit": identity["commit"], "agent_version": info.get("version"),
            "agent_sha256": bundled, "ota_sha256": entry["ota_update_checksum"],
            "ota_path": entry["ota_update_path"]}


def prepare(wendy, device, entries, verify_only=False):
    info = device_info(wendy, device)
    if runtime_baseline(info):
        return {"device": device, "mode": "linux-runtime-baseline",
                "agent_version": info.get("version"), "agent_sha256": info.get("binarySha256"),
                "os_version": info.get("osVersion"), "note": "Existing Linux OS; no candidate OS installed"}
    entry = select_entry(entries, info, read_identity(wendy, device))
    if not verify_only:
        with tempfile.TemporaryDirectory(prefix="wendy-candidate-os-") as directory:
            artifact = Path(directory) / "candidate.wendy"
            download_verified(entry, artifact)
            # The CLI waits for reboot and the OS healthcheck result. The exact
            # rootfs stamps below also reject a rollback to another build of the
            # same DISTRO_VERSION, which the user-facing OS version cannot do.
            command(wendy, ["os", "update", str(artifact), "--device", device], timeout=1800)
    evidence = verify_boot(entry, device_info(wendy, device), read_identity(wendy, device))
    return {"device": device, "mode": "candidate-os", **evidence}


def write_evidence(path, data):
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(data, indent=2) + "\n")


def prior_evidence(path, device, version, commit):
    previous = json.loads(Path(path).read_text())
    if (not isinstance(previous, dict) or previous.get("device") != device or
            previous.get("status") != "verified" or
            previous.get("mode") not in {"candidate-os", "linux-runtime-baseline"}):
        raise ValueError("post-suite verification requires this device's successful preparation evidence")
    if previous["mode"] == "candidate-os":
        if previous.get("builder_commit") != commit or previous.get("build_version") != version:
            raise ValueError("preparation evidence belongs to a different candidate")
        for field in ("agent_sha256", "ota_sha256"):
            digest = previous.get(field, "")
            if not isinstance(digest, str) or not HEX.fullmatch(digest):
                raise ValueError("preparation evidence has no valid candidate hashes")
    return previous


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wendy", required=True)
    parser.add_argument("--device")
    parser.add_argument("--metadata-dir", required=True)
    parser.add_argument("--expected-version", required=True)
    parser.add_argument("--expected-commit", required=True)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--plan", action="store_true")
    parser.add_argument("--devices-json")
    parser.add_argument("--verify-only", action="store_true")
    args = parser.parse_args()
    try:
        entries = load_entries(args.metadata_dir, args.expected_version, args.expected_commit)
        if args.plan:
            if not args.devices_json or args.device or args.verify_only:
                raise ValueError("--plan requires --devices-json and no per-device mode")
            raw = args.devices_json
            devices = json.loads(raw if raw.lstrip().startswith("[") else Path(raw).read_text())
            candidates = plan(args.wendy, devices, entries)
            write_evidence(args.evidence, {"status": "planned", "version": args.expected_version,
                                          "commit": args.expected_commit, "devices": candidates,
                                          "scope": "Required rack matrix gates the whole nightly; other image variants are not hardware-tested"})
            print(json.dumps([c["device"] for c in candidates]))
        else:
            if not args.device:
                raise ValueError("--device is required outside --plan")
            previous = prior_evidence(args.evidence, args.device, args.expected_version,
                                      args.expected_commit) if args.verify_only else None
            result = prepare(args.wendy, args.device, entries, args.verify_only)
            if previous is not None:
                for field in ("mode", "board", "storage", "build_version", "builder_commit",
                              "agent_version", "agent_sha256", "ota_sha256"):
                    if previous.get(field) != result.get(field):
                        raise ValueError(f"{field} changed between preparation and post-suite verification")
            write_evidence(args.evidence, {"status": "verified", "postSuiteVerified": args.verify_only, **result})
        return 0
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        write_evidence(args.evidence, {"status": "failed", "device": args.device, "error": str(error)})
        print(f"ERROR: candidate OS qualification: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
