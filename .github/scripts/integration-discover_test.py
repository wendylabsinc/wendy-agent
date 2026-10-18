#!/usr/bin/env python3
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "discovery", Path(__file__).with_name("integration-discover.py")
)
discovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(discovery)


class DiscoveryTests(unittest.TestCase):
    def test_selects_renamed_devices_and_all_required_families(self):
        devices = [("new-pi.local", "raspberry-pi", 1),
                   ("new-thor.local", "jetson", 2),
                   ("ci-iq8.local", "qualcomm", 2),
                   ("ubuntu.local", "generic-linux-x86", 1)]
        self.assertEqual(discovery.select(devices, True), [d[0] for d in devices])

    def test_required_families_take_priority_over_matrix_limit(self):
        devices = [(f"other-{i}.local", "generic-linux-x86", 1) for i in range(20)]
        devices += [(f"{family}.local", family, 1) for family in discovery.REQUIRED]
        devices += [("containerd2.local", "jetson", 2)]
        selected = discovery.select(devices, True)
        self.assertEqual(len(selected), 20)
        for family in discovery.REQUIRED[:-1]:
            self.assertIn(f"{family}.local", selected)
        self.assertIn("other-0.local", selected)
        self.assertIn("containerd2.local", selected)

    def test_missing_platform_fails_gate_but_allows_discovery_only(self):
        devices = [("thor.local", "jetson", None)]
        with self.assertRaisesRegex(ValueError, "raspberry-pi, qualcomm"):
            discovery.select(devices, True)
        self.assertEqual(discovery.select(devices, False), ["thor.local"])
        with self.assertRaises(ValueError):
            discovery.select([], True)

    def test_both_server_majors_are_required(self):
        devices = [(f"{family}.local", family, 1) for family in discovery.REQUIRED]
        with self.assertRaisesRegex(ValueError, "containerd server major\\(s\\): 2"):
            discovery.select(devices, True)
        devices = [(host, family, None) for host, family, _ in devices]
        with self.assertRaisesRegex(ValueError, "containerd server major\\(s\\): 1, 2"):
            discovery.select(devices, True)

    def test_os_candidate_can_introduce_missing_runtime_major(self):
        devices = [(f"{family}.local", family, 1) for family in discovery.REQUIRED]
        self.assertEqual(discovery.select(devices, True, True), [host for host, _, _ in devices])
        with self.assertRaisesRegex(ValueError, "required platform"):
            discovery.select(devices[:-1], True, True)

    def test_deferred_runtime_still_reserves_existing_majors(self):
        devices = [(f"other-{i}.local", "generic-linux-x86", 1) for i in range(20)]
        devices += [(f"{family}.local", family, 1) for family in discovery.REQUIRED]
        devices += [("containerd2.local", "jetson", 2)]
        selected = discovery.select(devices, True, True)
        self.assertEqual(len(selected), discovery.MAX_DEVICES)
        self.assertIn("containerd2.local", selected)

    def test_runtime_deferral_requires_platform_gate(self):
        with self.assertRaisesRegex(ValueError, "requires --require-platforms"):
            discovery.select([], False, True)
        with patch("sys.argv", ["integration-discover", "--wendy", "wendy", "--defer-runtime-coverage"]), \
                patch.object(discovery, "cli_json") as cli, patch("sys.stderr", new_callable=io.StringIO), \
                self.assertRaises(SystemExit) as error:
            discovery.main()
        self.assertEqual(error.exception.code, 2)
        cli.assert_not_called()

    def test_deferred_gate_records_current_runtime_inventory(self):
        devices = [(f"{family}.local", family, 1) for family in discovery.REQUIRED]
        advertisements = {"lanDevices": [{"hostname": host, "isWendyDevice": True}
                                          for host, _, _ in devices]}
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory) / "inventory.json"
            with patch("sys.argv", ["integration-discover", "--wendy", "wendy", "--require-platforms",
                                    "--defer-runtime-coverage", "--evidence", str(evidence)]), \
                    patch.object(discovery, "cli_json", return_value=advertisements), \
                    patch.object(discovery, "probe", side_effect=lambda _, host, strict:
                                 next(row for row in devices if row[0] == host) if strict else None) as probe, \
                    patch("sys.stdout", new_callable=io.StringIO):
                self.assertEqual(discovery.main(), 0)
            actual = json.loads(evidence.read_text())
        self.assertEqual(set(actual["selected"]), {host for host, _, _ in devices})
        self.assertEqual({row["containerdServerMajor"] for row in actual["devices"]}, {1})
        self.assertTrue(all(call.args[2] for call in probe.call_args_list))

    def test_generic_linux_requires_actual_linux_and_x86_architecture(self):
        self.assertEqual(discovery.platform("", "linux", "amd64"), "generic-linux-x86")
        self.assertEqual(discovery.platform("pc", "linux", "x86_64"), "generic-linux-x86")
        self.assertEqual(discovery.platform("", "ubuntu", "amd64"), "generic-linux-x86")
        self.assertEqual(discovery.platform("", "debian", "amd64"), "generic-linux-x86")
        self.assertIsNone(discovery.platform("generic-x86-64", "wendyos", "amd64"))
        self.assertIsNone(discovery.platform("", "darwin", "amd64"))
        self.assertIsNone(discovery.platform("", "", "amd64"))
        self.assertIsNone(discovery.platform("", "linux", "arm64"))

    def test_strict_selection_excludes_unknown_devices_and_their_runtimes(self):
        supported = [(f"{family}.local", family, 1) for family in discovery.REQUIRED]
        devices = supported + [("macmini.local", None, 2), ("esp.local", None, None)]
        with self.assertRaisesRegex(ValueError, "containerd server major\\(s\\): 2"):
            discovery.select(devices, True)
        self.assertEqual(discovery.select(devices, True, True), [host for host, _, _ in supported])
        self.assertEqual(discovery.select(devices, False), [host for host, _, _ in devices])

    def test_strict_probe_retains_diagnostics_without_running_unsupported_shell(self):
        for info in ({"os": "darwin", "cpuArchitecture": "arm64", "deviceType": "macmini"},
                     {"os": "linux", "cpuArchitecture": "arm64", "deviceType": "other-board"},
                     {"os": "linux", "cpuArchitecture": "arm", "deviceType": "raspberry-pi-3"},
                     {"os": "darwin", "cpuArchitecture": "arm64", "deviceType": "raspberry-pi-5"}):
            with self.subTest(info=info), patch.object(discovery, "cli_json", side_effect=[info, []]), \
                    patch.object(discovery, "runtime_major") as runtime:
                self.assertEqual(discovery.probe("wendy", "other.local", True), ("other.local", None, None))
                runtime.assert_not_called()

    def test_containerd_uses_server_not_client_version(self):
        self.assertEqual(discovery.containerd_server_major(
            "Client:\r\n  Version: v2.1.0\r\n\r\nServer:\r\n  Version: v1.7.28\r\n"), 1)
        self.assertEqual(discovery.containerd_server_major(
            "Client:\n  Version: v1.7.28\nServer:\n  Version: v2.0.5\n"), 2)
        for output in ("ctr github.com/containerd/containerd v2.0.5",
                       "Client:\n Version: 2.0.5\n", "Server:\n Version: 3.0.0\n",
                       "Server:\n Version: 1.7.0\nServer:\n Version: 2.0.0\n"):
            with self.subTest(output=output), self.assertRaises(ValueError):
                discovery.containerd_server_major(output)

    def test_runtime_probe_uses_authenticated_shell_and_agent_socket(self):
        with patch.object(discovery.subprocess, "run", return_value=subprocess.CompletedProcess(
                [], 0, "WENDY_CONTAINERD_BEGIN\nServer:\n Version: 2.0.5\nWENDY_CONTAINERD_END\n", "")) as run:
            self.assertEqual(discovery.runtime_major("wendy", "ubuntu.local"), 2)
        argv = run.call_args.args[0]
        self.assertEqual(argv[:8], ["wendy", "device", "shell", "--device", "ubuntu.local", "--", "sh", "-c"])
        self.assertIn('"${WENDY_CONTAINERD_ADDR:-/run/containerd/containerd.sock}" version', argv[-1])
        self.assertTrue(run.call_args.kwargs["check"])

    def test_runtime_probe_rejects_truncated_shell_even_with_server_version(self):
        with patch.object(discovery.subprocess, "run", return_value=subprocess.CompletedProcess(
                [], 0, "WENDY_CONTAINERD_BEGIN\nServer:\n Version: 2.0.5\n", "")), \
                self.assertRaisesRegex(ValueError, "did not complete"):
            discovery.runtime_major("wendy", "ubuntu.local")

    def test_candidates_validate_and_deduplicate_advertisements(self):
        devices = [{"hostname": host, "isWendyDevice": True} for host in
                   ["New-Pi.local", "new-pi.local", "--help", "a;whoami", "a\nb"]]
        devices.append({"hostname": "not-wendy.local", "isWendyDevice": False})
        self.assertEqual(discovery.candidates({"lanDevices": devices}), ["new-pi.local"])
        self.assertEqual(discovery.candidates({"lanDevices": None}), [])

    def test_platform_names(self):
        for kind, family in [("raspberry-pi-3", "raspberry-pi"),
                             ("raspberry-pi-5", "raspberry-pi"),
                             ("jetson-agx-thor", "jetson"),
                             ("jetson-orin-nano", "jetson"),
                             ("dragonwing-iq-8275", "qualcomm"),
                             ("dragonwing-iq-9075", "qualcomm"),
                             ("arduino-uno-q", "qualcomm"),
                             ("qualcomm-iq8", "qualcomm"), ("x86", None)]:
            self.assertEqual(discovery.platform(kind), family)

    def test_public_info_does_not_prove_authorization(self):
        with patch.object(discovery, "cli_json", side_effect=[
            {"deviceType": "jetson-agx-thor"},
            subprocess.CalledProcessError(1, "wendy"),
        ]) as cli:
            self.assertIsNone(discovery.probe("wendy", "thor.local"))
            self.assertEqual(cli.call_count, 2)
            self.assertEqual(cli.call_args.args,
                             ("wendy", "device", "apps", "list", "--device", "thor.local"))

    def test_probe_uses_live_hardware_type(self):
        with patch.object(discovery, "cli_json", side_effect=[
            {"deviceType": "dragonwing-iq-8275"}, [],
        ]):
            self.assertEqual(discovery.probe("wendy", "renamed.local"),
                             ("renamed.local", "qualcomm", None))

    def test_strict_probe_requires_connected_runtime(self):
        for result in (2, ValueError("no server")):
            with self.subTest(result=result), patch.object(discovery, "cli_json", side_effect=[
                    {"os": "linux", "cpuArchitecture": "amd64"}, []]), \
                    patch.object(discovery, "runtime_major", side_effect=[result]):
                expected = ("ubuntu.local", "generic-linux-x86", 2) if result == 2 else None
                self.assertEqual(discovery.probe("wendy", "ubuntu.local", True), expected)

    def test_timed_out_device_is_skipped(self):
        with patch.object(discovery, "cli_json",
                          side_effect=subprocess.TimeoutExpired("wendy", 45)):
            self.assertIsNone(discovery.probe("wendy", "offline.local"))


if __name__ == "__main__":
    unittest.main()
