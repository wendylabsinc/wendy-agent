"""Local and CI packaging mechanics. No registry access or product behavior."""
import argparse
import hashlib
import json
import os
import platform
import re
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BUILD = ROOT / ".build"
TARGETS = ("linux-x64", "linux-arm64", "darwin-x64", "darwin-arm64", "win32-x64")
SWIFT_TARGETS = ("linux-x64", "darwin-x64", "darwin-arm64")
LIBRARIES = {"linux": "libWendyCore.so", "darwin": "libWendyCore.dylib", "win32": "WendyCore.dll"}
JNI = {"linux": "libwendy_core_jni.so", "darwin": "libwendy_core_jni.dylib", "win32": "wendy_core_jni.dll"}
JS = ROOT / "distributions/javascript"
SWIFT = ROOT / "distributions/swift"
KOTLIN = ROOT / "distributions/kotlin"
PYTHON = ROOT / "distributions/python"


def run(*args, cwd=ROOT, env=None):
    subprocess.run([str(x) for x in args], cwd=cwd, env=env, check=True)


def version():
    value = (ROOT / "VERSION").read_text().strip()
    if not re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", value):
        raise ValueError("VERSION must be a stable SemVer x.y.z (prereleases not yet packaged)")
    return value


def check_version(tag=None):
    value = version()
    package = json.loads((JS / "package.json").read_text())
    if package["version"] != value:
        raise ValueError("npm version differs from VERSION")
    if f'version = "{value}"' not in (PYTHON / "pyproject.toml").read_text():
        raise ValueError("Python version differs from VERSION")
    if f"coreVersion={value}\n" != (KOTLIN / "gradle.properties").read_text():
        raise ValueError("Kotlin version differs from VERSION")
    if tag and tag != "v" + value:
        raise ValueError(f"Tag {tag!r} must match v{value}")
    return value


def host_target():
    system = {"Linux": "linux", "Darwin": "darwin", "Windows": "win32"}[platform.system()]
    architecture = {"x86_64": "x64", "AMD64": "x64", "arm64": "arm64", "aarch64": "arm64"}[platform.machine()]
    target = system + "-" + architecture
    if target not in TARGETS:
        raise ValueError(f"Unsupported initial target: {target}")
    return target


def copy(source, destination):
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def go_license(goroot):
    # Homebrew keeps notices beside libexec rather than within GOROOT.
    for path in (goroot / "LICENSE", goroot.parent / "LICENSE"):
        if path.is_file():
            return path
    raise FileNotFoundError("Cannot locate the bundled Go toolchain license")


def stage_licenses():
    for distribution in (JS, PYTHON, SWIFT):
        for name in ("LICENSE", "NOTICE"):
            copy(ROOT / name, distribution / name)


def stage_native(target, directory):
    stage_licenses()
    system = target.split("-")[0]
    library = directory / LIBRARIES[system]
    copy(library, JS / "assets/native" / target / library.name)
    license_file = directory / "LICENSE-Go.txt"
    if license_file.exists():
        copy(license_file, JS / "assets/native" / target / license_file.name)
        copy(license_file, KOTLIN / "src/main/resources/native" / target / license_file.name)
        if target in SWIFT_TARGETS:
            copy(license_file, SWIFT / "Sources/WendyCore/native" / target / license_file.name)
    if target in SWIFT_TARGETS:
        copy(library, SWIFT / "Sources/WendyCore/native" / target / library.name)
    python_native = PYTHON / "wendy_core/native"
    shutil.rmtree(python_native, ignore_errors=True)
    copy(library, python_native / library.name)
    if license_file.exists():
        copy(license_file, python_native / license_file.name)
    copy(library, KOTLIN / "src/main/resources/native" / target / library.name)
    for name in ("wendy_core.node", JNI[system]):
        path = directory / name
        if path.exists():
            destination = JS / "assets/native" / target / name if name.endswith(".node") else KOTLIN / "src/main/resources/native" / target / name
            copy(path, destination)
    # The standalone SwiftPM distribution gets a private copy of the same
    # loader/header, so consumers need neither a Go toolchain nor this monorepo.
    for name, source in {
        "native_loader.h": ROOT / "internal/bridge/native_loader.h",
        "WendyCore.h": ROOT / "include/WendyCore/WendyCore.h",
    }.items():
        copy(source, SWIFT / "Sources/CWendyCoreBridge" / name)
        (SWIFT / "Sources/CWendyCoreBridge/include" / name).unlink(missing_ok=True)


