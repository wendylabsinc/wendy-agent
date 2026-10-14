import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import build
import versioning

SHA = "a" * 40


class VersionTests(unittest.TestCase):
    def derive(self, channel, **kwargs):
        return versioning.derive("0.1.0", channel, source_sha=SHA, **kwargs)

    def test_all_channels_and_python_mapping(self):
        self.assertEqual(self.derive("stable")["version"], "0.1.0")
        self.assertEqual(self.derive("dev")["pythonVersion"], "0.1.0.dev0")
        nightly = self.derive("nightly", run_id=123, attempt=2)
        self.assertEqual(nightly["version"], "0.1.0-nightly.123.2")
        self.assertEqual(nightly["pythonVersion"], "0.1.0.dev123002")
        preview = self.derive("pr", branch="kb/My Feature", pr=42, run_id=124)
        self.assertEqual(preview["version"], "0.1.0-pr.42-kb-my-feature.124.1")
        self.assertEqual(preview["npmTag"], "pr-42")
        manual = self.derive("pr", branch="testing", run_id=125)
        self.assertEqual(manual["version"], "0.1.0-pr.manual-testing.125.1")
        self.assertEqual(manual["npmTag"], "preview-testing")

    def test_attempts_and_runs_have_distinct_versions(self):
        values = [self.derive("nightly", run_id=run, attempt=attempt)
                  for run, attempt in ((123, 1), (123, 2), (124, 1))]
        for key in ("version", "pythonVersion"):
            self.assertEqual(len({value[key] for value in values}), 3)

    def test_slug_is_bounded_and_not_a_shell_expression(self):
        self.assertEqual(versioning.slug("\n$(echo PWNED)/../ABC"), "echo-pwned-abc")
        self.assertLessEqual(len(versioning.slug("a" * 100)), 40)
        self.assertEqual(versioning.slug("/"), "branch")

    def test_invalid_context_rejected(self):
        for channel, options in (("beta", {}), ("nightly", {}), ("pr", {"run_id": -1}),
                                 ("nightly", {"run_id": 1, "attempt": 1000}),
                                 ("pr", {"run_id": 1, "pr": 0})):
            with self.subTest(channel=channel, options=options), self.assertRaises(ValueError):
                self.derive(channel, **options)
        with self.assertRaises(ValueError):
            versioning.derive("01.0.0", "stable", source_sha=SHA)
        with self.assertRaises(ValueError):
            versioning.derive("0.1.0", "stable", source_sha="branch")

    def test_forged_effective_versions_rejected(self):
        info = self.derive("pr", branch="feature", pr=42, run_id=123)
        for key, value in (("version", "0.1.0"), ("npmTag", "latest"), ("pythonVersion", "0.1.0")):
            with self.subTest(key=key), self.assertRaises(ValueError):
                versioning.validate({**info, key: value})

    def test_github_pr_main_tag_and_manual_plans(self):
        event = {"number": 42, "pull_request": {"head": {"sha": SHA, "ref": "feature"}}}
        with patch.object(versioning, "base_version", return_value="0.1.0"), \
                patch.object(versioning.subprocess, "check_output", return_value=SHA + "\n"):
            self.assertEqual(versioning.github_plan(event, "pull_request", "branch", "42/merge", 123, 1)["pr"], 42)
            self.assertEqual(versioning.github_plan({}, "push", "branch", "main", 123, 1)["channel"], "nightly")
            self.assertEqual(versioning.github_plan({}, "push", "tag", "v0.1.0", 123, 1)["channel"], "stable")
            self.assertIsNone(versioning.github_plan({}, "workflow_dispatch", "branch", "feature", 123, 1)["pr"])
            for tag in ("v0.2.0", "v0.1.0-pr.42-feature.123.1"):
                with self.assertRaises(ValueError):
                    versioning.github_plan({}, "push", "tag", tag, 123, 1)

    def test_identity_requires_matching_source_and_base(self):
        info = self.derive("nightly", run_id=123)
        with patch.dict(os.environ, {"CORE_BUILD_IDENTITY": json.dumps(info)}), \
                patch.object(versioning, "base_version", return_value="0.1.0"), \
                patch.object(versioning.subprocess, "check_output", return_value="b" * 40):
            with self.assertRaises(ValueError):
                versioning.identity()

    def test_npm_staging_does_not_modify_source_metadata(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            source = directory / "js"
            source.mkdir()
            package = {"name": "@wendylabs/core", "version": "0.1.0", "files": ["src"]}
            original = json.dumps(package)
            (source / "package.json").write_text(original)
            (source / "src").mkdir()
            (source / "src/index.mjs").write_text("export {}")

            def inspect(*args, cwd):
                self.assertEqual(json.loads((cwd / "package.json").read_text())["version"], "0.1.0-dev")
                self.assertIn("--ignore-scripts", args)

            with patch.object(build, "JS", source), patch.object(build, "stage_licenses"), \
                    patch.object(build, "version", return_value="0.1.0-dev"), patch.object(build, "run", side_effect=inspect):
                build.pack_npm(directory / "out")
            self.assertEqual((source / "package.json").read_text(), original)


if __name__ == "__main__":
    unittest.main()
