#!/usr/bin/env python3
"""Nightly ordering and conservative lease release without contacting devices."""
import argparse
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("nightly", Path(__file__).with_name("nightly-device-check.py"))
nightly = importlib.util.module_from_spec(spec)
spec.loader.exec_module(nightly)


class NightlyTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.args = argparse.Namespace(wendy="/fake/wendy", device="rack.local", evidence=Path(self.directory.name))
        self.marker = self.args.evidence / "lease-releasable"
        self.discovery = patch.object(nightly.runner, "discovery_module").start()
        self.addCleanup(patch.stopall)
        self.discovery.return_value.cli_json.return_value = {"os": "wendyos"}

    def test_os_then_nightly_agent_then_apps_then_post_suite_hash(self):
        with patch.object(nightly.runner, "execute") as execute:
            nightly.check(self.args)
        commands = [call.args[0] for call in execute.call_args_list]
        self.assertEqual(commands[0][1:], ["os", "update", "--nightly", "--device", "rack.local"])
        self.assertIn("--nightly", commands[1])
        self.assertTrue(commands[2][1].endswith("/test-ci.sh"))
        self.assertTrue(commands[3][1].endswith("/test-templates.sh"))
        self.assertIn("--verify-only", commands[4])
        self.assertTrue(self.marker.is_file())

    def test_failed_preparation_clears_stale_marker_and_never_tests(self):
        for failed_step in (0, 1):
            self.marker.write_text("old success")
            failure = subprocess.CalledProcessError(1, "prepare")
            with patch.object(nightly.runner, "execute", side_effect=[None] * failed_step + [failure]) as execute:
                with self.assertRaises(subprocess.CalledProcessError):
                    nightly.check(self.args)
            self.assertEqual(execute.call_count, failed_step + 1)
            self.assertFalse(self.marker.exists())

    def test_failure_without_verified_cleanup_retains_lease(self):
        for step in (2, 3):
            for failure, releasable in ((subprocess.CalledProcessError(1, "test"), False),
                                        (subprocess.CalledProcessError(-15, "test"), False),
                                        (subprocess.TimeoutExpired("test", 10), False)):
                with self.subTest(step=step, failure=failure):
                    with patch.object(nightly.runner, "execute", side_effect=[None] * step + [failure]):
                        with self.assertRaises(subprocess.SubprocessError):
                            nightly.check(self.args)
                    self.assertEqual(self.marker.exists(), releasable)

    def test_verified_cleanup_allows_release_after_normal_fixture_failure(self):
        def execute(command, *_):
            if command[1].endswith("/test-ci.sh"):
                (self.args.evidence / "fixture-cleanup.json").write_text("cleanup verified\n")
                raise subprocess.CalledProcessError(1, command)
        with patch.object(nightly.runner, "execute", side_effect=execute):
            with self.assertRaises(subprocess.CalledProcessError):
                nightly.check(self.args)
        self.assertTrue(self.marker.is_file())

    def test_vendor_linux_still_installs_nightly_agent_without_os_update(self):
        self.discovery.return_value.cli_json.return_value = {"os": "ubuntu"}
        with patch.object(nightly.runner, "execute") as execute:
            nightly.check(self.args)
        commands = [call.args[0] for call in execute.call_args_list]
        self.assertEqual(len(commands), 4)
        self.assertIn("--nightly", commands[0])
        self.assertFalse(any(command[1:3] == ["os", "update"] for command in commands))

    def test_agent_drift_fails_after_suite(self):
        with patch.object(nightly.runner, "execute", side_effect=[None] * 4 + [subprocess.CalledProcessError(1, "verify")]):
            with self.assertRaises(subprocess.CalledProcessError):
                nightly.check(self.args)
        self.assertTrue(self.marker.is_file())


if __name__ == "__main__":
    unittest.main()
