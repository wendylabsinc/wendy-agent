"""Artifact manifests: the contract between a trained artifact and its consumer.

A manifest records input and output shapes, a human-readable observation
layout, the producing framework, and a file list with sizes and SHA-256
checksums. ``verify_manifest`` fails loudly on the first missing or modified
file, turning "training finished" into "deployable artifact".
"""

import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path

MANIFEST_NAME = "manifest.json"


class ManifestError(Exception):
    """A manifest is missing, or a listed file is missing or modified."""


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_manifest(
    directory: str | Path,
    *,
    files: list[str | Path],
    inputs: dict,
    outputs: dict,
    layout: str,
    framework: str,
    extra: dict | None = None,
) -> Path:
    """Write ``manifest.json`` into ``directory`` and return its path.

    ``files`` are paths relative to ``directory`` (absolute paths inside it
    are accepted and stored relative). ``inputs`` and ``outputs`` are shape
    dictionaries, ``layout`` describes the observation layout in prose, and
    ``extra`` keys are merged into the top level of the manifest. The manifest
    is deterministic apart from the ``created`` timestamp (International
    Organization for Standardization (ISO) 8601, Coordinated Universal Time).
    """
    directory = Path(directory)
    entries = []
    for item in files:
        path = Path(item)
        if path.is_absolute():
            path = path.relative_to(directory)
        full = directory / path
        entries.append(
            {
                "name": str(path),
                "size": full.stat().st_size,
                "sha256": _sha256(full),
            }
        )
    manifest = {
        "created": datetime.now(timezone.utc).isoformat(),
        "framework": framework,
        "inputs": inputs,
        "outputs": outputs,
        "layout": layout,
        "files": sorted(entries, key=lambda e: e["name"]),
        **(extra or {}),
    }
    path = directory / MANIFEST_NAME
    path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    return path


def verify_manifest(directory: str | Path) -> None:
    """Verify every file listed in ``directory``'s manifest.

    Raises ``ManifestError`` naming the manifest when it is absent, or naming
    the first listed file that is missing or whose size or SHA-256 checksum
    does not match. Returns None when everything matches.
    """
    directory = Path(directory)
    manifest_path = directory / MANIFEST_NAME
    if not manifest_path.is_file():
        raise ManifestError(f"no {MANIFEST_NAME} in {directory}")
    manifest = json.loads(manifest_path.read_text())
    for entry in manifest["files"]:
        full = directory / entry["name"]
        if not full.is_file():
            raise ManifestError(f"missing file {entry['name']!r} listed in {MANIFEST_NAME}")
        size = full.stat().st_size
        if size != entry["size"]:
            raise ManifestError(
                f"size mismatch for {entry['name']!r}: manifest says {entry['size']}, "
                f"file has {size}"
            )
        digest = _sha256(full)
        if digest != entry["sha256"]:
            raise ManifestError(
                f"checksum mismatch for {entry['name']!r}: manifest says "
                f"{entry['sha256']}, file hashes to {digest}"
            )
