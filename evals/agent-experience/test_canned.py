import json
import tempfile
import unittest
from pathlib import Path

import canned
import capture

FAKE = str(Path(__file__).resolve().parent / "fake_mcp_server.py")


def text(s):
    return {"content": [{"type": "text", "text": s}], "isError": False}


def write(root, set_name, tool, replies):
    d = Path(root) / set_name
    d.mkdir(parents=True, exist_ok=True)
    (d / f"{tool}.json").write_text(json.dumps({"tool": tool, "captured_with": {}, "replies": replies}))


class CannedTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.addCleanup(self.tmp.cleanup)

    def test_reply_steps_through_sequence_and_repeats_last(self):
        write(self.root, "s", "t", [text("a"), text("b")])
        store = canned.CannedStore(self.root, "s", {})
        got = [store.reply("t", {})[0]["content"][0]["text"] for _ in range(3)]
        self.assertEqual(got, ["a", "b", "b"])
        self.assertTrue(store.reply("t", {})[1])

    def test_placeholders_fill_from_args_and_earlier_reply(self):
        write(self.root, "s", "os_install_start",
              [{"content": [{"type": "text", "text": "x"}], "structuredContent": {"job_id": "J1"}}])
        write(self.root, "s", "os_install_status",
              [text('{"job_id": "{{reply.os_install_start.job_id}}", "ssid": "{{args.ssid}}"}')])
        store = canned.CannedStore(self.root, "s", {})
        store.reply("os_install_start", {})
        result, _ = store.reply("os_install_status", {"ssid": "Lab5G"})
        self.assertIn("J1", result["content"][0]["text"])
        self.assertIn("Lab5G", result["content"][0]["text"])

    def test_missing_argument_renders_empty(self):
        self.assertEqual(canned.render("a{{args.nope}}b", {"args": {}}), "ab")

    def test_unknown_placeholder_rejected_at_load(self):
        write(self.root, "s", "t", [text("{{bogus}}")])
        with self.assertRaises(ValueError):
            canned.load_capture(self.root, "s", "t")

    def test_uncaptured_tool_returns_flagged_fallback(self):
        store = canned.CannedStore(self.root, "s", {})
        result, captured = store.reply("nothing", {})
        self.assertEqual(result, canned.FALLBACK_RESULT)
        self.assertFalse(captured)

    def test_cli_capture_longest_prefix(self):
        d = self.root / "s" / "cli"
        d.mkdir(parents=True)
        (d / "os-install.json").write_text(json.dumps({"argv_prefix": ["os", "install"], "stdout": "short"}))
        (d / "os-install-start.json").write_text(json.dumps({"argv_prefix": ["os", "install", "start"], "stdout": "long"}))
        got = canned.load_cli_capture(self.root, "s", ["os", "install", "start", "--x"])
        self.assertEqual(got["stdout"], "long")
        self.assertIsNone(canned.load_cli_capture(self.root, "s", ["device"]))

    def test_scrub_removes_macs_uuids_selectors_and_serial_keys(self):
        value = {"serial_number": "ABC", "org_id": "o", "name": "ok",
                 "note": "mac aa:bb:cc:dd:ee:ff id 123e4567-e89b-12d3-a456-426614174000 at cloud://h:1/org/x/asset/y",
                 "content": [{"type": "text", "text": json.dumps({"hostname": "h", "k": "v"})}]}
        out = canned.scrub(value)
        self.assertEqual(out["serial_number"], "scrubbed")
        self.assertEqual(out["org_id"], "scrubbed")
        self.assertEqual(out["name"], "ok")
        self.assertIn("02:00:00:00:00:00", out["note"])
        self.assertIn("00000000-0000-0000-0000-000000000000", out["note"])
        self.assertIn("{{device}}", out["note"])
        self.assertNotIn("cloud://", out["note"])
        self.assertEqual(json.loads(out["content"][0]["text"]), {"hostname": "scrubbed", "k": "v"})

    def test_capture_refuses_mutating_tool_without_yes(self):
        base = ["--wendy", FAKE, "--device", "vm:test", "--set", "s", "--tool", "write_thing",
                "--canned-dir", str(self.root)]
        self.assertEqual(capture.main(base), 2)
        self.assertFalse((self.root / "s" / "write_thing.json").exists())
        self.assertEqual(capture.main(base + ["--yes"]), 0)
        data = json.loads((self.root / "s" / "write_thing.json").read_text())
        self.assertEqual(len(data["replies"]), 1)
        self.assertEqual(data["captured_with"]["device_kind"], "simulator")
        self.assertEqual(data["captured_with"]["wendy_version"], "fake-wendy 0.0.0")

    def _fake_cli(self, out):
        marker = self.root / "marker"
        script = self.root / "fakewendy"
        script.write_text("#!/bin/sh\necho \"$@\" >> %s\n[ \"$1\" = --version ] && { echo fake 1; exit 0; }\n"
                          "echo '%s'\n" % (marker, out))
        script.chmod(0o755)
        return script, marker

    def _cli_args(self, script, *extra):
        return ["--wendy", str(script), "--device", "vm:t", "--set", "s", "--canned-dir", str(self.root),
                *extra]

    def test_cli_captures_keyed_on_full_subcommand_path(self):
        script, _ = self._fake_cli("{}")
        for words in (["device", "apps", "stop"], ["device", "apps", "list"]):
            self.assertEqual(capture.main(self._cli_args(script, "--yes", "--cli", "--json", *words)), 0)
        self.assertTrue((self.root / "s/cli/device-apps-stop.json").exists())
        self.assertTrue((self.root / "s/cli/device-apps-list.json").exists())
        got = canned.load_cli_capture(self.root, "s", ["device", "apps", "stop"])
        self.assertEqual(got["argv_prefix"], ["device", "apps", "stop"])

    def test_prefix_words_shortens_prefix(self):
        script, _ = self._fake_cli("{}")
        capture.main(self._cli_args(script, "--yes", "--prefix-words", "2", "--cli", "wifi", "connect", "Lab5G"))
        self.assertEqual(json.loads((self.root / "s/cli/wifi-connect.json").read_text())["argv_prefix"],
                         ["wifi", "connect"])

    def test_cli_requires_yes_and_passes_device(self):
        script, marker = self._fake_cli("{}")
        self.assertEqual(capture.main(self._cli_args(script, "--cli", "device", "list")), 2)
        self.assertNotIn("--device", marker.read_text())  # only --version ran
        capture.main(self._cli_args(script, "--yes", "--cli", "device", "list"))
        self.assertIn("--device vm:t device list", marker.read_text())

    def test_unresolvable_wendy_exits_2_without_running_anything(self):
        script, marker = self._fake_cli("{}")
        args = ["--wendy", str(self.root / "missing"), "--device", "vm:t", "--set", "s",
                "--canned-dir", str(self.root), "--yes", "--tool", "read_thing"]
        self.assertEqual(capture.main(args), 2)
        self.assertFalse(marker.exists())

    def test_cli_json_output_scrubbed_and_valid(self):
        out = json.dumps({"serial_number": "S1", "org_id": "o", "sel": "cloud://h:1/org/x/asset/y", "k": "v"})
        script, _ = self._fake_cli(out)
        capture.main(self._cli_args(script, "--yes", "--cli", "device", "list"))
        stdout = json.loads((self.root / "s/cli/device-list.json").read_text())["stdout"]
        parsed = json.loads(stdout)
        self.assertEqual(parsed["serial_number"], "scrubbed")
        self.assertEqual(parsed["org_id"], "scrubbed")
        self.assertEqual(parsed["sel"], "{{device}}")
        self.assertEqual(parsed["k"], "v")

    def test_placeholder_values_are_json_escaped(self):
        write(self.root, "s", "t", [text('{"ssid": "{{args.ssid}}"}')])
        store = canned.CannedStore(self.root, "s", {})
        result, _ = store.reply("t", {"ssid": 'a"b\\c'})
        self.assertEqual(json.loads(result["content"][0]["text"])["ssid"], 'a"b\\c')

    def test_tool_name_cannot_escape_canned_dir(self):
        outside = self.root / "x.json"
        outside.write_text(json.dumps({"tool": "x", "replies": [text("leak")]}))
        (self.root / "s").mkdir()
        self.assertIsNone(canned.load_capture(self.root, "s", "../x"))
        self.assertEqual(canned.CannedStore(self.root, "s", {}).reply("../x", {}),
                         (canned.FALLBACK_RESULT, False))


if __name__ == "__main__":
    unittest.main()
