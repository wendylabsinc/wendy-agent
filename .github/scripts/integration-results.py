#!/usr/bin/env python3
"""Fail qualification unless every expected job and required runtime passed."""

import argparse
import json
from pathlib import Path
import re


REQUIRED = {"raspberry-pi", "jetson", "qualcomm", "generic-linux-x86"}
HEX = re.compile(r"[0-9a-f]{64}")


def validate_candidate(result, sha, os_version="", os_sha="", candidate_version=""):
    """Require post-suite executable/OS proof, not just a successful wrapper exit."""
    candidate = result.get("candidate")
    if not isinstance(candidate, dict) or candidate.get("device") != result.get("hostname"):
        raise ValueError("Missing candidate evidence or wrong candidate device")
    if result.get("source_sha") != sha or result.get("candidate_version", "") != candidate_version:
        raise ValueError("Wrong source commit or release version in candidate result")
    if candidate.get("postSuiteVerified") is not True:
        raise ValueError("Candidate has no successful post-suite verification")
    if type(result.get("containerd_major")) is not int or result["containerd_major"] not in (1, 2):
        raise ValueError("Candidate has no supported containerd server major")
    kind = result.get("candidate_kind")
    if kind == "agent":
        digest = candidate.get("candidateSha256")
        if (candidate.get("sourceCommit") != sha or candidate.get("verified") is not True
                or not isinstance(digest, str) or not HEX.fullmatch(digest)
                or candidate.get("runningSha256") != digest):
            raise ValueError("Agent evidence does not prove the expected source and running binary")
        if candidate.get("version") != (candidate_version or f"ci-{sha}"):
            raise ValueError("Agent evidence does not match the expected candidate version")
    elif kind == "os":
        if (not os_version or not os_sha or candidate.get("status") != "verified"
                or candidate.get("mode") != "candidate-os"
                or candidate.get("builder_commit") != os_sha
                or candidate.get("build_version") != os_version):
            raise ValueError("OS evidence does not prove the expected builder commit and version")
        if any(not isinstance(candidate.get(key), str) or not HEX.fullmatch(candidate[key])
               for key in ("agent_sha256", "ota_sha256")):
            raise ValueError("OS evidence is missing executable or OTA SHA-256")
        digest = candidate["agent_sha256"]
    else:
        raise ValueError("Unknown candidate kind")
    info = result.get("device_info")
    if not isinstance(info, dict) or info.get("binarySha256") != digest:
        raise ValueError("Final device snapshot does not match the verified running candidate")


def verify(results, hosts, platforms, sha, require_matrix, os_version="", os_sha="", candidate_version=""):
    expected = {(host, platform) for host in hosts for platform in platforms}
    if not expected:
        raise ValueError("Qualification has no expected device jobs")
    seen = set()
    families, majors, os_families = set(), set(), set()
    for result in results:
        key = result.get("hostname"), result.get("host_platform")
        if key not in expected or key in seen:
            raise ValueError(f"Unexpected or duplicate qualification result: {key}")
        if result.get("passed") is not True or result.get("source_sha") != sha:
            raise ValueError(f"Failed or stale candidate result: {key}")
        if os_version and (result.get("os_candidate_version") != os_version or
                           result.get("os_candidate_sha") != os_sha):
            raise ValueError(f"Wrong OS candidate result: {key}")
        validate_candidate(result, sha, os_version, os_sha, candidate_version)
        seen.add(key)
        families.add(result.get("family"))
        majors.add(result.get("containerd_major"))
        if result.get("candidate_kind") == "os":
            os_families.add(result.get("family"))
    if seen != expected:
        raise ValueError(f"Missing qualification results: {sorted(expected - seen)}")
    if require_matrix:
        if REQUIRED - families:
            raise ValueError(f"Missing required device families: {sorted(REQUIRED - families)}")
        if {1, 2} - majors:
            raise ValueError(f"Missing tested containerd server majors: {sorted({1, 2} - majors)}")
        if os_version and {"raspberry-pi", "jetson", "qualcomm"} - os_families:
            raise ValueError("OS candidate was not tested on all required physical families")
    return len(seen)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results", type=Path, required=True)
    parser.add_argument("--hosts", required=True)
    parser.add_argument("--platforms", required=True)
    parser.add_argument("--source-sha", required=True)
    parser.add_argument("--require-matrix", action="store_true")
    parser.add_argument("--os-version", default="")
    parser.add_argument("--os-sha", default="")
    parser.add_argument("--candidate-version", default="")
    args = parser.parse_args()
    results = [json.loads(path.read_text()) for path in args.results.rglob("result.json")]
    count = verify(results, json.loads(args.hosts), args.platforms.split(","), args.source_sha,
                   args.require_matrix, args.os_version, args.os_sha, args.candidate_version)
    print(f"Qualified {count} device/host jobs; source {args.source_sha}")


if __name__ == "__main__":
    main()
