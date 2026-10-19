#!/usr/bin/env python3
"""Adversarial checks of the rack runner, without Docker or device access."""
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest


REPO = Path(__file__).resolve().parents[2]
FAKE_CLI = r'''
import json, os, pathlib, subprocess, sys, time

args = sys.argv[1:]
root = pathlib.Path(os.environ["MOCK_ROOT"])
with (root / "calls.jsonl").open("a") as calls:
    calls.write(json.dumps(args) + "\n")
state = root / "state.json"
apps = json.loads(state.read_text()) if state.exists() else []
if args[:2] == ["device", "info"]:
    print(json.dumps({"version": "candidate", "os": "linux", "deviceType": "generic-x86-64"}))
elif args[:3] == ["device", "apps", "list"]:
    if os.environ.get("MOCK_CONTROL_FAIL"):
        raise SystemExit(7)
    if apps and os.environ.get("MOCK_LIST_MALFORMED"):
        print("not JSON")
    else:
        print(json.dumps(apps))
elif args[:3] == ["device", "apps", "remove"]:
    if os.environ.get("MOCK_REMOVE_FAIL"):
        print("remove failed", file=sys.stderr)
        raise SystemExit(9)
    if not os.environ.get("MOCK_REMOVE_NOOP"):
        state.write_text(json.dumps([app for app in apps if app["name"] != args[3]]))
elif args[0] == "run":
    if "ghost" in args:
        print("unknown service ghost", file=sys.stderr)
        raise SystemExit(2)
    fixture = pathlib.Path(args[args.index("--prefix") + 1])
    config = json.loads((fixture / "wendy.json").read_text()) if (fixture / "wendy.json").exists() else {}
    app_id = config.get("appId", fixture.name)
    services = list(config.get("services", {}))
    if fixture.name in ("compose-hello", "compose-images"):
        services = ["greeter", "reporter"]
    if config.get("serviceName"):
        services = [config["serviceName"]]
    app = {"name": app_id, "runningState": os.environ.get("MOCK_STATE", "STOPPED")}
    if services:
        app["services"] = [{"name": name, "runningState": os.environ.get("MOCK_SERVICE_STATE", "STOPPED")}
                           for name in services]
        if os.environ.get("MOCK_MISSING_SERVICE"):
            app["services"].pop()
    elif os.environ.get("MOCK_EXIT_CODE", "0") != "missing":
        app["exitCode"] = int(os.environ.get("MOCK_EXIT_CODE", "0"))
    state.write_text(json.dumps([] if os.environ.get("MOCK_MISSING_APP") else [app]))
    if os.environ.get("MOCK_LONG_LOG"):
        print("FIRST-LINE-MUST-SURVIVE")
        for number in range(100):
            print(f"build log {number}")
    if "MOCK_OUTPUT" in os.environ:
        print(os.environ["MOCK_OUTPUT"])
    elif services and not config.get("serviceName"):
        for name in services:
            print(f"[{name}] {name}: PASS")
    else:
        print("PASS: fixture assertions")
    sys.stdout.flush()
    if os.environ.get("MOCK_HANG_TEST") == fixture.name:
        time.sleep(60)
    if os.environ.get("MOCK_CHILD_HANG"):
        child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
        (root / "child-pid").write_text(str(child.pid))
    raise SystemExit(int(os.environ.get("MOCK_RUN_EXIT", "0")))
else:
    raise AssertionError(args)
'''

FAKE_SOCKET = '''
import errno
import os
SOCK_STREAM = 1
def getaddrinfo(host, port, **kwargs):
    if os.environ.get("MOCK_SOCKET") == "dns":
        raise OSError("name resolution failed")
    return [(2, 1, 6, "", (host, port))]
class socket:
    def __init__(self, *args): pass
    def __enter__(self): return self
    def __exit__(self, *args): pass
    def settimeout(self, value): pass
    def connect(self, address):
        mode = os.environ.get("MOCK_SOCKET", "refused")
        if mode == "refused": raise ConnectionRefusedError()
        if mode == "timeout": raise TimeoutError()
        if mode == "unreachable": raise OSError(errno.ENETUNREACH, "network unreachable")
        if mode == "broken": raise RuntimeError("probe unavailable")
'''


class HarnessTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.harness = self.root / "go/scripts/test-ci.sh"
        self.harness.parent.mkdir(parents=True)
        shutil.copyfile(REPO / "go/scripts/test-ci.sh", self.harness)
        shutil.copytree(REPO / ".github/ci-tests", self.root / ".github/ci-tests")
        self.cli = self.root / "wendy"
        self.cli.write_text(f"#!{sys.executable}\n" + FAKE_CLI)
        self.cli.chmod(0o755)
        mocks = self.root / "mocks"
        mocks.mkdir()
        (mocks / "socket.py").write_text(FAKE_SOCKET)
        self.logs = self.root / "logs"
        self.cleanup_receipt = self.root / "fixture-cleanup.json"
        self.env = dict(os.environ, MOCK_ROOT=str(self.root), PYTHONPATH=str(mocks),
                        WENDY_CI_LOG_DIR=str(self.logs), WENDY_CI_POLL_INTERVAL_SECONDS="0",
                        WENDY_CI_CLEANUP_RECEIPT=str(self.cleanup_receipt))
        self.env.pop("WENDY_BUILDX_BUILDER", None)

    def run_harness(self, *tests, **environment):
        (self.root / "state.json").write_text("[]")
        command = ["bash", str(self.harness), "-w", str(self.cli), "-h", "fixture.local"]
        for test in tests:
            command.extend(["-t", test])
        return subprocess.run(command, env=dict(self.env, **environment), text=True,
                              capture_output=True, timeout=20)

    def assert_passed(self, result):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def assert_failed(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("FAIL", result.stdout)

    def calls(self):
        return [json.loads(line) for line in (self.root / "calls.jsonl").read_text().splitlines()]

    def test_successful_single_container_is_verified_and_removed(self):
        self.assert_passed(self.run_harness("python-hello"))
        self.assertEqual(json.loads((self.root / "state.json").read_text()), [])
        self.assertTrue(json.loads(self.cleanup_receipt.read_text())["cleanupVerified"])
        self.assertTrue(any(call[:3] == ["device", "apps", "remove"] for call in self.calls()))

    def test_multiservice_cli_failure_overrides_pass_text(self):
        for fixture in ("python-multiservice-resources", "python-multiservice-env"):
            with self.subTest(fixture=fixture):
                self.assert_failed(self.run_harness(fixture, MOCK_RUN_EXIT="77"))

    def test_crashing_or_unfinished_app_cannot_pass_from_logs(self):
        for state in ("CRASH_LOOPING", "RUNNING", "UNKNOWN"):
            with self.subTest(state=state):
                self.assert_failed(self.run_harness("python-servicename", MOCK_STATE=state))

    def test_group_requires_all_expected_stopped_services_and_verdicts(self):
        for environment in ({"MOCK_MISSING_SERVICE": "1"}, {"MOCK_SERVICE_STATE": "RUNNING"},
                            {"MOCK_SERVICE_STATE": "CRASH_LOOPING"}, {"MOCK_OUTPUT": "db: PASS"},
                            {"MOCK_OUTPUT": "db: PASS\napi: PASS\n[api] FAIL: late assertion"}):
            with self.subTest(environment=environment):
                self.assert_failed(self.run_harness("python-multiservice-resources", **environment))

    def test_missing_or_bad_device_verdict_fails(self):
        for environment in ({"MOCK_MISSING_APP": "1"}, {"MOCK_LIST_MALFORMED": "1"},
                            {"MOCK_EXIT_CODE": "7"}, {"MOCK_EXIT_CODE": "missing", "MOCK_OUTPUT": "BYPASS"}):
            with self.subTest(environment=environment):
                self.assert_failed(self.run_harness("python-hello", **environment))

    def test_general_multiservice_fixture_starts_both_services(self):
        self.assert_passed(self.run_harness("python-multiservice"))
        runs = [call for call in self.calls() if call[0] == "run" and "ghost" not in call]
        self.assertEqual(len(runs), 2)
        for call in runs:
            self.assertNotIn("--deploy", call)
            self.assertIn("--no-restart", call)
        self.assertIn("--service", runs[1])

    def test_env_cases_run_and_keep_cli_overrides(self):
        self.assert_passed(self.run_harness("python-env", "python-env-flag"))
        runs = [call for call in self.calls() if call[0] == "run"]
        self.assertEqual(len(runs), 2)
        self.assertIn("CI_ENV_LEVEL=debug", runs[1])
        self.assertIn("CI_ENV_ONLY_FLAG=1", runs[1])

    def test_compose_cases_wait_for_each_service(self):
        for fixture in ("compose-hello", "compose-images", "compose-companion"):
            with self.subTest(fixture=fixture):
                self.assert_passed(self.run_harness(fixture))
        for call in self.calls():
            if call[0] == "run":
                self.assertNotIn("--detach", call)
                self.assertIn("--no-restart", call)

    def test_timeout_fails_cleans_up_and_runs_remaining_fixture(self):
        result = self.run_harness("python-hello", "python-env", MOCK_HANG_TEST="python-hello",
                                  WENDY_CI_COMMAND_TIMEOUT_SECONDS="0.2")
        self.assert_failed(result)
        self.assertIn("Timed out after", result.stdout)
        self.assertEqual(len([call for call in self.calls() if call[0] == "run"]), 2)
        self.assertEqual(json.loads((self.root / "state.json").read_text()), [])

    def test_descendant_cannot_keep_output_pipe_open_after_cli_exit(self):
        self.assert_passed(self.run_harness("python-hello", MOCK_CHILD_HANG="1"))

    def test_interruption_fails_and_cleans_registered_app(self):
        command = ["bash", str(self.harness), "-w", str(self.cli), "-h", "fixture.local",
                   "-t", "python-hello"]
        process = subprocess.Popen(command, env=dict(self.env, MOCK_HANG_TEST="python-hello"),
                                   text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   start_new_session=True)
        try:
            deadline = time.monotonic() + 5
            state = self.root / "state.json"
            while not state.exists() and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertTrue(state.exists(), "fixture never started")
            os.killpg(process.pid, signal.SIGTERM)
            stdout, stderr = process.communicate(timeout=10)
            self.assertNotEqual(process.returncode, 0, stdout + stderr)
            self.assertEqual(json.loads(state.read_text()), [], stdout + stderr)
            self.assertFalse(self.cleanup_receipt.exists())
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.communicate()

    def test_cleanup_failure_and_silent_noop_fail_the_suite(self):
        for environment in ({"MOCK_REMOVE_FAIL": "1"}, {"MOCK_REMOVE_NOOP": "1"}):
            with self.subTest(environment=environment):
                self.cleanup_receipt.write_text('{"cleanupVerified":true}')
                self.assert_failed(self.run_harness("python-hello", **environment))
                self.assertFalse(self.cleanup_receipt.exists())

    def test_failed_assertion_can_certify_verified_cleanup(self):
        self.assert_failed(self.run_harness("python-hello", MOCK_RUN_EXIT="7"))
        self.assertEqual(json.loads((self.root / "state.json").read_text()), [])
        self.assertTrue(json.loads(self.cleanup_receipt.read_text())["cleanupVerified"])

    def test_bad_invocation_clears_stale_cleanup_receipt(self):
        self.cleanup_receipt.write_text('{"cleanupVerified":true}')
        result = subprocess.run(["bash", str(self.harness), "--invalid"], env=self.env,
                                text=True, capture_output=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.cleanup_receipt.exists())

    def test_success_and_failure_keep_complete_logs(self):
        for exit_code in ("0", "7"):
            result = self.run_harness("python-hello", MOCK_LONG_LOG="1", MOCK_RUN_EXIT=exit_code)
            if exit_code == "0":
                self.assert_passed(result)
            else:
                self.assert_failed(result)
            logs = "\n".join(path.read_text() for path in self.logs.glob("*.log"))
            self.assertIn("FIRST-LINE-MUST-SURVIVE", logs)
            self.assertIn("build log 99", logs)

    def test_otel_requires_successful_probe_and_reachable_control(self):
        for environment in ({"MOCK_SOCKET": "open"}, {"MOCK_SOCKET": "dns"},
                            {"MOCK_SOCKET": "unreachable"}, {"MOCK_SOCKET": "broken"},
                            {"MOCK_CONTROL_FAIL": "1"}):
            with self.subTest(environment=environment):
                self.assert_failed(self.run_harness("otel-localhost-only", **environment))

    def test_otel_accepts_refusal_or_firewall_timeout_on_reachable_device(self):
        for mode in ("refused", "timeout"):
            with self.subTest(mode=mode):
                self.assert_passed(self.run_harness("otel-localhost-only", MOCK_SOCKET=mode))

    def test_otel_live_control_uses_authenticated_app_rpc(self):
        self.assert_passed(self.run_harness("otel-localhost-only"))
        self.assertEqual(sum(call[:2] == ["device", "info"] for call in self.calls()), 1)
        self.assertEqual(sum(call[:3] == ["device", "apps", "list"] for call in self.calls()), 2)


if __name__ == "__main__":
    unittest.main()
