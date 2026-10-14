import base64
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import publication
from versioning import derive

SHA = "a" * 40
REPOSITORY = "wendylabsinc/wendy-core"


class PublicationTests(unittest.TestCase):
    def setUp(self):
        # Integrity/context tests are independent of package-format validation,
        # which has real archive fixtures in PackageVersionTests below.
        guard = patch.object(publication, "verify_versions")
        guard.start()
        self.addCleanup(guard.stop)

    def run_info(self, event="push", branch="main"):
        return {"id": 123, "run_attempt": 2, "event": event, "head_branch": branch,
                "head_sha": SHA, "head_repository": {"full_name": REPOSITORY},
                "conclusion": "success", "path": ".github/workflows/ci.yml",
                "pull_requests": [{"number": 42}]}

    def api(self, run, *, fork=False, current_sha=SHA):
        def response(path):
            if "/actions/runs/" in path:
                return run
            if "/contents/VERSION" in path:
                return {"content": base64.b64encode(b"0.1.0\n").decode()}
            if "/pulls/" in path:
                return {"head": {"repo": {"full_name": "other/fork" if fork else REPOSITORY},
                                 "sha": current_sha, "ref": "feature"}}
            if "/git/ref/heads/" in path:
                return {"object": {"sha": current_sha}}
            return {"sha": current_sha}
        return response

    def test_authenticated_redirects_do_not_leak_credentials(self):
        from urllib.request import Request
        request = Request("https://maven.pkg.github.com/source", headers={"Authorization": "Basic secret"})
        handler = publication.SafeRedirect()
        same = handler.redirect_request(request, None, 302, "Found", {}, "https://maven.pkg.github.com/other")
        self.assertEqual(same.get_header("Authorization"), "Basic secret")
        other = handler.redirect_request(request, None, 302, "Found", {}, "https://artifacts.example/download")
        self.assertIsNone(other.get_header("Authorization"))
        with self.assertRaises(ValueError):
            handler.redirect_request(request, None, 302, "Found", {}, "http://artifacts.example/download")

    def test_rederive_preview_from_trusted_run_metadata(self):
        run = self.run_info("pull_request", "feature")
        with patch.object(publication, "api", side_effect=self.api(run)):
            info = publication.context(REPOSITORY, 123, 1)
        self.assertEqual(info, derive("0.1.0", "pr", source_sha=SHA, branch="feature", pr=42, run_id=123))

    def test_retries_keep_originating_attempt(self):
        run = self.run_info()
        with patch.object(publication, "api", side_effect=self.api(run)):
            self.assertEqual(publication.context(REPOSITORY, 123, 1)["attempt"], 1)
            with self.assertRaises(ValueError):
                publication.context(REPOSITORY, 123, 3)

    def test_forks_and_stale_previews_fail_closed(self):
        run = self.run_info("pull_request", "feature")
        for options in ({"fork": True}, {"current_sha": "b" * 40}):
            with self.subTest(options=options), patch.object(publication, "api", side_effect=self.api(run, **options)), \
                    self.assertRaises(ValueError):
                publication.context(REPOSITORY, 123)
        with patch.object(publication, "api", side_effect=self.api(self.run_info(), current_sha="b" * 40)), \
                self.assertRaises(ValueError):
            publication.context(REPOSITORY, 123)

    def test_manual_branch_need_not_have_pr_or_special_name(self):
        run = self.run_info("workflow_dispatch", "very-good")
        with patch.object(publication, "api", side_effect=self.api(run)):
            self.assertIsNone(publication.context(REPOSITORY, 123)["pr"])

    def seal(self, directory, info):
        (directory / "release.json").write_text(json.dumps({"identity": info, "version": info["version"]}))
        (directory / "sample.tgz").write_bytes(b"packaged bytes")
        entries = [f"{hashlib.sha256(file.read_bytes()).hexdigest()}  {file.name}\n" for file in sorted(directory.iterdir())]
        (directory / "SHA256SUMS").write_text("".join(entries))

    def test_integrity_and_run_identity_are_both_required(self):
        info = derive("0.1.0", "nightly", source_sha=SHA, run_id=123)
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            self.seal(directory, info)
            publication.verify(directory, info)
            with self.assertRaises(ValueError):
                publication.verify(directory, derive("0.1.0", "stable", source_sha=SHA))
            (directory / "sample.tgz").write_bytes(b"modified")
            with self.assertRaises(ValueError):
                publication.verify(directory, info)

    def test_unsealed_files_and_path_traversal_rejected(self):
        info = derive("0.1.0", "nightly", source_sha=SHA, run_id=123)
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            self.seal(directory, info)
            (directory / "unexpected").write_text("extra")
            with self.assertRaises(ValueError):
                publication.verify(directory, info)
            (directory / "unexpected").unlink()
            (directory / "SHA256SUMS").write_text("0" * 64 + "  ../outside\n")
            with self.assertRaises(ValueError):
                publication.verify(directory, info)


class PackageVersionTests(unittest.TestCase):
    def archive(self, path, name, content):
        import io
        import tarfile
        with tarfile.open(path, "w:gz") as archive:
            member = tarfile.TarInfo(name)
            member.size = len(content)
            archive.addfile(member, io.BytesIO(content))

    def fixtures(self, directory, info):
        import zipfile
        from build import TARGETS
        self.archive(directory / f"wendylabs-core-{info['version']}.tgz", "package/package.json",
                     json.dumps({"name": "@wendylabs/core", "version": info["version"]}).encode())
        for target in TARGETS:
            with zipfile.ZipFile(directory / f"wheel-{target}.whl", "w") as archive:
                archive.writestr("wendy_core.dist-info/METADATA", f"Name: wendy-core\nVersion: {info['pythonVersion']}\n")
        for suffix in (*TARGETS, "swiftpm"):
            self.archive(directory / f"wendy-core-{info['version']}-{suffix}.tar.gz", "VERSION", info["version"].encode())

    def test_inner_versions_must_match_even_if_artifacts_are_sealed(self):
        info = derive("0.1.0", "pr", source_sha=SHA, pr=42, branch="feature", run_id=123)
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            self.fixtures(directory, info)
            publication.verify_versions(directory, info)
            self.archive(directory / f"wendylabs-core-{info['version']}.tgz", "package/package.json",
                         b'{"name":"@wendylabs/core","version":"0.1.0"}')
            with self.assertRaises(ValueError):
                publication.verify_versions(directory, info)


if __name__ == "__main__":
    unittest.main()
