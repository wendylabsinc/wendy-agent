#!/usr/bin/env python3
"""Candidate OS checks never reach a device when identity or bytes disagree."""

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("prepare_os", Path(__file__).with_name("integration-prepare-os.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

SHA = "a" * 40
AGENT = "b" * 64
VERSION = "nightly-20261018T120000"
PAYLOAD = b"candidate OTA bytes"


def entry(board="raspberry-pi-5", storage="sd", **extra):
    return {"device": board, "build_storage": storage, "build_sha": SHA,
            "version": VERSION, "aliases": [],
            "ota_update_path": f"images/{board}/{VERSION}/image.wendy",
            "ota_update_checksum": hashlib.sha256(PAYLOAD).hexdigest(),
            "ota_update_size": len(PAYLOAD), **extra}


def info(board="raspberry-pi-5", arch="arm64"):
    return {"deviceType": board, "os": "linux", "osVersion": "WendyOS-0.20.0",
            "cpuArchitecture": arch, "version": "2026.10.17-123916"}


def identity(board="raspberry-pi-5", storage="sd"):
    return {"BOARD": board, "STORAGE": storage, "commit": SHA,
            "build-version": VERSION, "agent-sha256": AGENT,
            "installed-agent-sha256": AGENT, "running-agent-sha256": AGENT}


class Response(io.BytesIO):
    def geturl(self):
        return module.PUBLIC_BASE + "candidate.wendy"


class CandidateOSTest(unittest.TestCase):
    @patch.object(module, "prepare")
    def test_cli_marks_post_suite_evidence_only_after_matching_recheck(self, prepare):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "manifest-entry-pi-sd.json").write_text(json.dumps(entry()))
            evidence = root / "evidence.json"
            prepare.return_value = {"device": "pi", "mode": "candidate-os",
                                    **module.verify_boot(entry(), info(), identity())}
            argv = ["prepare", "--wendy", "wendy", "--device", "pi", "--metadata-dir", directory,
                    "--expected-version", VERSION, "--expected-commit", SHA, "--evidence", str(evidence)]
            with patch.object(module.sys, "argv", argv):
                self.assertEqual(module.main(), 0)
            self.assertFalse(json.loads(evidence.read_text())["postSuiteVerified"])
            with patch.object(module.sys, "argv", [*argv, "--verify-only"]):
                self.assertEqual(module.main(), 0)
            self.assertTrue(json.loads(evidence.read_text())["postSuiteVerified"])
            prepare.return_value = {**prepare.return_value, "agent_sha256": "c" * 64}
            with patch.object(module.sys, "argv", [*argv, "--verify-only"]), patch.object(module.sys, "stderr", io.StringIO()):
                self.assertEqual(module.main(), 1)
            self.assertEqual(json.loads(evidence.read_text())["status"], "failed")

    def test_post_suite_check_requires_prior_matching_preparation(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "evidence.json"
            with self.assertRaises(OSError):
                module.prior_evidence(path, "pi", VERSION, SHA)
            good = {"device": "pi", "status": "verified", "mode": "candidate-os",
                    "build_version": VERSION, "builder_commit": SHA,
                    "agent_sha256": AGENT, "ota_sha256": "c" * 64,
                    "postSuiteVerified": False}
            for change in ({"device": "other"}, {"status": "failed"}, {"build_version": "old"},
                           {"builder_commit": "c" * 40}, {"ota_sha256": ""}):
                path.write_text(json.dumps({**good, **change}))
                with self.subTest(change=change), self.assertRaises(ValueError):
                    module.prior_evidence(path, "pi", VERSION, SHA)
            path.write_text(json.dumps(good))
            self.assertEqual(module.prior_evidence(path, "pi", VERSION, SHA), good)

    def test_rejects_missing_or_stale_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, "missing"):
                module.load_entries(directory, VERSION, SHA)
            path = Path(directory) / "manifest-entry-pi-sd.json"
            for change in ({"version": "old"}, {"build_sha": "c" * 40}, {"build_storage": ""}, {"pr": 123}):
                path.write_text(json.dumps(entry(**change)))
                with self.subTest(change=change), self.assertRaises(ValueError):
                    module.load_entries(directory, VERSION, SHA)
            path.write_text(json.dumps(entry()))
            self.assertEqual(len(module.load_entries(directory, VERSION, SHA)), 1)

    def test_rejects_duplicate_variants(self):
        with tempfile.TemporaryDirectory() as directory:
            for name in ("a", "b"):
                (Path(directory) / f"manifest-entry-{name}.json").write_text(json.dumps(entry()))
            with self.assertRaisesRegex(ValueError, "duplicate"):
                module.load_entries(directory, VERSION, SHA)

    def test_exact_storage_and_authenticated_board_required(self):
        sd, nvme = entry(), entry(storage="nvme")
        self.assertEqual(module.select_entry([sd, nvme], info(), identity())["build_storage"], "sd")
        for live in (identity(storage="emmc"), {"BOARD": "raspberry-pi-5"}, identity(board="raspberry-pi-4")):
            with self.subTest(identity=live), self.assertRaises(ValueError):
                module.select_entry([sd, nvme], info(), live)
        with self.assertRaisesRegex(ValueError, "disagrees"):
            module.select_entry([sd], info("jetson-agx-orin"), identity())

    def test_explicit_alias_maps_to_exact_variant(self):
        candidate = entry("jetson-agx-orin", "emmc", aliases=["jetson-agx-orin-emmc"])
        self.assertIs(module.select_entry([candidate], info("jetson-agx-orin-emmc"),
                                         identity("jetson-agx-orin-emmc", "emmc")), candidate)

    def test_legacy_storage_requires_unambiguous_authenticated_identity(self):
        candidate = entry("jetson-orin-nano", "nvme")
        legacy = {"BOARD": "jetson-orin-nano", "MACHINE": "jetson-orin-nano-devkit-nvme-wendyos"}
        live = {**info("jetson-orin-nano"), "storageMedium": "nvme"}
        self.assertIs(module.select_entry([candidate], live, legacy), candidate)
        self.assertIs(module.select_entry([candidate], info("jetson-orin-nano"), legacy), candidate)
        self.assertIs(module.select_entry([candidate], live, {"BOARD": "jetson-orin-nano"}), candidate)
        for fields, reported in (({**legacy, "STORAGE": "emmc"}, live),
                                 (legacy, {**live, "storageMedium": "emmc"}),
                                 ({"BOARD": "jetson-orin-nano", "STORAGE": "sd"}, live),
                                 ({**legacy, "MACHINE": "board-nvme-emmc"}, live),
                                 ({"BOARD": "jetson-orin-nano"}, info("jetson-orin-nano"))):
            with self.subTest(fields=fields, reported=reported), self.assertRaises(ValueError):
                module.select_entry([candidate], reported, fields)

    def test_legacy_plain_board_is_supported_but_never_guesses_storage(self):
        raw = "WENDY_CANDIDATE_IDENTITY_BEGIN\n# old board format\nraspberry-pi-5\n\ncommit=old\nWENDY_CANDIDATE_IDENTITY_END\n"
        live = module.parse_identity(raw)
        self.assertEqual(live["BOARD"], "raspberry-pi-5")
        self.assertIsNotNone(module.select_entry([entry()], {**info(), "storageMedium": "sd"}, live))
        with self.assertRaisesRegex(ValueError, "exact WendyOS BOARD and STORAGE"):
            module.select_entry([entry()], info(), live)
        with self.assertRaisesRegex(ValueError, "duplicate"):
            module.parse_identity(raw.replace("commit=old", "BOARD=other"))

    def test_arduino_uno_q_is_a_qualcomm_rack_member(self):
        self.assertEqual(module.family("arduino-uno-q"), "qualcomm")
        self.assertEqual(module.select_entry([entry("arduino-uno-q", "emmc")],
                         info("arduino-uno-q"), identity("arduino-uno-q", "emmc"))["device"], "arduino-uno-q")

    def test_rejects_unversioned_or_unsafe_ota_paths(self):
        for path in (None, "https://evil.invalid/a.wendy", "/etc/passwd", "../a.wendy",
                     f"images/{VERSION}/../a.wendy", "images/latest/a.wendy", f"images/{VERSION}/a.wic"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                module.validate_ota(entry(ota_update_path=path))

    def test_hash_size_and_redirect_checks(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "candidate.wendy"
            module.download_verified(entry(), path, lambda *a, **k: Response(PAYLOAD))
            self.assertEqual(path.read_bytes(), PAYLOAD)
            for payload in (b"short", PAYLOAD + b"extra", b"x" * len(PAYLOAD)):
                with self.subTest(payload=payload), self.assertRaises(ValueError):
                    module.download_verified(entry(), path, lambda *a, **k: Response(payload))
            response = Response(PAYLOAD)
            response.geturl = lambda: "https://evil.invalid/a.wendy"
            with self.assertRaisesRegex(ValueError, "redirected"):
                module.download_verified(entry(), path, lambda *a, **k: response)

    def test_rollback_and_background_agent_updates_rejected(self):
        for change in ({"commit": "c" * 40}, {"build-version": "old"},
                       {"running-agent-sha256": "c" * 64},
                       {"installed-agent-sha256": "c" * 64}, {"agent-sha256": ""}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                module.verify_boot(entry(), info(), {**identity(), **change})
        evidence = module.verify_boot(entry(), info(), identity())
        self.assertEqual(evidence["agent_sha256"], AGENT)
        self.assertEqual(evidence["build_version"], VERSION)

    def test_host_shell_requires_complete_verdict_not_just_zero_exit(self):
        good = "CLI banner\r\nWENDY_CANDIDATE_IDENTITY_BEGIN\r\nBOARD=pi\r\nWENDY_CANDIDATE_IDENTITY_END\r\n"
        self.assertEqual(module.parse_identity(good), {"BOARD": "pi"})
        for bad in ("", good.replace("WENDY_CANDIDATE_IDENTITY_END", ""),
                    good.replace("BOARD=pi", "BOARD=pi\nBOARD=other")):
            with self.subTest(output=bad), self.assertRaises(ValueError):
                module.parse_identity(bad)

    @patch.object(module, "command")
    @patch.object(module, "download_verified")
    @patch.object(module, "read_identity", return_value=identity())
    @patch.object(module, "device_info", return_value=info())
    def test_wrong_candidate_or_corrupt_bytes_never_update(self, get_info, get_identity, download, command):
        with self.assertRaises(ValueError):
            module.prepare("wendy", "pi.local", [entry(storage="nvme")])
        download.assert_not_called()
        command.assert_not_called()
        download.side_effect = ValueError("bad checksum")
        with self.assertRaisesRegex(ValueError, "checksum"):
            module.prepare("wendy", "pi.local", [entry()])
        command.assert_not_called()

    @patch.object(module, "command")
    @patch.object(module, "download_verified")
    @patch.object(module, "read_identity", return_value=identity())
    @patch.object(module, "device_info", return_value=info())
    def test_update_uses_only_verified_local_bytes_and_can_recheck(self, get_info, get_identity, download, command):
        evidence = module.prepare("wendy", "pi.local", [entry()])
        self.assertEqual(evidence["mode"], "candidate-os")
        args = command.call_args.args[1]
        self.assertEqual(args[:2], ["os", "update"])
        self.assertTrue(args[2].endswith("candidate.wendy"))
        self.assertNotIn("--nightly", args)
        command.reset_mock()
        download.reset_mock()
        module.prepare("wendy", "pi.local", [entry()], verify_only=True)
        command.assert_not_called()
        download.assert_not_called()

    @patch.object(module, "read_identity")
    @patch.object(module, "device_info")
    def test_required_rack_gate_includes_explicit_linux_baseline(self, get_info, get_identity):
        boards = {"pi": "raspberry-pi-5", "jetson": "jetson-agx-orin", "qcom": "dragonwing-iq-8275"}
        entries = [entry(b) for b in boards.values()]
        baseline = {"os": "ubuntu", "osVersion": "Ubuntu 24.04", "cpuArchitecture": "amd64", "version": "old"}
        get_info.side_effect = lambda w, d: baseline if d == "runtime" else info(boards[d])
        get_identity.side_effect = lambda w, d: identity(boards[d])
        devices = [*boards, "runtime"]
        result = module.plan("wendy", devices, entries)
        self.assertEqual(result[-1]["mode"], "linux-runtime-baseline")
        for missing in devices:
            with self.subTest(missing=missing), self.assertRaisesRegex(ValueError, "coverage missing"):
                module.plan("wendy", [d for d in devices if d != missing], entries)

    @patch.object(module, "command")
    @patch.object(module, "device_info", return_value={"os": "ubuntu", "osVersion": "Ubuntu",
                                                     "cpuArchitecture": "amd64", "version": "old"})
    def test_vendor_linux_is_never_flashed(self, get_info, command):
        result = module.prepare("wendy", "runtime", [entry()])
        command.assert_not_called()
        self.assertEqual(result["mode"], "linux-runtime-baseline")


if __name__ == "__main__":
    unittest.main()
