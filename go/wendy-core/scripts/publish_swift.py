"""Publish an already-built SwiftPM distribution to a configured mirror.

The checkout must be a dedicated, generated distribution repository. This
script never creates a hosted repository or overwrites an existing release.
"""
import argparse
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path

from build import check_version, digest


def git(directory, *args):
    return subprocess.check_output(["git", "-C", str(directory), *args], text=True).strip()


def publish(archive, repository, push=True):
    value = check_version()
    tag = "v" + value
    existing = git(repository, "tag", "--list", tag)
    if existing:
        recorded = git(repository, "show", f"{tag}:CORE_ARTIFACT_SHA256")
        if recorded != digest(archive):
            raise ValueError("An existing SwiftPM version cannot be replaced with different bytes")
        print(f"SwiftPM {tag} already published with the same artifact")
        return
    with tempfile.TemporaryDirectory() as temp:
        with tarfile.open(archive) as source:
            for member in source.getmembers():
                path = Path(member.name)
                if path.is_absolute() or ".." in path.parts or member.issym() or member.islnk():
                    raise ValueError("Invalid SwiftPM archive entry")
            source.extractall(temp)
        if (Path(temp) / "VERSION").read_text().strip() != value:
            raise ValueError("SwiftPM archive version differs from the Core release")
        for name in ("Sources", "Tests"):
            shutil.rmtree(repository / name, ignore_errors=True)
            shutil.copytree(Path(temp) / name, repository / name)
        for name in ("Package.swift", "README.md", "VERSION", "LICENSE", "NOTICE"):
            shutil.copy2(Path(temp) / name, repository / name)
    (repository / "CORE_ARTIFACT_SHA256").write_text(digest(archive) + "\n")
    git(repository, "config", "user.name", "github-actions[bot]")
    git(repository, "config", "user.email", "41898282+github-actions[bot]@users.noreply.github.com")
    git(repository, "add", "Package.swift", "README.md", "VERSION", "LICENSE", "NOTICE", "Sources", "Tests", "CORE_ARTIFACT_SHA256")
    git(repository, "commit", "-m", f"chore: distribute Wendy Core {value}")
    git(repository, "tag", "-a", tag, "-m", f"Wendy Core {value}")
    if push:
        git(repository, "push", "--atomic", "origin", "HEAD:main", "refs/tags/" + tag)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    parser.add_argument("repository", type=Path)
    arguments = parser.parse_args()
    publish(arguments.archive, arguments.repository)
