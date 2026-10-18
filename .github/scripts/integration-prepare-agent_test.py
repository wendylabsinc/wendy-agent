#!/usr/bin/env python3
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "prepare_agent", Path(__file__).with_name("integration-prepare-agent.py"))
prepare_agent = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare_agent)


def elf(arch="amd64"):
    data = bytearray(64)
    data[:6] = b"\x7fELF\x02\x01"
    data[18:20] = {"amd64": 62, "arm64": 183}[arch].to_bytes(2, "little")
    return bytes(data)


class PreparationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.digest = hashlib.sha256(elf()).hexdigest()

    def archive(self, name="wendy-agent-linux-amd64-candidate.tar.gz", member=None, symlink=False):
        path = self.root / name
        with tarfile.open(path, "w:gz") as archive:
            entry = tarfile.TarInfo(member or "wendy-agent-linux-amd64/wendy-agent")
            entry.size = len(elf())
            if symlink:
                entry.type, entry.linkname = tarfile.SYMTYPE, "/etc/passwd"
            archive.addfile(entry, io.BytesIO(elf()))
        return path

    def test_reads_exact_candidate_archive_without_extracting_paths(self):
        archive = self.archive()
        data, source = prepare_agent.candidate_bytes(self.root, "amd64", "candidate")
        self.assertEqual(data, elf())
        self.assertEqual(source, str(archive))
        self.assertEqual(list(self.root.iterdir()), [archive])

    def test_rejects_ambiguous_archives_and_wrong_requested_version(self):
        self.archive()
        self.archive("wendy-agent-linux-amd64-other.tar.gz")
        with self.assertRaisesRegex(ValueError, "ambiguous"):
            prepare_agent.candidate_bytes(self.root, "amd64")
        with self.assertRaises(ValueError):
            prepare_agent.candidate_bytes(self.root, "amd64", "missing")

    def test_rejects_symlink_or_missing_archive_binary(self):
        for member, symlink in ((None, True), ("../../wendy-agent", False)):
            with self.subTest(member=member, symlink=symlink):
                self.archive(member=member, symlink=symlink)
                with self.assertRaisesRegex(ValueError, "regular agent binary"):
                    prepare_agent.candidate_bytes(self.root, "amd64")

    def test_raw_and_extracted_candidates(self):
        raw = self.root / "wendy-agent"
        raw.write_bytes(elf())
        self.assertEqual(prepare_agent.candidate_bytes(self.root, "amd64")[0], elf())
        raw.unlink()
        built = self.root / "wendy-agent-linux-amd64"
        built.write_bytes(elf())
        self.assertEqual(prepare_agent.candidate_bytes(self.root, "amd64")[0], elf())
        extracted = self.root / "wendy-agent-linux-arm64" / "wendy-agent"
        extracted.parent.mkdir()
        extracted.write_bytes(elf("arm64"))
        self.assertEqual(prepare_agent.candidate_bytes(self.root, "arm64")[0], elf("arm64"))

    def test_refuses_wrong_os_architecture_and_non_elf_before_upload(self):
        for info in ({"os": "darwin", "cpuArchitecture": "amd64"},
                     {"os": "linux", "cpuArchitecture": "arm"}, {}):
            with self.subTest(info=info), self.assertRaises(ValueError):
                prepare_agent.architecture(info)
        self.assertEqual(prepare_agent.architecture({"os": "linux", "cpuArchitecture": "x86_64"}), "amd64")
        self.assertEqual(prepare_agent.architecture({"os": "ubuntu", "cpuArchitecture": "amd64"}), "amd64")
        for data in (b"#!/bin/sh\nexit 0", elf("arm64")):
            with self.subTest(data=data), self.assertRaises(ValueError):
                prepare_agent.validate_elf(data, "amd64")

    def test_requires_running_hash_and_exact_version(self):
        invalid = ({"version": "candidate"},
                   {"version": "candidate", "binarySha256": "b" * 64},
                   {"version": "old", "binarySha256": self.digest})
        for info in invalid:
            with self.subTest(info=info), patch.object(prepare_agent, "cli_json", return_value=info), \
                    self.assertRaisesRegex(ValueError, "candidate verification failed"):
                prepare_agent.wait_for_candidate("wendy", "fixture", self.digest, "candidate", timeout=0)

    def test_retries_old_process_and_requires_authenticated_app_rpc(self):
        candidate = {"version": "candidate", "binarySha256": self.digest}
        with patch.object(prepare_agent, "cli_json", side_effect=[
                {"version": "old", "binarySha256": "b" * 64}, candidate]), \
                patch.object(prepare_agent.time, "sleep"), \
                patch.object(prepare_agent.subprocess, "run") as run:
            self.assertEqual(prepare_agent.wait_for_candidate(
                "wendy", "fixture", self.digest, "candidate"), candidate)
            self.assertEqual(run.call_args.args[0],
                             ["wendy", "device", "apps", "list", "--device", "fixture", "--json"])
        with patch.object(prepare_agent, "cli_json", return_value=candidate), \
                patch.object(prepare_agent.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "wendy")), \
                self.assertRaises(ValueError):
            prepare_agent.wait_for_candidate("wendy", "fixture", self.digest, timeout=0)

    def test_preparation_uploads_exact_bytes_and_writes_only_verified_evidence(self):
        (self.root / "wendy-agent").write_bytes(elf())
        evidence = self.root / "evidence.json"
        info = {"os": "linux", "cpuArchitecture": "amd64"}

        def cli(wendy, *args, **kwargs):
            if args[:2] == ("device", "info"):
                return info
            self.assertEqual(args[:4], ("device", "update", "--device", "fixture"))
            self.assertEqual(args[4], "--binary")
            self.assertEqual(Path(args[5]).read_bytes(), elf())
            self.assertNotIn("--yes", args)
            return {"status": "success"}

        with patch.object(prepare_agent, "cli_json", side_effect=cli), \
                patch.object(prepare_agent, "wait_for_candidate", return_value={
                    "version": "candidate", "binarySha256": self.digest}):
            record = prepare_agent.prepare("wendy", "fixture", self.root, evidence, "candidate", "source-sha")
        self.assertTrue(evidence.exists())
        self.assertEqual(record["candidateSha256"], record["runningSha256"])
        self.assertEqual(record["sourceCommit"], "source-sha")
        with patch.object(prepare_agent, "cli_json", side_effect=cli), \
                patch.object(prepare_agent, "wait_for_candidate", side_effect=ValueError("wrong hash")), \
                self.assertRaises(ValueError):
            prepare_agent.prepare("wendy", "fixture", self.root, evidence)
        self.assertFalse(evidence.exists())

    def test_malformed_candidate_never_calls_update(self):
        (self.root / "wendy-agent").write_bytes(b"not an agent")
        with patch.object(prepare_agent, "cli_json", return_value={
                "os": "linux", "cpuArchitecture": "amd64"}) as cli, self.assertRaises(ValueError):
            prepare_agent.prepare("wendy", "fixture", self.root, self.root / "evidence.json")
        self.assertEqual(cli.call_count, 1)

    def test_nightly_resolves_once_and_checks_archive_integrity(self):
        raw = self.archive().read_bytes()
        checksum = hashlib.sha256(raw).hexdigest()
        manifest = {"latest_nightly": "candidate", "versions": {"candidate": {
            "is_nightly": True, "artifacts": {"amd64": {
                "path": "agent/candidate/wendy-agent-linux-amd64-candidate.tar.gz",
                "checksum": checksum, "size_bytes": len(raw)}}}}}
        with patch.object(prepare_agent.urllib.request, "urlopen", side_effect=[
                io.BytesIO(json.dumps(manifest).encode()), io.BytesIO(raw)]) as fetch:
            data, url, version, got_checksum = prepare_agent.nightly_candidate("amd64")
        self.assertEqual(data, elf())
        self.assertEqual(version, "candidate")
        self.assertEqual(got_checksum, checksum)
        self.assertEqual(fetch.call_count, 2)
        self.assertEqual(fetch.call_args_list[0].args[0], prepare_agent.MANIFEST_URL)
        self.assertEqual(fetch.call_args_list[1].args[0], url)

        artifact = manifest["versions"]["candidate"]["artifacts"]["amd64"]
        for changed in ({"checksum": "b" * 64}, {"size_bytes": len(raw) + 1},
                        {"checksum": ""}, {"path": "agent/../../other.tar.gz"}):
            with self.subTest(changed=changed):
                bad = json.loads(json.dumps(manifest))
                bad["versions"]["candidate"]["artifacts"]["amd64"].update(changed)
                with patch.object(prepare_agent.urllib.request, "urlopen", side_effect=[
                        io.BytesIO(json.dumps(bad).encode()), io.BytesIO(raw)]), self.assertRaises(ValueError):
                    prepare_agent.nightly_candidate("amd64")
        self.assertEqual(artifact["checksum"], checksum)

    def test_nightly_preparation_pins_resolved_version(self):
        evidence = self.root / "nightly.json"
        with patch.object(prepare_agent, "cli_json", side_effect=[
                {"os": "wendyos", "cpuArchitecture": "amd64"}, {"status": "success"}]), \
                patch.object(prepare_agent, "nightly_candidate", return_value=(
                    elf(), "https://storage.googleapis.com/artifact", "nightly-version", "archive-hash")) as fetch, \
                patch.object(prepare_agent, "wait_for_candidate", return_value={
                    "version": "nightly-version", "binarySha256": self.digest}) as verify:
            record = prepare_agent.prepare("wendy", "fixture", None, evidence, nightly=True)
        self.assertEqual(fetch.call_count, 1)
        self.assertEqual(verify.call_args.args, ("wendy", "fixture", self.digest, "nightly-version"))
        self.assertEqual(record["channel"], "nightly")
        self.assertEqual(record["archiveSha256"], "archive-hash")

    def test_verify_only_rechecks_without_downloading_or_installing(self):
        evidence = self.root / "existing.json"
        record = {"device": "fixture", "verified": True, "candidateSha256": self.digest,
                  "version": "candidate", "postSuiteVerified": True}
        evidence.write_text(json.dumps(record))
        with patch.object(prepare_agent, "wait_for_candidate") as verify, \
                patch.object(prepare_agent, "cli_json") as cli, \
                patch.object(prepare_agent.urllib.request, "urlopen") as fetch:
            verified = prepare_agent.verify_existing("wendy", "fixture", evidence)
        verify.assert_called_once_with("wendy", "fixture", self.digest, "candidate")
        cli.assert_not_called()
        fetch.assert_not_called()
        self.assertTrue(verified["postSuiteVerified"])
        with patch.object(prepare_agent, "wait_for_candidate", side_effect=ValueError("replaced")), \
                self.assertRaises(ValueError):
            prepare_agent.verify_existing("wendy", "fixture", evidence)
        self.assertFalse(json.loads(evidence.read_text())["postSuiteVerified"])
        with self.assertRaisesRegex(ValueError, "another device"):
            prepare_agent.verify_existing("wendy", "another", evidence)


if __name__ == "__main__":
    unittest.main()
