import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import build
from check_release import require_compatible


def api(version, **functions):
    return {"version": version, "c": {"functions": [{"name": name, "signature": signature} for name, signature in functions.items()]}}


class ReleaseTests(unittest.TestCase):
    def test_versions_match_and_wrong_tags_fail(self):
        value = build.version()
        self.assertEqual(build.check_version("v" + value), value)
        with self.assertRaises(ValueError):
            build.check_version("v-not-semver")

    def test_removal_requires_major_bump_even_before_one(self):
        with self.assertRaises(ValueError):
            require_compatible(api("0.1.0", WendyHelloWorld="int32_t (void)"), api("0.2.0"))
        require_compatible(api("0.1.0", WendyHelloWorld="int32_t (void)"), api("1.0.0"))

    def test_addition_is_not_a_patch(self):
        with self.assertRaises(ValueError):
            require_compatible(api("1.0.0"), api("1.0.1", WendyHelloWorld="int32_t (void)"))
        require_compatible(api("1.0.0"), api("1.1.0", WendyHelloWorld="int32_t (void)"))

    def test_unchanged_operation_allows_patch(self):
        require_compatible(api("1.0.0", WendyHelloWorld="int32_t (void)"), api("1.0.1", WendyHelloWorld="int32_t (void)"))

    def test_assembly_rejects_wrong_artifact_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp) / "native-linux-x64"
            directory.mkdir()
            (directory / "manifest.json").write_text(json.dumps({"version": "9.0.0", "target": "linux-x64"}))
            # Do not remove a developer's staged artifacts while testing failure.
            with patch.object(build.shutil, "rmtree"), self.assertRaises(ValueError):
                build.assemble(Path(temp))


if __name__ == "__main__":
    unittest.main()
