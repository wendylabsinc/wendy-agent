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


if __name__ == "__main__":
    unittest.main()
