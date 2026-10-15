#!/usr/bin/env python3
"""Regression checks for the public plugin export boundary and review metadata."""

import copy
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
import zipfile


SCRIPT = Path(__file__).with_name("package-chatgpt-plugin.py")
SPEC = importlib.util.spec_from_file_location("package_chatgpt_plugin", SCRIPT)
package = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(package)


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "plugins/wendy-chatgpt"
        shutil.copytree(package.SOURCE, self.source)
        self.manifest = json.loads((self.source / "plugin.json").read_text())
        self.mcp = json.loads((self.source / "mcp.json").read_text())

    def save(self):
        (self.source / "plugin.json").write_text(json.dumps(self.manifest))
        (self.source / "mcp.json").write_text(json.dumps(self.mcp))

    def test_archive_retains_metadata_and_excludes_private_files(self):
        (self.source / "gateway.json").write_text('{"credential":"do-not-export"}')
        self.save()
        manifest, mcp, files, gaps = package.validate_package(self.source)
        output = self.root / "plugin.zip"
        package.export_package(self.source, output, manifest, mcp, files)
        with zipfile.ZipFile(output) as archive:
            self.assertNotIn("wendy-robots/gateway.json", archive.namelist())
            self.assertNotIn("wendy-robots/SUBMISSION.md", archive.namelist())
            exported = json.loads(archive.read("wendy-robots/plugin.json"))
            extension = exported["extensions"]["com.openai"]
            self.assertEqual(extension["publication"]["countries"], [])
            self.assertIs(extension["review"]["commerce"], False)
            self.assertEqual(len(extension["review"]["test_cases"]["positive"]), 5)
            self.assertEqual(len(extension["review"]["test_cases"]["negative"]), 3)
            self.assertEqual(json.loads(archive.read("wendy-robots/mcp.json")), mcp)

    def test_no_recording_is_an_explicit_gap(self):
        extension = copy.deepcopy(self.manifest["extensions"]["com.openai"])
        extension["review"].pop("demo_recording_url", None)
        self.assertTrue(any("demo_recording_url" in gap for gap in package.submission_gaps(extension)))

    def test_strict_export_fails_before_writing_incomplete_archive(self):
        self.manifest["extensions"]["com.openai"]["review"].pop("demo_recording_url", None)
        self.save()
        scripts = self.root / "scripts"
        scripts.mkdir()
        shutil.copy2(SCRIPT, scripts / SCRIPT.name)
        output = self.root / "strict.zip"
        result = subprocess.run([sys.executable, "-O", str(scripts / SCRIPT.name), "--output", str(output), "--require-submission-metadata"], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("demo_recording_url", result.stderr)
        self.assertFalse(output.exists())

    def test_rejects_app_bindings_even_when_empty(self):
        self.manifest["extensions"]["com.openai"]["apps"] = {}
        self.save()
        with self.assertRaisesRegex(ValueError, "cannot declare apps"):
            package.validate_package(self.source)

    def test_rejects_bearer_header(self):
        self.mcp["mcpServers"]["wendy-robots"]["headers"] = {"Authorization": "Bearer do-not-export"}
        self.save()
        with self.assertRaisesRegex(ValueError, "authentication headers"):
            package.validate_package(self.source)

    def test_rejects_relay_auth_for_standalone_package(self):
        auth = self.mcp["mcpServers"]["wendy-robots"]["extensions"]["com.openai"]["auth"]
        auth["baseScopes"] = ["mcp:relay"]
        self.save()
        with self.assertRaisesRegex(ValueError, "standalone gateway scopes"):
            package.validate_package(self.source)

    def test_rejects_array_in_expected_behavior(self):
        self.manifest["extensions"]["com.openai"]["review"]["test_cases"]["positive"][0]["expected_behavior"] = ["not a string"]
        self.save()
        with self.assertRaisesRegex(ValueError, "expected_behavior"):
            package.validate_package(self.source)

    def test_rejects_symlinked_asset(self):
        (self.source / "assets/private.txt").symlink_to(self.source / "plugin.json")
        with self.assertRaisesRegex(ValueError, "Symlinks"):
            package.validate_package(self.source)

    def test_rejects_credential_url(self):
        self.manifest["extensions"]["com.openai"]["interface"]["supportURL"] = "https://user:secret@wendy.dev/support"
        self.save()
        with self.assertRaisesRegex(ValueError, "supportURL"):
            package.validate_package(self.source)


if __name__ == "__main__":
    unittest.main()
