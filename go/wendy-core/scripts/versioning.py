"""One release identity, derived from the next stable VERSION and build context."""
import argparse
import json
import os
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
STABLE = r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
SHA = r"[0-9a-f]{40}"


def base_version(root=ROOT):
    value = (root / "VERSION").read_text().strip()
    if not re.fullmatch(STABLE, value):
        raise ValueError("VERSION must name the next stable release: x.y.z")
    return value


def slug(value):
    value = re.sub(r"[^a-z0-9-]+", "-", value.lower()).strip("-")[:40].rstrip("-")
    return value or "branch"


def derive(base, channel, *, source_sha, branch="", pr=None, run_id=0, attempt=1):
    if not re.fullmatch(STABLE, base) or not (
            source_sha is None and channel == "dev" or
            isinstance(source_sha, str) and re.fullmatch(SHA, source_sha)):
        raise ValueError("Invalid base version or source commit")
    if channel not in ("stable", "nightly", "pr", "dev"):
        raise ValueError("Unknown release channel")
    if type(run_id) is not int or type(attempt) is not int or (pr is not None and type(pr) is not int):
        raise ValueError("Build counters and PR numbers must be integers")
    if channel in ("nightly", "pr") and (run_id <= 0 or not 0 < attempt < 1000):
        raise ValueError("Published previews need a positive run ID and attempt below 1000")
    if pr is not None and pr <= 0:
        raise ValueError("PR numbers must be positive")
    suffix = f"{run_id}.{attempt}"
    if channel == "stable":
        value, python, npm_tag = base, base, "latest"
    elif channel == "dev":
        value = base + "-dev" + ("+" + source_sha[:7] if source_sha else "")
        python, npm_tag = base + ".dev0", ""
    else:
        # GitHub run IDs are unique across workflows; attempts reserve distinct
        # versions for fresh rebuilds. Failed publication retries reuse identity.
        python = f"{base}.dev{run_id * 1000 + attempt}"
        if channel == "nightly":
            value, npm_tag = f"{base}-nightly.{suffix}", "nightly"
        else:
            label = str(pr) if pr is not None else "manual"
            value = f"{base}-pr.{label}-{slug(branch)}.{suffix}"
            npm_tag = f"pr-{pr}" if pr is not None else "preview-" + slug(branch)
    return {
        "schemaVersion": 1, "baseVersion": base, "version": value,
        "pythonVersion": python, "channel": channel, "npmTag": npm_tag,
        "sourceSha": source_sha, "branch": branch, "pr": pr,
        "runId": run_id, "attempt": attempt,
    }


def validate(info):
    expected = derive(info["baseVersion"], info["channel"], source_sha=info["sourceSha"],
                      branch=info["branch"], pr=info["pr"], run_id=info["runId"], attempt=info["attempt"])
    if info != expected:
        raise ValueError("Release identity differs from its build context")
    return expected


def checkout_revision():
    # A source archive nested in another repository must not inherit its HEAD.
    if not (ROOT / ".git").exists():
        return None
    try:
        return subprocess.check_output(["git", "rev-parse", "--verify", "HEAD"], cwd=ROOT,
                                       text=True, stderr=subprocess.DEVNULL).strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        return None


def identity():
    if os.environ.get("CORE_BUILD_IDENTITY"):
        info = validate(json.loads(os.environ["CORE_BUILD_IDENTITY"]))
        if info["baseVersion"] != base_version():
            raise ValueError("Release identity base differs from VERSION")
        sha = checkout_revision()
        if info["sourceSha"] != sha:
            raise ValueError("Release identity source differs from the checkout")
        return info
    return derive(base_version(), "dev", source_sha=checkout_revision())


def github_plan(event, event_name, ref_type, ref_name, run_id, attempt):
    if event_name == "pull_request":
        pr = event["pull_request"]
        return derive(base_version(), "pr", source_sha=pr["head"]["sha"],
                      branch=pr["head"]["ref"], pr=event["number"], run_id=run_id, attempt=attempt)
    sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    if ref_type == "tag":
        if event_name != "push":
            raise ValueError("Stable publication starts with a tag push; retry the original run, not a new build")
        if ref_name != "v" + base_version():
            raise ValueError("Stable tags must match VERSION exactly; preview tags are not release triggers")
        channel = "stable"
    elif event_name == "push" and ref_name == "main":
        channel = "nightly"
    elif event_name == "workflow_dispatch":
        channel = "pr"
    else:
        raise ValueError("Unsupported build trigger")
    return derive(base_version(), channel, source_sha=sha, branch=ref_name, run_id=run_id, attempt=attempt)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("base", "plan"))
    parser.add_argument("--github-output", action="store_true")
    args = parser.parse_args()
    if args.command == "base":
        print(base_version())
        return
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    info = github_plan(event, os.environ["GITHUB_EVENT_NAME"], os.environ["GITHUB_REF_TYPE"],
                       os.environ["GITHUB_REF_NAME"], int(os.environ["GITHUB_RUN_ID"]),
                       int(os.environ["GITHUB_RUN_ATTEMPT"]))
    text = json.dumps(info, separators=(",", ":"))
    print(text)
    if args.github_output:
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"identity={text}\nsource_sha={info['sourceSha']}\n")


if __name__ == "__main__":
    main()
