#!/usr/bin/env python3
"""Reject build cache tar streams with entries that could escape the workspace.

Reads a tar stream on stdin. Every entry must be a regular file, directory,
symlink or hard link with a relative path that stays inside the archive root,
and every link must resolve inside the workspace given as the only argument.
Used by restore-trusted-build-cache.sh before extracting. WDY-3798.
"""

import os
import sys
import tarfile


def main() -> None:
    if len(sys.argv) != 2:
        sys.exit("usage: validate-build-cache-archive.py WORKSPACE < archive.tar")
    root = os.path.realpath(sys.argv[1])

    def inside(path: str) -> bool:
        return path == root or path.startswith(root + os.sep)

    with tarfile.open(fileobj=sys.stdin.buffer, mode="r|") as archive:
        for member in archive:
            name = os.path.normpath(member.name)
            if os.path.isabs(member.name) or name == ".." or name.startswith(".." + os.sep):
                sys.exit(f"unsafe path: {member.name}")
            if member.isfile() or member.isdir():
                continue
            if member.issym():
                target = member.linkname
                if not os.path.isabs(target):
                    target = os.path.join(root, os.path.dirname(name), target)
            elif member.islnk():
                target = os.path.join(root, member.linkname)
            else:
                sys.exit(f"unsupported entry type: {member.name}")
            if not inside(os.path.normpath(target)):
                sys.exit(f"link escapes the workspace: {member.name} -> {member.linkname}")


if __name__ == "__main__":
    main()
