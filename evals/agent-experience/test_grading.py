import unittest

import grading as g


def mcp(name, **arguments):
    return {"channel": "mcp", "name": name, "arguments": arguments}


def cli(*argv):
    words = [w for w in argv if not w.startswith("-")]
    return {"channel": "cli", "name": " ".join(words), "arguments": list(argv)}


def shell(command):
    return {"channel": "shell", "name": "shell", "arguments": command}


WIFI = {"right": [{"tool": "wifi_connect", "args": {"ssid": "Lab5G"}}],
        "wrong": [{"tool": "container_exec", "args_regex": "nmcli"}]}


class GradeTests(unittest.TestCase):
    def test_wrong_beats_right(self):
        trace = [mcp("wifi_connect", ssid="Lab5G"), mcp("container_exec", command=["nmcli", "dev"])]
        r = g.grade(trace, WIFI, "", True)
        self.assertEqual((r["outcome"], r["rule"], r["entry"]), ("wrong", "wrong[0]", 1))

    def test_required_argument_must_match(self):
        self.assertEqual(g.grade([mcp("wifi_connect", ssid="Other")], WIFI, "", True)["outcome"], "missed")

    def test_cli_prefix_matches_by_word(self):
        m = {"cli": "wifi connect"}
        self.assertTrue(g.matches(m, cli("wifi", "connect", "Lab5G")))
        self.assertFalse(g.matches({"cli": "wifi connect"}, cli("wifi", "connected")))
        self.assertFalse(g.matches({"cli": "os"}, cli("osx")))
        self.assertTrue(g.matches({"cli": "wifi", "args_regex": "Lab5G"}, cli("wifi", "connect", "Lab5G")))

    def test_help_lookups_never_match(self):
        # Reading help is informational: it is neither the job done nor the wrong way to do it.
        expect = {"right": [{"cli": "os update-status"}], "wrong": [{"cli": "os update", "block": True}]}
        for argv in (["os", "update", "--help"], ["os", "update", "-h"], ["--device", "x", "os", "update", "--help"]):
            entry = {"channel": "cli", "name": "os update", "arguments": argv}
            with self.subTest(argv=argv):
                self.assertFalse(g.matches({"cli": "os update"}, entry))
                self.assertEqual(g.grade([entry], expect, "", True)["outcome"], "missed")
        self.assertFalse(g.matches({"cli": "os update-status"}, cli("os", "update-status", "--help")))
        # After "--" the words belong to the device-side command, not to wendy.
        self.assertTrue(g.matches({"cli": "device shell"}, cli("device", "shell", "--", "nmcli", "--help")))
        self.assertTrue(g.matches({"cli": "os update"}, cli("os", "update", "--help=false")))
        self.assertEqual(g.grade([cli("os", "update")], expect, "", True)["outcome"], "wrong")

    def test_same_job_via_mcp_and_cli_is_right_once(self):
        expect = {"right": [{"tool": "run"}, {"cli": "run"}], "wrong": [{"shell": "\\b(docker|ssh)\\b"}]}
        trace = [mcp("run"), cli("run")]
        r = g.grade(trace, expect, "", True)
        self.assertEqual((r["outcome"], r["rule"]), ("right", "right[0]"))
        self.assertEqual(g.grade(trace + [shell("docker ps")], expect, "", True)["outcome"], "wrong")

    def test_asked_only_when_expected_and_completed(self):
        text = "Which drive should I erase?"
        self.assertEqual(g.grade([], {"ask": True}, text, True)["outcome"], "asked")
        self.assertEqual(g.grade([], {"ask": False}, text, True)["outcome"], "missed")
        self.assertEqual(g.grade([], {"ask": True}, text, False)["outcome"], "missed")

    def test_hidden_group_only_for_group_discovering_clients(self):
        e = {"right": [{"tool": "wifi_connect"}]}
        self.assertTrue(g.grade([], e, "", True, group="setup", adapter="claude")["hidden_group"])
        self.assertFalse(g.grade([mcp("wendy_tools", groups=["setup"])], e, "", True, group="setup", adapter="claude")["hidden_group"])
        self.assertFalse(g.grade([mcp("wendy_tools", groups=["all"])], e, "", True, group="setup", adapter="codex")["hidden_group"])
        self.assertFalse(g.grade([], e, "", True, group="setup", adapter="wendy")["hidden_group"])
        self.assertFalse(g.grade([], e, "", True, group="core", adapter="claude")["hidden_group"])

    def test_agents_filter_limits_matcher(self):
        m = {"tool": "run", "agents": ["codex"]}
        self.assertTrue(g.matches(m, mcp("run"), "codex"))
        self.assertFalse(g.matches(m, mcp("run"), "claude"))

    def test_list_argument_contains(self):
        self.assertTrue(g.matches({"tool": "x", "args": {"c": "a"}}, mcp("x", c=["a", "b"])))

    def test_validate_expect_rejects_unknown_keys_and_bad_regex(self):
        g.validate_expect(WIFI)
        for bad in ({"nope": 1}, {"right": [{"tool": "a", "cli": "b"}]}, {"right": [{}]},
                    {"wrong": [{"shell": "("}]}, {"right": [{"tool": "a", "args_regex": "["}]},
                    {"right": [{"tool": "a", "extra": 1}]}):
            with self.assertRaises(ValueError):
                g.validate_expect(bad)

    def test_validate_expect_rejects_silent_typos(self):
        for bad in ({"right": [{"tool": 1}]}, {"right": [{"cli": ["run"]}]}, {"wrong": [{"shell": 5}]},
                    {"right": [{"tool": "a", "agents": "claude"}]}, {"right": [{"tool": "a", "agents": [1]}]},
                    {"right": [{"tool": "a", "block": "yes"}]},
                    {"right": [{"cli": "run", "args": {"a": 1}}]}, {"wrong": [{"shell": "x", "args": {}}]},
                    {"right": [{"tool": "a", "args": ["x"]}]},
                    {"wrong": [{"shell": "x", "args_regex": "y"}]}, {"right": [{"tool": "a", "args_regex": 3}]}):
            with self.assertRaises(ValueError, msg=bad):
                g.validate_expect(bad)
        g.validate_expect({"right": [{"tool": "a", "agents": ["claude"], "block": True}]})

    def test_compose_app_shell_wrong_rules(self):
        import json
        from pathlib import Path
        tasks = json.loads((Path(__file__).parent / "tasks.json").read_text())
        wrong = next(t for t in tasks if t["id"] == "compose-app")["expect"]["wrong"]
        hit = lambda c: any(g.matches(m, shell(c)) for m in wrong)
        for ok in ("cat docker-compose.yml", "docker compose up -d", "grep -r docker ."):
            self.assertFalse(hit(ok), ok)
        for bad in ("ssh root@10.0.0.5 'docker ps'", "cd x && scp a b:", "docker -H tcp://10.0.0.5:2375 ps"):
            self.assertTrue(hit(bad), bad)

    def test_stop_and_start_have_cli_matchers(self):
        import json
        from pathlib import Path
        tasks = {t["id"]: t for t in json.loads((Path(__file__).parent / "tasks.json").read_text())}
        self.assertIn({"cli": "device apps stop"}, tasks["stop-app"]["expect"]["right"])
        self.assertIn({"cli": "device apps start"}, tasks["start-app"]["expect"]["right"])


if __name__ == "__main__":
    unittest.main()