def native():
    check_version()
    target = host_target()
    system = target.split("-")[0]
    directory = BUILD / "native" / target
    directory.mkdir(parents=True, exist_ok=True)
    library = directory / LIBRARIES[system]
    env = {**os.environ, "CGO_ENABLED": "1"}
    if system == "darwin":
        env["MACOSX_DEPLOYMENT_TARGET"] = "15.0"
        env["CGO_CFLAGS"] = env.get("CGO_CFLAGS", "-O2 -g") + " -mmacosx-version-min=15.0"
        env["CGO_LDFLAGS"] = env.get("CGO_LDFLAGS", "") + " -mmacosx-version-min=15.0"
    linker = ["-ldflags=-extldflags=-Wl,-install_name,@rpath/libWendyCore.dylib"] if system == "darwin" else []
    run("go", "build", "-trimpath", "-buildmode=c-shared", *linker, "-o", library, "./cmd/wendy-core-native", env=env)
    # Test an independently compiled C consumer of the public header and symbol.
    compiler = os.environ.get("CC", "cc" if system != "win32" else "gcc")
    executable = directory / ("hello.exe" if system == "win32" else "hello")
    flags = [] if system == "win32" else [f"-Wl,-rpath,{directory}"]
    run(compiler, "-std=c11", "-Iinclude", "examples/c/hello.c", library, *flags, "-o", executable)
    run(executable)
    goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
    copy(go_license(goroot), directory / "LICENSE-Go.txt")
    stage_native(target, directory)


def node_addon():
    target = host_target()
    npm = "npm.cmd" if platform.system() == "Windows" else "npm"
    run(npm, "run", "build:native", cwd=JS)
    output = JS / "native/build/Release/wendy_core.node"
    copy(output, BUILD / "native" / target / output.name)
    stage_native(target, BUILD / "native" / target)


def jni():
    target = host_target()
    system = target.split("-")[0]
    java = Path(os.environ["JAVA_HOME"])
    java_system = {"linux": "linux", "darwin": "darwin", "win32": "win32"}[system]
    output = BUILD / "native" / target / JNI[system]
    compiler = os.environ.get("CC", "cc" if system != "win32" else "gcc")
    flags = ["-dynamiclib", "-mmacosx-version-min=15.0"] if system == "darwin" else ["-shared"]
    if system != "win32":
        flags += ["-fPIC"]
    if system == "linux":
        flags += ["-ldl"]
    env = dict(os.environ)
    if system == "darwin":
        env["MACOSX_DEPLOYMENT_TARGET"] = "15.0"
    run(compiler, "-Iinclude/WendyCore", "-Iinternal/bridge", f"-I{java / 'include'}", f"-I{java / 'include' / java_system}", "distributions/kotlin/native/bridge.c", *flags, "-o", output, env=env)
    stage_native(target, BUILD / "native" / target)


def wasm():
    check_version()
    stage_licenses()
    output = JS / "assets/wasm"
    output.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "GOOS": "js", "GOARCH": "wasm", "CGO_ENABLED": "0"}
    run("go", "build", "-trimpath", "-o", output / "wendy_core.wasm", "./cmd/wendy-core-wasm", env=env)
    goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
    copy(goroot / "lib/wasm/wasm_exec.js", output / "wasm_exec.mjs")
    copy(go_license(goroot), output / "LICENSE-Go.txt")
    files = {name: digest(output / name) for name in ("wendy_core.wasm", "wasm_exec.mjs", "LICENSE-Go.txt")}
    (output / "manifest.json").write_text(json.dumps({"version": version(), "target": "browser-wasm", "files": files}, indent=2) + "\n")


def inventory():
    target = host_target()
    directory = BUILD / "native" / target
    expected = (LIBRARIES[target.split("-")[0]], JNI[target.split("-")[0]], "wendy_core.node", "LICENSE-Go.txt")
    if target.startswith("darwin-"):
        for name in expected:
            if name.endswith(".txt"):
                continue
            commands = subprocess.check_output(["otool", "-l", str(directory / name)], text=True)
            minimum = re.search(r"minos (\d+)\.(\d+)", commands)
            if minimum is None or tuple(int(x) for x in minimum.groups()) > (15, 0):
                raise ValueError(f"{name} does not meet the declared macOS 15 minimum")
    files = {name: digest(directory / name) for name in expected}
    (directory / "manifest.json").write_text(json.dumps({"version": check_version(), "target": target, "files": files}, indent=2) + "\n")


def assemble(artifacts):
    check_version()
    # Validate everything before replacing the staged package resources.
    for target in TARGETS:
        directory = artifacts / ("native-" + target)
        manifest = json.loads((directory / "manifest.json").read_text())
        if manifest["target"] != target or manifest["version"] != version():
            raise ValueError(f"Wrong native artifact identity: {target}")
        expected = {LIBRARIES[target.split("-")[0]], JNI[target.split("-")[0]], "wendy_core.node", "LICENSE-Go.txt"}
        if set(manifest["files"]) != expected:
            raise ValueError(f"Incomplete native artifact: {target}")
        for name, sha in manifest["files"].items():
            if digest(directory / name) != sha:
                raise ValueError(f"Native checksum mismatch: {target}/{name}")
    wasm_directory = artifacts / "wasm"
    wasm_manifest = json.loads((wasm_directory / "manifest.json").read_text())
    if wasm_manifest["version"] != version() or wasm_manifest["target"] != "browser-wasm":
        raise ValueError("Wrong WASM artifact identity")
    if set(wasm_manifest["files"]) != {"wendy_core.wasm", "wasm_exec.mjs", "LICENSE-Go.txt"}:
        raise ValueError("Incomplete WASM artifact")
    for name, sha in wasm_manifest["files"].items():
        if digest(wasm_directory / name) != sha:
            raise ValueError(f"WASM checksum mismatch: {name}")
    for destination in (JS / "assets", SWIFT / "Sources/WendyCore/native", KOTLIN / "src/main/resources/native"):
        shutil.rmtree(destination, ignore_errors=True)
    for target in TARGETS:
        stage_native(target, artifacts / ("native-" + target))
    for name in (*wasm_manifest["files"], "manifest.json"):
        copy(wasm_directory / name, JS / "assets/wasm" / name)


