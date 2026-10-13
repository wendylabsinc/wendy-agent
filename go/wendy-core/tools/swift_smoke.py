"""Consume the packaged SwiftPM artifact through a real local SemVer dependency."""
import argparse
import tempfile
from pathlib import Path

from build import check_version, run
from publish_swift import publish


def smoke(archive, xcode_mcp=False):
    value = check_version()
    with tempfile.TemporaryDirectory(prefix="wendy-core-swift-") as temp:
        root = Path(temp)
        mirror = root / "mirror"
        run("git", "init", "-b", "main", mirror)
        publish(archive, mirror, push=False)
        consumer = root / "consumer"
        tests = consumer / "Tests/SmokeTests"
        tests.mkdir(parents=True)
        (consumer / "Package.swift").write_text('''// swift-tools-version: 6.0
import PackageDescription
let package = Package(
    name: "CoreConsumer",
    platforms: [.macOS(.v15)],
    dependencies: [.package(url: "%s", from: "%s")],
    targets: [.testTarget(name: "SmokeTests", dependencies: [.product(name: "WendyCore", package: "mirror")])]
)
''' % (mirror.as_uri(), value))
        (tests / "Smoke.swift").write_text('''import Testing
import WendyCore
@Test func consumesCoreThroughAnOrdinaryDependency() throws {
    #expect(try WendyHelloWorld() == 42)
}
''')
        if xcode_mcp:
            run("xcodebuildmcp", "swift-package", "test", "--package-path", consumer)
        else:
            run("swift", "test", cwd=consumer)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    parser.add_argument("--xcode-mcp", action="store_true")
    arguments = parser.parse_args()
    smoke(arguments.archive.resolve(), arguments.xcode_mcp)
