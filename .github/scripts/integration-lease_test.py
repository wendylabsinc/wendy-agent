#!/usr/bin/env python3
"""Exercise exclusive leases locally, without any device or cloud calls."""

import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("integration_lease", Path(__file__).with_name("integration-lease.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
OWNER = base64.b64encode(b"repo/actions/runs/123/job/456").decode()


class PersistentLeaseTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.lease_root = self.root / "leases"
        self.script = module.LEASE_SCRIPT.replace("root=/var/lib/wendy-ci-rack", f"root={self.lease_root}")
        self.script = self.script.replace("/usr/lib/wendyos/version.txt", str(self.root / "not-wendyos"))
        self.script = self.script.replace("/etc/wendyos/version.txt", str(self.root / "also-not-wendyos"))

    def remote(self, operation, token="a" * 32, owner=OWNER, script=None):
        return subprocess.run(["/bin/sh", "-c", script or self.script, "lease", operation, token, owner],
                              capture_output=True, text=True, timeout=10)

    def test_atomic_exclusion_across_independent_runners(self):
        tokens = [f"{i:032x}" for i in range(12)]
        with ThreadPoolExecutor(max_workers=12) as pool:
            results = list(pool.map(lambda token: self.remote("acquire", token), tokens))
        self.assertEqual(sum("status=acquired" in r.stdout for r in results), 1)
        self.assertTrue(all(r.returncode == 0 for r in results))
        self.assertEqual(len(list((self.lease_root / "lease").iterdir())), 1)

    def test_lost_acquisition_response_can_retry_same_token(self):
        self.assertIn("status=acquired", self.remote("acquire").stdout)
        self.assertIn("status=acquired", self.remote("acquire").stdout)
        self.assertIn("status=busy", self.remote("acquire", "b" * 32).stdout)

    def test_only_exact_owner_can_release(self):
        self.remote("acquire")
        self.assertIn("status=not-owner", self.remote("release", "b" * 32).stdout)
        self.assertIn("status=not-owner", self.remote("release", owner="wrong").stdout)
        self.assertTrue((self.lease_root / "lease" / ("a" * 32)).is_dir())
        self.assertIn("status=released", self.remote("release").stdout)
        self.assertFalse((self.lease_root / "lease").exists())

    def test_old_owner_cannot_release_replacement_lease(self):
        self.remote("acquire")
        (self.lease_root / "lease").rename(self.lease_root / "inspected-old-lease")
        self.remote("acquire", "b" * 32)
        self.assertIn("status=not-owner", self.remote("release").stdout)
        self.assertTrue((self.lease_root / "lease" / ("b" * 32)).is_dir())

    def test_empty_or_ancient_lease_is_never_stolen(self):
        (self.lease_root / "lease").mkdir(parents=True)
        result = self.remote("acquire")
        self.assertIn("status=busy", result.stdout)
        self.assertEqual(list((self.lease_root / "lease").iterdir()), [])

    def test_wendyos_requires_mounted_persistent_data(self):
        (self.root / "not-wendyos").touch()
        script = self.script.replace("mountpoint -q /data", "false")
        result = self.remote("acquire", script=script)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not mounted", result.stderr)
        self.assertFalse(self.lease_root.exists())


class LeaseWrapperTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.operations = []

    def remote(self, wendy, device, operation, token, owner):
        self.operations.append(operation)
        return {"status": "acquired" if operation == "acquire" else "released",
                "path": "/data/wendy-ci-rack/lease", "token": token, "owner": owner}

    def run_child(self, source, timeout=10, marker=None):
        args = argparse.Namespace(wendy="unused", device="fixture", owner="fixture-run",
                                  wait_seconds=0, command_timeout_seconds=timeout,
                                  release_marker=str(marker) if marker else "",
                                  command=[sys.executable, "-c", source])
        with patch.object(module, "remote", self.remote), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            return module.run_locked(args)

    def test_success_releases(self):
        self.assertEqual(self.run_child("pass"), 0)
        self.assertEqual(self.operations, ["acquire", "release"])

    def test_uncertain_failure_retains(self):
        self.assertEqual(self.run_child("raise SystemExit(7)"), 7)
        self.assertEqual(self.operations, ["acquire"])

    def test_normal_failure_with_child_cleanup_marker_releases(self):
        marker = self.root / "safe"
        source = f"from pathlib import Path; Path({str(marker)!r}).touch(); raise SystemExit(7)"
        self.assertEqual(self.run_child(source, marker=marker), 7)
        self.assertEqual(self.operations, ["acquire", "release"])

    def test_previous_attempt_marker_does_not_release_failed_preparation(self):
        marker = self.root / "safe"
        marker.touch()
        self.assertEqual(self.run_child("raise SystemExit(7)", marker=marker), 7)
        self.assertEqual(self.operations, ["acquire"])
        self.assertFalse(marker.exists())

    def test_signal_exit_retains_even_with_marker(self):
        marker = self.root / "safe"
        source = (f"from pathlib import Path; import os, signal; Path({str(marker)!r}).touch(); "
                  "os.kill(os.getpid(), signal.SIGTERM)")
        self.assertEqual(self.run_child(source, marker=marker), 1)
        self.assertEqual(self.operations, ["acquire"])

    def test_timeout_retains_even_with_marker(self):
        marker = self.root / "safe"
        source = f"from pathlib import Path; import time; Path({str(marker)!r}).touch(); time.sleep(10)"
        with self.assertRaises(module.Interrupted):
            self.run_child(source, timeout=0.05, marker=marker)
        self.assertEqual(self.operations, ["acquire"])

    def test_incomplete_host_shell_verdict_rejected(self):
        with self.assertRaises(ValueError):
            module.parse_verdict("status=acquired")

    def test_busy_lease_wait_is_bounded_and_never_released(self):
        verdict = {"status": "busy", "path": "/data/wendy-ci-rack/lease", "token": "a", "owner": OWNER}
        with patch.object(module, "remote", return_value=verdict) as remote:
            with self.assertRaisesRegex(ValueError, "wait expired"):
                module.acquire("unused", "device", "a", OWNER, 0)
            self.assertEqual(remote.call_count, 1)
            self.assertEqual(remote.call_args.args[2], "acquire")


if __name__ == "__main__":
    unittest.main()
