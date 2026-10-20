import copy
import json
from pathlib import Path
import tempfile
import time
import unittest

import with_without as w
import report_with_without as report


class WithWithoutTests(unittest.TestCase):
    def sample(self, tick):
        return {"schema": "g1-replay-eval.v1", "nonce": "id", "version": "v1",
                "tick": tick, "simulation_time": tick * .001,
                "wall_time": time.time(), "joint_names": [str(i) for i in range(43)],
                "joint_positions": [tick * .001] * 43, "actuators": 43,
                "assets": {"model.mjb": "a", "rollout.npz": "b"}, "error": None}

    def test_grader_rejects_frozen_fabricated_wrong_or_nonfinite_state(self):
        samples = [self.sample(1), self.sample(2)]
        hashes = samples[0]["assets"]
        w.validate_samples(samples, "id", hashes)
        changes = [lambda s: s.update(nonce="other"),
                   lambda s: s.update(wall_time=0),
                   lambda s: s.update(actuators=29),
                   lambda s: s.update(assets={}),
                   lambda s: s.update(error="worker died"),
                   lambda s: s.update(joint_positions=[float("nan")] * 43),
                   lambda s: s.update(tick=1),
                   lambda s: s.update(simulation_time=.001),
                   lambda s: s.update(joint_positions=[.001] * 43)]
        for change in changes:
            bad = copy.deepcopy(samples)
            change(bad[1])
            with self.assertRaises(RuntimeError):
                w.validate_samples(bad, "id", hashes)

    def row(self, arm, success, tokens=100, complete=True):
        return {"task": "deploy", "arm": arm, "success": success,
                "agent": {"seconds": 2}, "metrics": {"usage_complete": complete,
                "tokens": {"input_tokens": tokens, "cached_input_tokens": 80,
                           "output_tokens": 20}}}

    def test_failures_count_and_cache_is_not_double_counted(self):
        rows = [self.row("without", True), self.row("without", False),
                self.row("with", True), self.row("with", True)]
        result = w.summarize(rows)[0]
        self.assertEqual(result["arms"]["without"]["tokens_per_verified_success"], 240)
        self.assertEqual(result["arms"]["with"]["tokens_per_verified_success"], 120)
        self.assertEqual(result["tokens_per_verified_success_reduction_pct"], 50)
        self.assertEqual(result["arms"]["without"]["uncached_plus_output_per_verified_success"], 80)

    def test_missing_usage_prevents_savings_claim(self):
        result = w.summarize([self.row("without", False, complete=False),
                              self.row("with", True)])[0]
        self.assertIsNone(result["tokens_per_verified_success_reduction_pct"])

    def test_baseline_command_audit_rejects_wendy(self):
        events = [{"type": "item.completed", "item": {"type": "command_execution",
                   "command": "/opt/homebrew/bin/wendy run --device docker"}}]
        self.assertFalse(w.audit_commands(events, "without", "/opt/homebrew/bin/wendy")["route_compliant"])
        self.assertTrue(w.audit_commands(events, "with", "/opt/homebrew/bin/wendy")["route_compliant"])

    def test_neutral_config_disables_host_context_and_integrations(self):
        with tempfile.TemporaryDirectory() as directory:
            config = w.child_config(Path(directory))
            self.assertIn("skip_host_skill_discovery = true", config)
            for name in ("plugins", "apps", "memories", "multi_agent"):
                self.assertIn(name + " = false", config)
            self.assertNotIn("mcp_servers", config)

    def test_context_audit_rejects_host_skill_leak_and_model_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "session.jsonl"
            context = {"type": "turn_context", "payload": {
                "model": "gpt-6-astra", "effort": "medium", "approval_policy": "never"}}
            def write(skills):
                message = {"type": "response_item", "payload": {"type": "message",
                    "content": [{"text": "<skills_instructions>\n" + skills}]}}
                path.write_text(json.dumps(context) + "\n" + json.dumps(message) + "\n")
            write("- imagegen: built in")
            self.assertTrue(w.audit_context(path, "without")["isolation_passed"])
            write("- wendy-device-ops: leaked global skill")
            self.assertFalse(w.audit_context(path, "without")["isolation_passed"])
            write("- wendy: supplied\n- wendy-app-lifecycle: supplied")
            self.assertTrue(w.audit_context(path, "with")["isolation_passed"])
            context["payload"]["effort"] = "xhigh"
            write("- wendy: supplied\n- wendy-app-lifecycle: supplied")
            self.assertFalse(w.audit_context(path, "with")["isolation_passed"])

    def test_paired_statistics_exclude_incomplete_pairs_and_keep_pair_direction(self):
        rows = []
        for pair in range(1, 6):
            for arm, tokens in (("without", 180), ("with", 80)):
                row = self.row(arm, True, tokens=tokens)
                row["pair"] = pair
                rows.append(row)
        extra = self.row("without", True, tokens=100000)
        extra["pair"] = 6
        rows.append(extra)
        stats = report.pair_statistics(rows, "deploy")
        self.assertEqual(stats["complete_successful_pairs"], 5)
        self.assertEqual(stats["reduction_pct"], 50)
        self.assertEqual(stats["wendy_fewer_token_pairs"], 5)
        self.assertEqual(stats["paired_sign_flip_two_sided_p"], .0625)


if __name__ == "__main__":
    unittest.main()