def swift_archive(destination):
    stage_licenses()
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(destination, "w:gz") as archive:
        for name in ("Package.swift", "Sources", "Tests", "README.md", "LICENSE", "NOTICE"):
            archive.add(SWIFT / name, arcname=name)
        archive.add(ROOT / "VERSION", arcname="VERSION")


def pack(artifacts):
    check_version()
    stage_licenses()
    release = BUILD / "release"
    shutil.rmtree(release, ignore_errors=True)
    release.mkdir(parents=True)
    npm = "npm.cmd" if platform.system() == "Windows" else "npm"
    run(npm, "pack", "--pack-destination", release, cwd=JS)
    for target in TARGETS:
        system = target.split("-")[0]
        directory = artifacts / ("native-" + target)
        with tarfile.open(release / f"wendy-core-{version()}-{target}.tar.gz", "w:gz") as archive:
            archive.add(directory / LIBRARIES[system], arcname="lib/" + LIBRARIES[system])
            archive.add(directory / "LICENSE-Go.txt", arcname="LICENSE-Go.txt")
            archive.add(ROOT / "include", arcname="include")
            archive.add(ROOT / "proto", arcname="proto")
            archive.add(ROOT / "VERSION", arcname="VERSION")
            for name in ("LICENSE", "NOTICE"):
                archive.add(ROOT / name, arcname=name)
    # Complete, standalone SwiftPM source distribution (including native assets).
    swift_archive(release / f"wendy-core-{version()}-swiftpm.tar.gz")
    for wheel in artifacts.glob("wheel-*/*.whl"):
        copy(wheel, release / wheel.name)
    if len(list(release.glob("*.whl"))) != len(TARGETS):
        raise ValueError("Every native target must contribute a Python wheel")
    (release / "SHA256SUMS").write_text("".join(f"{digest(path)}  {path.name}\n" for path in sorted(release.iterdir()) if path.is_file()))
    (release / "release.json").write_text(json.dumps({"version": version(), "targets": TARGETS, "javascript": ["node", "browser"], "goModule": "github.com/wendylabsinc/wendy-core/core"}, indent=2) + "\n")


def seal():
    release = BUILD / "release"
    documentation = release / "documentation"
    if documentation.exists():
        shutil.make_archive(str(release / "documentation"), "zip", documentation)
        shutil.rmtree(documentation)
    sums = release / "SHA256SUMS"
    sums.write_text("".join(f"{digest(path)}  {path.relative_to(release).as_posix()}\n" for path in sorted(release.rglob("*")) if path.is_file() and path != sums))


def test_packed_npm():
    npm = "npm.cmd" if platform.system() == "Windows" else "npm"
    package_directory = BUILD / "npm-smoke"
    package_directory.mkdir(parents=True, exist_ok=True)
    run(npm, "pack", "--pack-destination", package_directory, cwd=JS)
    package = package_directory / f"wendylabs-core-{version()}.tgz"
    with tempfile.TemporaryDirectory() as temp:
        path = Path(temp)
        (path / "package.json").write_text('{"type":"module","private":true}')
        run(npm, "install", "--ignore-scripts", "--no-audit", "--no-fund", package, cwd=path)
        conformance = (JS / "tests/conformance.mjs").as_uri()
        run("node", "--input-type=module", "-e", f'import * as core from "@wendylabs/core"; import {{assertCore}} from "{conformance}"; assertCore(core);', cwd=path)
        env = {**os.environ, "CORE_PACKAGE_ROOT": str(path / "node_modules/@wendylabs/core")}
        npx = "npx.cmd" if platform.system() == "Windows" else "npx"
        run(npx, "--no-install", "playwright", "test", cwd=JS, env=env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["version", "native", "node", "jni", "wasm", "inventory", "assemble", "pack", "seal", "swift-archive", "test-npm"])
    parser.add_argument("--tag")
    parser.add_argument("--artifacts", type=Path, default=BUILD / "artifacts")
    arguments = parser.parse_args()
    if arguments.command == "version":
        print(check_version(arguments.tag))
    elif arguments.command == "swift-archive":
        swift_archive(BUILD / f"wendy-core-{version()}-swiftpm.tar.gz")
    elif arguments.command in ("assemble", "pack"):
        globals()[arguments.command](arguments.artifacts)
    else:
        {"native": native, "node": node_addon, "jni": jni, "wasm": wasm, "inventory": inventory, "test-npm": test_packed_npm, "seal": seal}[arguments.command]()


if __name__ == "__main__":
    main()
