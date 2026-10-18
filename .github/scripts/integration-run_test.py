#!/usr/bin/env python3
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location("run_integration", Path(__file__).with_name("integration-run.py"))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
SHA, OS_SHA, DIGEST = "a" * 40, "b" * 40, "c" * 64


class RunTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.evidence = self.root / "evidence"
        self.info = {"os": "ubuntu", "cpuArchitecture": "amd64", "version": f"ci-{SHA}",
                     "binarySha256": DIGEST}
        self.module = Mock()
        self.module.cli_json.return_value = self.info
        self.module.runtime_major.return_value = 2
        self.module.platform.return_value = "generic-linux-x86"
        self.calls = []
        self.timeouts = []
        self.candidate = {"device": "fixture.local", "sourceCommit": SHA, "version": f"ci-{SHA}",
                          "candidateSha256": DIGEST, "runningSha256": DIGEST,
                          "verified": True, "postSuiteVerified": True}

    def invoke(self, extra=(), failing_stage="", candidate=None, failure=None, cleanup_receipt=None):
        argv = ["integration-run.py", "--wendy", "/fixture/wendy", "--device", "fixture.local",
                "--agents", "/fixture/agents", "--source-sha", SHA,
                "--evidence", str(self.evidence), *extra]

        def execute(args, log, timeout):
            self.calls.append((args, log.name))
            self.timeouts.append(timeout)
            if log.name == "suite.log" and cleanup_receipt is not None:
                Path(os.environ["WENDY_CI_CLEANUP_RECEIPT"]).write_text(cleanup_receipt)
            if log.name == failing_stage:
                raise failure if failure is not None else subprocess.CalledProcessError(1, args)
            if log.name in ("prepare.log", "verify.log"):
                (self.evidence / "candidate.json").write_text(json.dumps(
                    self.candidate if candidate is None else candidate))

        with patch.object(sys, "argv", argv), patch.object(runner, "discovery_module", return_value=self.module), \
                patch.object(runner, "execute", side_effect=execute), \
                patch.dict(os.environ, {"RUNNER_OS": "Linux"}):
            runner.main()
        return json.loads((self.evidence / "result.json").read_text())

    def test_agent_candidate_is_prepared_tested_and_reverified_before_success(self):
        row = self.invoke(extra=("--tests", "python-hello, python-network"))
        self.assertEqual([stage for _, stage in self.calls], ["prepare.log", "suite.log", "verify.log"])
        prepare, suite, verify = [args for args, _ in self.calls]
        self.assertIn("integration-prepare-agent.py", prepare[1])
        self.assertEqual(prepare[prepare.index("--artifact-dir") + 1], "/fixture/agents")
        self.assertEqual(prepare[prepare.index("--expected-commit") + 1], SHA)
        self.assertEqual(suite[-4:], ["-t", "python-hello", "-t", "python-network"])
        self.assertIn("--verify-only", verify)
        self.assertNotIn("--artifact-dir", verify)
        self.assertTrue(row["passed"])
        self.assertEqual(row["containerd_major"], 2)
        self.assertEqual(row["candidate_version"], "")
        self.assertEqual(self.module.cli_json.call_count, 2)
        self.module.runtime_major.assert_called_once_with("/fixture/wendy", "fixture.local")

    def test_failed_preparation_suite_or_final_verification_cannot_leave_success(self):
        for stage in ("prepare.log", "suite.log", "verify.log"):
            self.evidence.mkdir(exist_ok=True)
            (self.evidence / "result.json").write_text('{"passed":true}')
            self.calls = []
            with self.subTest(stage=stage), self.assertRaises(subprocess.CalledProcessError):
                self.invoke(failing_stage=stage)
            self.assertFalse((self.evidence / "result.json").exists())
            self.assertEqual(self.calls[-1][1], stage)

    def test_zero_exit_without_correct_candidate_evidence_cannot_pass(self):
        for candidate in ({}, dict(self.candidate, sourceCommit="d" * 40),
                          dict(self.candidate, postSuiteVerified=False),
                          dict(self.candidate, runningSha256="e" * 64)):
            with self.subTest(candidate=candidate), self.assertRaises(ValueError):
                self.invoke(candidate=candidate)
            self.assertFalse((self.evidence / "result.json").exists())

    def test_runtime_probe_failure_cannot_write_success(self):
        self.module.runtime_major.side_effect = ValueError("no server version")
        with self.assertRaisesRegex(ValueError, "no server version"):
            self.invoke()
        self.assertFalse((self.evidence / "result.json").exists())

    def test_final_device_snapshot_cannot_disagree_with_verified_candidate(self):
        self.module.cli_json.side_effect = [self.info, dict(self.info, binarySha256="e" * 64)]
        with self.assertRaisesRegex(ValueError, "Final device snapshot"):
            self.invoke()
        self.assertFalse((self.evidence / "result.json").exists())

    def test_os_candidate_preserves_metadata_identity_during_verification(self):
        self.info.update(os="linux", osVersion="WendyOS-0.20.0", deviceType="jetson-agx-thor")
        self.module.platform.return_value = "jetson"
        candidate = {"device": "fixture.local", "status": "verified", "mode": "candidate-os",
                     "postSuiteVerified": True, "builder_commit": OS_SHA, "build_version": "os-candidate",
                     "agent_sha256": DIGEST, "ota_sha256": "e" * 64}
        row = self.invoke(extra=("--os-metadata", "/fixture/os", "--os-version", "os-candidate",
                                 "--os-sha", OS_SHA), candidate=candidate)
        prepare, _, verify = [args for args, _ in self.calls]
        self.assertIn("integration-prepare-os.py", prepare[1])
        self.assertNotIn("--artifact-dir", prepare)
        self.assertEqual(verify, prepare + ["--verify-only"])
        self.assertEqual(row["candidate_kind"], "os")
        self.assertEqual(row["os_candidate_sha"], OS_SHA)
        self.assertEqual(self.timeouts[0], 3300)

    def test_generic_linux_uses_agent_candidate_during_os_rack_run(self):
        row = self.invoke(extra=("--os-metadata", "/fixture/os", "--os-version", "os-candidate",
                                 "--os-sha", OS_SHA))
        self.assertIn("integration-prepare-agent.py", self.calls[0][0][1])
        self.assertEqual(row["candidate_kind"], "agent")
        self.assertEqual(self.timeouts[0], 1200)

    def test_stale_receipt_cleared_before_failed_preparation(self):
        marker = self.root / "safe"
        marker.touch()
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke(extra=("--release-marker", str(marker)), failing_stage="prepare.log")
        self.assertFalse(marker.exists())

    def test_normal_suite_failure_and_postverify_failure_have_cleanup_receipts(self):
        marker = self.root / "safe"
        for stage in ("suite.log", "verify.log"):
            with self.subTest(stage=stage), self.assertRaises(subprocess.CalledProcessError):
                self.invoke(extra=("--release-marker", str(marker)), failing_stage=stage,
                            cleanup_receipt='{"cleanupVerified":true}')
            self.assertTrue(json.loads(marker.read_text())["suite_completed"])

    def test_suite_failure_without_fresh_nonempty_cleanup_receipt_keeps_lease(self):
        marker = self.root / "safe"
        self.evidence.mkdir(exist_ok=True)
        cleanup = self.evidence / "fixture-cleanup.json"
        for receipt in (None, ""):
            with self.subTest(receipt=receipt):
                cleanup.write_text('{"cleanupVerified":true}')
                with self.assertRaises(subprocess.CalledProcessError):
                    self.invoke(extra=("--release-marker", str(marker)), failing_stage="suite.log",
                                cleanup_receipt=receipt)
                self.assertFalse(marker.exists())
                if receipt is None:
                    self.assertFalse(cleanup.exists())

    def test_timeout_interrupt_or_signal_death_never_grants_release(self):
        marker = self.root / "safe"
        for failure in (subprocess.TimeoutExpired(["fixture"], 1),
                        runner.InterruptedExecution("cancelled"),
                        subprocess.CalledProcessError(-signal.SIGTERM, ["fixture"])):
            marker.touch()
            with self.subTest(failure=failure), self.assertRaises(subprocess.SubprocessError):
                self.invoke(extra=("--release-marker", str(marker)), failing_stage="suite.log", failure=failure,
                            cleanup_receipt='{"cleanupVerified":true}')
            self.assertFalse(marker.exists())

    def test_successful_suite_records_receipt(self):
        marker = self.root / "safe"
        self.invoke(extra=("--release-marker", str(marker)))
        receipt = json.loads(marker.read_text())
        self.assertEqual(receipt["device"], "fixture.local")
        self.assertEqual(receipt["source_sha"], SHA)

    def test_os_detection_handles_reported_distro_and_legacy_version(self):
        self.assertTrue(runner.is_os_candidate("metadata", {"os": "WendyOS"}))
        self.assertTrue(runner.is_os_candidate("metadata", {"os": "linux", "osVersion": "WendyOS-0.20"}))
        self.assertFalse(runner.is_os_candidate("", {"os": "wendyos"}))
        self.assertFalse(runner.is_os_candidate("metadata", {"os": "ubuntu", "osVersion": "24.04"}))

    def test_release_version_reaches_preparation_and_final_result(self):
        self.candidate["version"] = "release-candidate"
        row = self.invoke(extra=("--version", "release-candidate"))
        prepare = self.calls[0][0]
        self.assertEqual(prepare[prepare.index("--expected-version") + 1], "release-candidate")
        self.assertEqual(row["candidate_version"], "release-candidate")


class ExecuteTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.log = Path(self.temp.name) / "command.log"
        self.process = Mock(pid=43210, returncode=0)

    def test_child_runs_in_its_own_group_and_preserves_exit_code(self):
        self.process.poll.return_value = 7
        self.process.returncode = 7
        with patch.object(runner.subprocess, "Popen", return_value=self.process) as spawn:
            with self.assertRaises(subprocess.CalledProcessError) as raised:
                runner.execute(["fixture"], self.log, 30)
        self.assertEqual(raised.exception.returncode, 7)
        self.assertTrue(spawn.call_args.kwargs["start_new_session"])

    def test_timeout_terminates_then_kills_entire_group(self):
        self.process.poll.return_value = None
        with patch.object(runner.subprocess, "Popen", return_value=self.process), \
                patch.object(runner.time, "monotonic", side_effect=[0, 11]), \
                patch.object(runner.os, "killpg") as kill:
            with self.assertRaises(subprocess.TimeoutExpired):
                runner.execute(["fixture"], self.log, 10)
        self.assertEqual([c.args for c in kill.call_args_list],
                         [(43210, signal.SIGTERM), (43210, signal.SIGKILL)])
        self.assertEqual([c.kwargs["timeout"] for c in self.process.wait.call_args_list], [20, 10])

    def test_signal_is_forwarded_and_cannot_look_like_normal_test_failure(self):
        handlers = {}

        def install(sig, handler):
            handlers[sig] = handler
            return signal.SIG_DFL

        def wait(**_kwargs):
            if callable(handlers[signal.SIGTERM]):
                handlers[signal.SIGTERM](signal.SIGTERM, None)
            self.process.poll.return_value = -signal.SIGTERM
            self.process.returncode = -signal.SIGTERM

        self.process.poll.return_value = None
        self.process.wait.side_effect = wait
        with patch.object(runner.subprocess, "Popen", return_value=self.process), \
                patch.object(runner.signal, "signal", side_effect=install), \
                patch.object(runner.os, "killpg") as kill:
            with self.assertRaises(runner.InterruptedExecution):
                runner.execute(["fixture"], self.log, 30)
        self.assertIn((43210, signal.SIGTERM), [c.args for c in kill.call_args_list])
        self.assertIn((43210, signal.SIGKILL), [c.args for c in kill.call_args_list])
        self.assertEqual(handlers[signal.SIGTERM], signal.SIG_DFL)

    def test_signal_killed_child_cannot_leave_its_grandchildren_running(self):
        self.process.poll.return_value = -signal.SIGKILL
        self.process.returncode = -signal.SIGKILL
        with patch.object(runner.subprocess, "Popen", return_value=self.process), \
                patch.object(runner.os, "killpg") as kill:
            with self.assertRaises(subprocess.CalledProcessError):
                runner.execute(["fixture"], self.log, 30)
        self.assertIn((43210, signal.SIGKILL), [c.args for c in kill.call_args_list])


if __name__ == "__main__":
    unittest.main()
