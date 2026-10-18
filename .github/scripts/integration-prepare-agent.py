#!/usr/bin/env python3
"""Install a CI candidate and require its exact running executable SHA-256."""

import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request


ARCHITECTURES = {"amd64": "amd64", "x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}
GCS_BASE = "https://storage.googleapis.com/wendyos-images-public"
MANIFEST_URL = GCS_BASE + "/agent/manifest.json"


def archive_binary(archive, arch):
    # Read just the regular binary. Never extract arbitrary archive paths,
    # links, or metadata onto the runner filesystem.
    entries = [entry for entry in archive.getmembers()
               if entry.name.removeprefix("./") == f"wendy-agent-linux-{arch}/wendy-agent"]
    if len(entries) != 1 or not entries[0].isfile():
        raise ValueError("candidate archive must contain one regular agent binary")
    stream = archive.extractfile(entries[0])
    if stream is None:
        raise ValueError("candidate archive binary is unreadable")
    return stream.read()


def nightly_candidate(arch):
    # Resolve the channel once. Neither the download nor verification resolves
    # "latest" again, so a publication mid-run cannot change the selected build.
    with urllib.request.urlopen(MANIFEST_URL, timeout=30) as response:
        manifest = json.load(response)
    if not isinstance(manifest, dict):
        raise ValueError("agent manifest must be an object")
    version = manifest.get("latest_nightly")
    if not isinstance(version, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", version):
        raise ValueError("agent manifest has no valid latest_nightly version")
    try:
        release = manifest["versions"][version]
        artifact = release["artifacts"][arch]
        path, checksum, size = artifact["path"], artifact["checksum"], artifact["size_bytes"]
    except (KeyError, TypeError):
        raise ValueError(f"agent manifest has no complete {arch} nightly artifact") from None
    if release.get("is_nightly") is not True:
        raise ValueError("latest_nightly points to a non-nightly release")
    if (not isinstance(path, str) or not re.fullmatch(r"agent/[A-Za-z0-9._/-]+\.tar\.gz", path)
            or any(part in ("", ".", "..") for part in path.split("/"))):
        raise ValueError("agent manifest artifact path is invalid")
    if not isinstance(checksum, str) or not re.fullmatch(r"[0-9a-fA-F]{64}", checksum):
        raise ValueError("agent manifest artifact SHA-256 is required")
    if isinstance(size, bool) or not isinstance(size, int) or not 0 < size <= 512 * 1024 * 1024:
        raise ValueError("agent manifest artifact size is invalid")
    url = GCS_BASE + "/" + path
    with urllib.request.urlopen(url, timeout=120) as response:
        raw = response.read(size + 1)
    if len(raw) != size:
        raise ValueError("nightly artifact size differs from manifest")
    if hashlib.sha256(raw).hexdigest() != checksum.lower():
        raise ValueError("nightly artifact SHA-256 differs from manifest")
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
        data = archive_binary(archive, arch)
    return data, url, version, checksum.lower()


def cli_json(wendy, *args, timeout=45):
    result = subprocess.run([wendy, *args, "--json"], stdin=subprocess.DEVNULL,
                            capture_output=True, text=True, check=True, timeout=timeout)
    value = json.loads(result.stdout)
    if not isinstance(value, dict):
        raise ValueError("CLI did not return a JSON object")
    return value


def architecture(info):
    # Agent OS is the Linux distribution ID, matching osIsLinuxFamily in the
    # CLI. Supported non-Linux agents report darwin or windows.
    if info.get("os", "").lower() in ("", "unknown", "darwin", "macos", "windows"):
        raise ValueError("candidate preparation requires a Linux/WendyOS device")
    arch = ARCHITECTURES.get(info.get("cpuArchitecture", "").lower())
    if arch is None:
        raise ValueError("device did not report a supported CPU architecture")
    return arch


def candidate_bytes(directory, arch, expected_version=""):
    stem = f"wendy-agent-linux-{arch}"
    archives = sorted(directory.rglob(f"{stem}-{expected_version or '*'}.tar.gz"))
    if len(archives) > 1:
        raise ValueError(f"ambiguous candidate archives for {arch}: {len(archives)} found")
    if archives:
        with tarfile.open(archives[0], "r:gz") as archive:
            return archive_binary(archive, arch), str(archives[0])
    paths = [directory / stem / "wendy-agent", directory / stem, directory / f"wendy-agent-{arch}",
             directory / "wendy-agent"]
    binaries = [path for path in paths if path.is_file()]
    if len(binaries) != 1:
        raise ValueError(f"expected exactly one candidate binary for {arch}; found {len(binaries)}")
    return binaries[0].read_bytes(), str(binaries[0])


def validate_elf(data, arch):
    # Linux release agents are 64-bit little-endian ELF executables. Validate
    # before any write RPC, even when a stale target agent skips its own check.
    machine = {"amd64": 62, "arm64": 183}[arch]
    if (len(data) < 64 or data[:6] != b"\x7fELF\x02\x01"
            or int.from_bytes(data[18:20], "little") != machine):
        raise ValueError(f"candidate is not a Linux {arch} ELF executable")


def wait_for_candidate(wendy, device, digest, expected_version="", timeout=90):
    deadline = time.monotonic() + timeout
    last_reason = "device did not respond"
    while True:
        try:
            info = cli_json(wendy, "device", "info", "--device", device,
                            timeout=min(30, max(1, deadline - time.monotonic())))
            reported_hash = info.get("binarySha256", "")
            if not isinstance(reported_hash, str) or not re.fullmatch(r"[0-9a-fA-F]{64}", reported_hash):
                last_reason = "running agent did not report an executable SHA-256"
            elif reported_hash.lower() != digest:
                last_reason = "running executable SHA-256 differs from candidate"
            elif expected_version and info.get("version") != expected_version:
                last_reason = "running agent version differs from candidate version"
            else:
                # The info RPC may be exposed during setup. Require authenticated
                # app access on this newly restarted agent before app tests begin.
                subprocess.run([wendy, "device", "apps", "list", "--device", device, "--json"],
                               stdin=subprocess.DEVNULL, capture_output=True, text=True,
                               check=True, timeout=min(30, max(1, deadline - time.monotonic())))
                return info
        except (subprocess.SubprocessError, ValueError, OSError) as error:
            last_reason = type(error).__name__
        if time.monotonic() >= deadline:
            raise ValueError(f"candidate verification failed: {last_reason}")
        time.sleep(min(2, max(0, deadline - time.monotonic())))


def prepare(wendy, device, artifact_dir, evidence, expected_version="", expected_commit="", nightly=False):
    # An earlier attempt's evidence must never survive a failed preparation.
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.unlink(missing_ok=True)
    info = cli_json(wendy, "device", "info", "--device", device)
    arch = architecture(info)
    archive_digest = ""
    if nightly:
        data, source, expected_version, archive_digest = nightly_candidate(arch)
    else:
        data, source = candidate_bytes(artifact_dir, arch, expected_version)
    validate_elf(data, arch)
    digest = hashlib.sha256(data).hexdigest()
    with tempfile.TemporaryDirectory(prefix="wendy-ci-agent-") as temp:
        binary = Path(temp) / "wendy-agent"
        binary.write_bytes(data)
        # JSON mode updates only the agent and intentionally skips OS updates.
        result = cli_json(wendy, "device", "update", "--device", device,
                          "--binary", str(binary), timeout=240)
        if result.get("status") != "success":
            raise ValueError("candidate upload did not report success")
    running = wait_for_candidate(wendy, device, digest, expected_version)
    record = {"device": device, "architecture": arch, "artifact": source,
              "sourceCommit": expected_commit, "version": running.get("version", ""),
              "candidateSha256": digest, "runningSha256": running["binarySha256"].lower(),
              "verified": True}
    if nightly:
        record.update({"channel": "nightly", "manifest": MANIFEST_URL,
                       "archiveSha256": archive_digest})
    evidence.write_text(json.dumps(record, indent=2) + "\n")
    print(f"Verified candidate on {device}: {digest}", file=sys.stderr)
    return record


def verify_existing(wendy, device, evidence):
    record = json.loads(evidence.read_text())
    if not isinstance(record, dict) or record.get("device") != device or record.get("verified") is not True:
        raise ValueError("candidate evidence is missing or belongs to another device")
    digest = record.get("candidateSha256", "")
    if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise ValueError("candidate evidence has no valid executable SHA-256")
    # Clear any previous successful final verdict before rechecking. If this
    # check fails, an aggregator must not consume stale final verification.
    record["postSuiteVerified"] = False
    evidence.write_text(json.dumps(record, indent=2) + "\n")
    wait_for_candidate(wendy, device, digest, record.get("version", ""))
    record["postSuiteVerified"] = True
    evidence.write_text(json.dumps(record, indent=2) + "\n")
    print(f"Candidate still running on {device}: {digest}", file=sys.stderr)
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wendy", required=True)
    parser.add_argument("--device", required=True)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--artifact-dir", type=Path)
    source.add_argument("--nightly", action="store_true")
    source.add_argument("--verify-only", action="store_true")
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument("--expected-version", default="")
    parser.add_argument("--expected-commit", default="",
                        help="source revision bound to the downloaded artifacts by the workflow")
    args = parser.parse_args()
    if (args.nightly or args.verify_only) and (args.expected_version or args.expected_commit):
        parser.error("--expected-version and --expected-commit apply only to --artifact-dir")
    try:
        if args.verify_only:
            verify_existing(args.wendy, args.device, args.evidence)
        else:
            prepare(args.wendy, args.device, args.artifact_dir, args.evidence,
                    args.expected_version, args.expected_commit, args.nightly)
    except (subprocess.SubprocessError, ValueError, OSError, tarfile.TarError) as error:
        # Avoid CLI stderr, which can contain credential diagnostics.
        detail = str(error) if isinstance(error, ValueError) else type(error).__name__
        print(f"ERROR: candidate agent preparation failed: {detail}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
