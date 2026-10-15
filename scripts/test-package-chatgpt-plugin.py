#!/usr/bin/env python3
"""Regression checks for the public plugin export boundary and review metadata."""

import copy
import importlib.util
import json
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest
import zipfile
import zlib


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

    def test_rejects_hidden_and_generated_component_files(self):
        for name in ("skills/wendy/.env", "assets/.DS_Store", "skills/wendy/.cache/data.json",
                     "skills/wendy/__pycache__/skill.pyc", "assets/node_modules/package/index.js",
                     "assets/__MACOSX/icon.png", "skills/wendy/notes.md~", "assets/icon.png.bak"):
            with self.subTest(name=name):
                source = self.root / "case"
                shutil.copytree(self.source, source)
                try:
                    path = source / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text("do-not-export")
                    with self.assertRaisesRegex(ValueError, "Hidden or generated"):
                        package.validate_package(source)
                finally:
                    shutil.rmtree(source)

    def test_rejects_truncated_png(self):
        icon = self.source / "assets/wendy-icon.png"
        original = icon.read_bytes()
        for data in (original[:24], original[:-1], original[:-12]):
            with self.subTest(length=len(data)):
                icon.write_bytes(data)
                with self.assertRaisesRegex(ValueError, "PNG"):
                    package.validate_package(self.source)

    def test_rejects_png_crc_corruption(self):
        icon = self.source / "assets/wendy-icon.png"
        data = bytearray(icon.read_bytes())
        data[29] ^= 1  # Corrupt IHDR's CRC without changing its dimensions.
        icon.write_bytes(data)
        with self.assertRaisesRegex(ValueError, "PNG chunk CRC"):
            package.validate_package(self.source)

    def test_rejects_png_without_image_data_and_with_trailing_data(self):
        icon = self.source / "assets/wendy-icon.png"
        original = icon.read_bytes()
        cases = ((original[:33] + original[-12:], "missing PNG image data"),
                 (original + b"unrelated", "end with an empty PNG IEND"))
        for data, error in cases:
            with self.subTest(error=error):
                icon.write_bytes(data)
                with self.assertRaisesRegex(ValueError, error):
                    package.validate_package(self.source)

    def test_accepts_png_with_consecutive_image_chunks(self):
        def chunk(kind, payload):
            return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", zlib.crc32(kind + payload))

        # A real 256-square RGB image with compressed scanlines, split over IDATs.
        compressed = zlib.compress((b"\x00" + b"\xff\x00\x00" * 256) * 256)
        midpoint = len(compressed) // 2
        data = (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 256, 256, 8, 2, 0, 0, 0))
                + chunk(b"IDAT", compressed[:midpoint]) + chunk(b"IDAT", compressed[midpoint:]) + chunk(b"IEND", b""))
        (self.source / "assets/wendy-icon.png").write_bytes(data)
        package.validate_package(self.source)

    def test_rejects_credential_url(self):
        self.manifest["extensions"]["com.openai"]["interface"]["supportURL"] = "https://user:secret@wendy.dev/support"
        self.save()
        with self.assertRaisesRegex(ValueError, "supportURL"):
            package.validate_package(self.source)


if __name__ == "__main__":
    unittest.main()
