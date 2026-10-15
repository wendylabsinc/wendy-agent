#!/usr/bin/env python3
"""Validate and export the public Wendy plugin without local policies or credentials."""

import argparse
import json
from pathlib import Path
import re
import struct
from urllib.parse import urlsplit
import zipfile
import zlib


ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "plugins/wendy-chatgpt"
ENDPOINT = "https://mcp.wendy.dev"
LISTING_URLS = ("websiteURL", "supportURL", "privacyPolicyURL", "termsOfServiceURL")
IGNORED_DIRECTORIES = {"__pycache__", "__MACOSX", "node_modules"}
IGNORED_SUFFIXES = {".pyc", ".pyo", ".swp", ".swo", ".tmp", ".bak"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def nonblank(value):
    return isinstance(value, str) and bool(value.strip())


def https_url(value):
    if not nonblank(value) or len(value) > 1024 or any(c.isspace() for c in value):
        return False
    try:
        url = urlsplit(value)
        return (url.scheme == "https" and bool(url.hostname) and not url.username
                and not url.password and url.hostname not in ("localhost", "example.com")
                and not url.hostname.endswith(".example.com"))
    except ValueError:
        return False


def png_dimensions(data, label):
    """Check the PNG container before trusting its declared dimensions."""
    require(data[:8] == b"\x89PNG\r\n\x1a\n" and len(data) <= 5 * 1024 * 1024, f"{label} must be a PNG of at most 5 MiB")
    offset, dimensions, image_bytes = 8, None, 0
    image_started, image_ended = False, False
    while offset < len(data):
        require(len(data) - offset >= 12, f"{label} has a truncated PNG chunk")
        length = struct.unpack_from(">I", data, offset)[0]
        kind = data[offset + 4:offset + 8]
        require(re.fullmatch(rb"[A-Za-z]{4}", kind) and kind[2:3].isupper(), f"{label} has an invalid PNG chunk type")
        end = offset + 12 + length
        require(end <= len(data), f"{label} has a truncated PNG chunk")
        payload = data[offset + 8:end - 4]
        checksum = struct.unpack_from(">I", data, end - 4)[0]
        require(zlib.crc32(data[offset + 4:end - 4]) == checksum, f"{label} has an invalid PNG chunk CRC")
        if dimensions is None:
            require(kind == b"IHDR" and length == 13, f"{label} must begin with a 13-byte PNG IHDR")
            width, height, depth, color, compression, filtering, interlace = struct.unpack(">IIBBBBB", payload)
            depths = {0: {1, 2, 4, 8, 16}, 2: {8, 16}, 3: {1, 2, 4, 8}, 4: {8, 16}, 6: {8, 16}}
            require(depth in depths.get(color, set()) and compression == filtering == 0 and interlace in (0, 1), f"{label} has invalid PNG IHDR fields")
            dimensions = (width, height)
        else:
            require(kind != b"IHDR", f"{label} has a duplicate PNG IHDR")
        if kind == b"IDAT":
            require(not image_ended, f"{label} has nonconsecutive PNG IDAT chunks")
            image_started = True
            image_bytes += length
        elif image_started:
            image_ended = True
        if kind == b"IEND":
            require(length == 0 and end == len(data), f"{label} must end with an empty PNG IEND")
            require(image_bytes > 0, f"{label} is missing PNG image data")
            return dimensions
        offset = end
    raise ValueError(f"{label} is missing PNG IEND")


def submission_gaps(extension):
    """Check required metadata, without claiming remote pages or cases were verified."""
    interface = extension["interface"]
    gaps = []
    for field in LISTING_URLS:
        value = interface.get(field)
        if not value:
            gaps.append("interface." + field)
        else:
            require(https_url(value), f"interface.{field} must be an absolute HTTPS URL without credentials")
    review = extension.get("review", {})
    publication = extension.get("publication", {})
    require(isinstance(review, dict) and isinstance(publication, dict), "review and publication must be objects")
    test_cases = review.get("test_cases", {})
    require(isinstance(test_cases, dict), "review.test_cases must be an object")
    for kind, minimum in (("positive", 5), ("negative", 3)):
        cases = test_cases.get(kind, [])
        require(isinstance(cases, list), f"review.test_cases.{kind} must be an array")
        if len(cases) < minimum:
            gaps.append(f"review.test_cases.{kind}: need at least {minimum}, found {len(cases)}")
        prompts = []
        for index, case in enumerate(cases):
            label = f"review.test_cases.{kind}[{index}]"
            require(isinstance(case, dict), f"{label} must be an object")
            fields = ("description", "prompt", "tools_triggered", "expected_behavior") if kind == "positive" else ("description", "prompt")
            for field in fields:
                require(nonblank(case.get(field)), f"{label}.{field} must be a nonblank string")
            prompts.append(" ".join(case["prompt"].split()).casefold())
            for field in ("expected_output_url",):
                if field in case:
                    require(https_url(case[field]), f"{label}.{field} must be an HTTPS URL")
            if "file_attachment_urls" in case:
                require(isinstance(case["file_attachment_urls"], list) and all(https_url(u) for u in case["file_attachment_urls"]), f"{label}.file_attachment_urls must contain HTTPS URLs")
        require(len(set(prompts)) == len(prompts), f"Duplicate {kind} review prompts")
    demo = review.get("demo_recording_url")
    if not demo:
        gaps.append("review.demo_recording_url: record and verify the real walkthrough")
    else:
        require(https_url(demo), "review.demo_recording_url must be an HTTPS URL")
    if "commerce" not in review:
        gaps.append("review.commerce")
    else:
        require(isinstance(review["commerce"], bool), "review.commerce must be a boolean")
        if review["commerce"] and not nonblank(review.get("commerce_description")):
            gaps.append("review.commerce_description")
    if not nonblank(publication.get("release_notes")):
        gaps.append("publication.release_notes")
    if "countries" not in publication:
        gaps.append("publication.countries: an explicit empty array means all countries")
    else:
        countries = publication["countries"]
        require(isinstance(countries, list) and all(isinstance(c, str) and re.fullmatch(r"[A-Z]{2}", c) for c in countries), "publication.countries must contain two-letter country codes, or be [] for all countries")
        require(len(set(countries)) == len(countries), "Duplicate country codes")
    return gaps


def validate_package(source):
    source = source.resolve()
    for path in (source / "plugin.json", source / "mcp.json", source / "skills", source / "assets"):
        require(not path.is_symlink(), f"Symlinks are not allowed: {path}")
    manifest = json.loads((source / "plugin.json").read_text())
    extension = manifest["extensions"]["com.openai"]
    interface = extension["interface"]
    require(manifest["name"] == "wendy-robots", "Preserve the wendy-robots plugin identity")
    require(re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?", manifest.get("version", "")), "Use an explicit semantic version")
    require(not (source / ".app.json").exists(), "Public uploads cannot contain .app.json; prepare a separate upload source")
    for metadata in (manifest, extension):
        for field in ("apps", "hooks"):
            require(metadata.get(field) is None, f"Public uploads cannot declare {field}")
    for field, maximum in (("displayName", 30), ("shortDescription", 30), ("longDescription", 4000), ("developerName", 80)):
        value = interface.get(field)
        require(nonblank(value) and len(value) <= maximum, f"interface.{field} must have 1–{maximum} characters")
    require(manifest.get("author", {}).get("name") == interface["developerName"], "Author and developerName must identify the same publisher")
    prompts = interface["defaultPrompt"]
    require(isinstance(prompts, list) and 1 <= len(prompts) <= 3, "Use 1–3 default prompts")
    require(all(nonblank(p) and len(p) <= 128 and "\n" not in p and "\r" not in p for p in prompts), "Default prompts must be nonblank, single-line strings of at most 128 characters")
    require(len({" ".join(p.split()) for p in prompts}) == len(prompts), "Default prompts must be unique")

    mcp = json.loads((source / "mcp.json").read_text())
    require(set(mcp["mcpServers"]) == {"wendy-robots"}, "The public plugin must declare exactly its Wendy server")
    server = mcp["mcpServers"]["wendy-robots"]
    require(server.get("type") == "streamable-http" and server.get("url") == ENDPOINT, f"Use the hosted MCP endpoint {ENDPOINT}")
    require(not any(key in server for key in ("command", "args", "env")) and not server.get("headers"), "Do not package local MCP commands, environment values, or authentication headers")
    auth = server.get("extensions", {}).get("com.openai", {}).get("auth", {})
    require(auth.get("type") == "oauth", "The standalone Cloud gateway requires OAuth")
    require("clientSecret" not in auth and "clientSecret" not in auth.get("client", {}), "Never package an OAuth client secret")
    require(set(auth.get("baseScopes", [])) == {"robots:read", "cameras:capture", "apps:control", "preferences:write"}, "Declare the standalone gateway scopes; mcp:relay belongs to the separate local-session relay")
    client = auth.get("client", {})
    require(client.get("mode") == "provided" and nonblank(client.get("clientId")) and client.get("tokenEndpointAuthMethod") == "none", "Declare the standalone gateway's registered public OAuth client")

    # Root inventory excludes developer notes and policies. Reject hidden files
    # and generated artifacts inside the public component directories as well.
    files = [source / "plugin.json", source / "mcp.json"]
    for directory in (source / "skills", source / "assets"):
        for path in sorted(directory.rglob("*")):
            require(not path.is_symlink(), f"Symlinks are not allowed: {path}")
            require(not any(part.startswith(".") or part in IGNORED_DIRECTORIES for part in path.relative_to(directory).parts)
                    and path.suffix.lower() not in IGNORED_SUFFIXES and not path.name.endswith("~"),
                    f"Hidden or generated files are not allowed in the public package: {path}")
            if path.is_file():
                files.append(path)
    require(source / "skills/wendy/SKILL.md" in files, "Missing Wendy skill")
    onboarding = extension.get("onboardingSkill", "")
    require(onboarding.startswith("./skills/") and (source / onboarding).resolve() in files, "onboardingSkill must point to an included skill")
    for field, minimum in (("logo", 256), ("composerIcon", 48), ("logoDark", 256), ("composerIconDark", 48)):
        if field not in interface and field.endswith("Dark"):
            continue
        relative = interface.get(field, "")
        path = (source / relative).resolve()
        require(relative.startswith("./assets/") and path.is_relative_to(source) and path in files, f"{field} must reference an included asset")
        data = path.read_bytes()
        width, height = png_dimensions(data, field)
        require(width == height and minimum <= width <= 4096, f"{field} must be square and {minimum}–4096 pixels")
    return manifest, mcp, files, submission_gaps(extension)


def export_package(source, output, manifest, mcp, files):
    source, output = source.resolve(), output.resolve()
    require(not output.is_relative_to(source), "Write the ZIP outside the source plugin directory")
    output.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as archive:
        for path in files:
            name = str(Path(manifest["name"]) / path.relative_to(source))
            info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100644 << 16
            archive.writestr(info, path.read_bytes())
    with zipfile.ZipFile(output) as archive:
        require(archive.testzip() is None, "ZIP integrity check failed")
        prefix = manifest["name"] + "/"
        require(set(archive.namelist()) == {prefix + str(p.relative_to(source)) for p in files}, "ZIP inventory mismatch")
        require(json.loads(archive.read(prefix + "plugin.json")) == manifest, "ZIP manifest mismatch")
        require(json.loads(archive.read(prefix + "mcp.json")) == mcp, "ZIP MCP configuration mismatch")
        submission_gaps(json.loads(archive.read(prefix + "plugin.json"))["extensions"]["com.openai"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--output", type=Path, help="Destination ZIP outside the plugin directory")
    action.add_argument("--check", action="store_true", help="Validate source without writing a ZIP")
    parser.add_argument("--require-submission-metadata", action="store_true", help="Fail if listing, cases, demo URL, or publication metadata is incomplete; does not verify online readiness")
    args = parser.parse_args()
    try:
        manifest, mcp, files, gaps = validate_package(SOURCE)
        if args.require_submission_metadata and gaps:
            raise ValueError("Incomplete submission metadata:\n- " + "\n- ".join(gaps))
        if args.output:
            export_package(SOURCE, args.output, manifest, mcp, files)
            print(f"Hosted package: {args.output.resolve()} ({args.output.stat().st_size:,} bytes; {len(files)} files)")
        else:
            print(f"Package valid: {manifest['name']} {manifest['version']} ({len(files)} files)")
        if gaps:
            print("Incomplete submission metadata:\n- " + "\n- ".join(gaps))
        else:
            print("Required submission metadata is present.")
        print("Online readiness is separate: verify policy coverage, recording playback, reviewer access, live cases, domain/developer verification, and portal scans.")
    except (ValueError, KeyError, TypeError, OSError) as error:
        parser.exit(1, f"Package validation failed: {error}\n")


if __name__ == "__main__":
    main()
