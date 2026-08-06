"""Tests for artifact manifests with checksum verification."""

import json

import pytest

from wendytrain.manifest import ManifestError, verify_manifest, write_manifest


@pytest.fixture()
def artifact_dir(tmp_path):
    (tmp_path / "theta.wtw").write_bytes(b"model bytes here")
    (tmp_path / "policy.json").write_bytes(b'{"hidden": [32]}')
    return tmp_path


def write(artifact_dir):
    return write_manifest(
        artifact_dir,
        files=["theta.wtw", "policy.json"],
        inputs={"obs": [4]},
        outputs={"action": [1]},
        layout="obs = [x, x_dot, theta, theta_dot]",
        framework="numpy",
        extra={"generation": 12},
    )


def test_write_then_verify_passes(artifact_dir):
    path = write(artifact_dir)
    assert path == artifact_dir / "manifest.json"
    verify_manifest(artifact_dir)  # must not raise


def test_manifest_contents(artifact_dir):
    manifest = json.loads(write(artifact_dir).read_text())
    assert manifest["inputs"] == {"obs": [4]}
    assert manifest["outputs"] == {"action": [1]}
    assert manifest["layout"] == "obs = [x, x_dot, theta, theta_dot]"
    assert manifest["framework"] == "numpy"
    assert manifest["generation"] == 12
    assert "T" in manifest["created"]  # International Organization for Standardization (ISO) timestamp
    by_name = {f["name"]: f for f in manifest["files"]}
    assert by_name["theta.wtw"]["size"] == len(b"model bytes here")
    assert len(by_name["theta.wtw"]["sha256"]) == 64


def test_flipped_byte_is_detected_and_named(artifact_dir):
    write(artifact_dir)
    original = bytearray((artifact_dir / "theta.wtw").read_bytes())
    original[0] ^= 0xFF
    (artifact_dir / "theta.wtw").write_bytes(bytes(original))
    with pytest.raises(ManifestError, match="theta.wtw"):
        verify_manifest(artifact_dir)


def test_missing_file_is_named(artifact_dir):
    write(artifact_dir)
    (artifact_dir / "policy.json").unlink()
    with pytest.raises(ManifestError, match="policy.json"):
        verify_manifest(artifact_dir)


def test_missing_manifest_itself_raises(tmp_path):
    with pytest.raises(ManifestError, match="manifest.json"):
        verify_manifest(tmp_path)


def test_manifest_is_deterministic_apart_from_timestamp(artifact_dir):
    first = json.loads(write(artifact_dir).read_text())
    second = json.loads(write(artifact_dir).read_text())
    first.pop("created")
    second.pop("created")
    assert first == second
