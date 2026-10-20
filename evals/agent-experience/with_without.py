#!/usr/bin/env python3
"""Matched Codex/Wendy CLI versus Codex/Docker deployment experiment.

The protocol is written before measured calls. Raw events and independent
evidence are retained; neither arm can see the verifier or earlier attempts.
Uses the existing benchmark's token accounting and bounded process runner.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import random
import re
import shutil
import socket
import statistics
import subprocess
import tempfile
import time
import urllib.request

import benchmark as b

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
SERVICE = HERE / "fixture_apps/g1_replay/service.py"
SKILLS = ROOT / "plugins/wendy-agentic-coding/skills"
BASE = "g1-sim-bench-deps:20261016"


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def command(argv, cwd=None, env=None, timeout=180):
    p = subprocess.run(list(map(str, argv)), cwd=cwd, env=env,
                       capture_output=True, text=True, timeout=timeout)
    return {"argv": list(map(str, argv)), "exit_code": p.returncode,
            "stdout": p.stdout, "stderr": p.stderr}


def checked(argv, **kwargs):
    result = command(argv, **kwargs)
    if result["exit_code"]:
        raise RuntimeError(json.dumps(result))
    return result


def manifests(workspace, ident, port):
    (workspace / "Dockerfile").write_text(
        f"FROM {BASE}\nWORKDIR /app\nCOPY service.py config.json ./\n"
        "COPY assets ./assets\nENTRYPOINT [\"python\",\"service.py\"]\n")
    (workspace / "compose.yaml").write_text(
        f"name: {ident}\nservices:\n  replay:\n    build: .\n"
        f"    image: {ident}:latest\n    container_name: {ident}\n"
        f"    ports: [\"127.0.0.1:{port}:8080\"]\n"
        "    environment:\n      MUJOCO_GL: disable\n      OMP_NUM_THREADS: '1'\n"
        "      OPENBLAS_NUM_THREADS: '1'\n    restart: unless-stopped\n")


def stage(workspace, assets, ident, broken=False):
    (workspace / "assets").mkdir()
    shutil.copy2(SERVICE, workspace / "service.py")
    for name in ("model.mjb", "rollout.npz", "result.json"):
        shutil.copy2(assets / name, workspace / "assets" / name)
    b.dump(workspace / "config.json", {"model_file": "model.mjb",
           "rollout_file": "missing.npz" if broken else "rollout.npz",
           "nonce": ident, "version": "v1"})
    (workspace / ".dockerignore").write_text(".agents\n.codex\nAGENTS.md\n")


def validate_samples(samples, ident, hashes):
    if len(samples) < 2:
        raise RuntimeError("two independent samples required")
    for s in samples:
        if (s.get("schema") != "g1-replay-eval.v1" or s.get("nonce") != ident
                or s.get("version") != "v1" or s.get("error") is not None
                or s.get("assets") != hashes or s.get("actuators") != 43
                or len(set(s.get("joint_names", []))) != 43
                or len(s.get("joint_positions", [])) != 43
                or not all(math.isfinite(x) for x in s["joint_positions"])
                or abs(time.time() - s.get("wall_time", 0)) > 15):
            raise RuntimeError("identity, freshness, assets or joint contract failed")
    for a, z in zip(samples, samples[1:]):
        if z["tick"] <= a["tick"] or z["simulation_time"] <= a["simulation_time"]:
            raise RuntimeError("simulation is not advancing")
        if a["joint_positions"] == z["joint_positions"]:
            raise RuntimeError("joint state is frozen")


def verify(workspace, ident, port, hashes, frozen, env):
    evidence = {"success": False}
    try:
        for name, digest in frozen.items():
            if sha(workspace / name) != digest:
                raise RuntimeError(f"immutable source changed: {name}")
        samples = []
        for _ in range(3):
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/state", timeout=8) as response:
                samples.append(json.load(response))
            time.sleep(.3)
        evidence["samples"] = samples
        validate_samples(samples, ident, hashes)
        inspect = checked(["docker", "inspect", ident], env=env)
        container = json.loads(inspect["stdout"])[0]
        evidence["container"] = container
        if not container["State"]["Running"]:
            raise RuntimeError("container is not running")
        # Check actual deployed source and data, not just the editable workspace.
        paths = list(frozen) + ["config.json"]
        script = "import hashlib,json;from pathlib import Path;print(json.dumps({p:hashlib.sha256(Path('/app',p).read_bytes()).hexdigest() for p in " + repr(paths) + "}))"
        inside = checked(["docker", "exec", ident, "python", "-c", script], env=env)
        evidence["deployed_hashes"] = json.loads(inside["stdout"])
        for name in paths:
            if evidence["deployed_hashes"][name] != sha(workspace / name):
                raise RuntimeError(f"deployed file mismatch: {name}")
        evidence["success"] = True
    except Exception as exc:
        evidence["error"] = str(exc)
    return evidence


def child_config(home):
    # Skip host skill discovery, disable integrations, and start without the
    # user's config, memories, instructions, hooks, MCP servers or plugins.
    text = '''model = "gpt-6-astra"
model_reasoning_effort = "medium"
approval_policy = "never"
sandbox_mode = "workspace-write"
web_search = "disabled"
project_doc_max_bytes = 0
[sandbox_workspace_write]
network_access = true
[features]
apps = false
plugins = false
remote_plugin = false
memories = false
multi_agent = false
multi_agent_v2 = false
browser_use = false
in_app_browser = false
skip_host_skill_discovery = true
'''
    # The current CLI still discovers ~/.agents/skills when the experimental
    # skip flag is set. Disable each discovered host skill explicitly as well.
    host_skills = sorted((Path.home() / ".agents/skills").glob("*/SKILL.md"))
    host_skills += sorted((Path.home() / ".codex/skills").glob("*/SKILL.md"))
    for path in host_skills:
        text += "\n[[skills.config]]\npath = " + json.dumps(str(path)) + "\nenabled = false\n"
    (home / "config.toml").write_text(text)
    return text


def audit_commands(events, arm, wendy):
    commands = [e.get("item", {}).get("command", "") for e in events
                if e.get("type") == "item.completed"
                and e.get("item", {}).get("type") == "command_execution"]
    used = [c for c in commands if str(wendy) in c or "wendy " in c]
    return {"commands": commands, "wendy_commands": used,
            "route_compliant": bool(used) if arm == "with" else not used,
            "mcp_calls": sum(e.get("item", {}).get("type") == "mcp_tool_call"
                             for e in events if e.get("type") == "item.completed")}


def audit_context(path, arm):
    events, invalid = b.read_events(path)
    names, contexts = [], []
    for event in events:
        payload = event.get("payload", {})
        if event.get("type") == "turn_context":
            contexts.append({k: payload.get(k) for k in ("model", "effort", "approval_policy")})
        if event.get("type") == "response_item" and payload.get("type") == "message":
            for block in payload.get("content", []):
                text = block.get("text", "")
                if text.startswith("<skills_instructions>"):
                    names.extend(re.findall(r"^- ([\w:-]+):", text, re.MULTILINE))
    wendy_names = sorted(n for n in names if n.startswith("wendy"))
    expected = ["wendy", "wendy-app-lifecycle"] if arm == "with" else []
    passed = (not invalid and bool(contexts) and wendy_names == expected and
              all(c == {"model": "gpt-6-astra", "effort": "medium", "approval_policy": "never"}
                  for c in contexts))
    return {"skills": names, "wendy_skills": wendy_names, "contexts": contexts,
            "isolation_passed": passed}


def run_attempt(args, task, pair, arm, output, calibration=False):
    ident = f"g1tb-{task}-{pair}-{arm}"
    directory = output / ident
    directory.mkdir()
    private = Path(tempfile.mkdtemp(prefix="g1tb-", dir="/private/tmp"))
    workspace, home = private / "work", private / "codex"
    workspace.mkdir(); home.mkdir()
    (home / "auth.json").symlink_to(Path.home() / ".codex/auth.json")
    child_config(home)
    docker_config = private / "docker"
    docker_config.mkdir()
    b.dump(docker_config / "config.json", {"auths": {},
           "cliPluginsExtraDirs": ["/opt/homebrew/lib/docker/cli-plugins"]})
    state = private / "wendy"
    state.mkdir()
    b.dump(state / "config.json", {"analytics": {"enabled": False},
           "completionInstalled": True, "completionPromptDismissed": True})
    env = {k: v for k, v in os.environ.items() if k in
           ("HOME", "USER", "LOGNAME", "TMPDIR", "SHELL", "LANG", "TERM")}
    env.update(PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin",
               CODEX_HOME=str(home), DOCKER_CONFIG=str(docker_config),
               DOCKER_HOST=args.docker_host, WENDY_CONFIG_DIR=str(state),
               WENDY_ANALYTICS="false", WENDY_SECRET_STORE="file",
               COMPOSE_PROJECT_NAME=ident, OMP_NUM_THREADS="1",
               OPENBLAS_NUM_THREADS="1")
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0)); port = sock.getsockname()[1]
    stage(workspace, args.assets, ident, broken=task == "repair")
    frozen = {str(p.relative_to(workspace)): sha(p) for p in
              [workspace / "service.py", *sorted((workspace / "assets").iterdir())]}
    hashes = {n: frozen["assets/" + n] for n in ("model.mjb", "rollout.npz")}
    row = {"id": ident, "task": task, "pair": pair, "arm": arm,
           "workspace": str(workspace), "port": port, "success": False,
           "status": "setup_error", "metrics": {}, "calibration": calibration}
    initial_others = checked(["docker", "ps", "-aq"], env=env)["stdout"].split()
    row["unrelated_containers_before"] = initial_others
    try:
        if task == "repair" or calibration:
            manifests(workspace, ident, port)
        if task == "repair":
            row["setup"] = checked(["docker", "compose", "up", "--build", "-d"],
                                     cwd=workspace, env=env, timeout=300)
            time.sleep(2)
            row["reproduced_fault"] = checked(["docker", "logs", ident], env=env)
            if "missing.npz" not in row["reproduced_fault"]["stderr"]:
                raise RuntimeError("startup fault did not reproduce")
        context = ("Deploy the supplied headless G1 recorded-expert replay service as a persistent Linux container. "
                   if task == "deploy" else
                   "The supplied G1 replay service is deployed but failing to start. Diagnose the error from the actual container logs, repair the configuration and redeploy it. ")
        context += f"""The Docker runtime is the local ARM64 Linux Colima VM, selected by DOCKER_HOST. This is simulation only. You have shell access and standard Docker tooling. Dependencies are already installed in the common image {BASE}; use that image as your base. No installation or OS provisioning is required.
The service must run from /app with service.py, config.json and assets/ copied into its image. Its internal HTTP port is 8080. Expose it only on host 127.0.0.1:{port}. Use {ident} for the container name, Compose project name (if using Compose), and image repository. Use image tag latest. Leave it running. Verify that /state serves this run's nonce and advancing simulation_time/tick with 43 finite joints.
Keep service.py and all assets byte-for-byte unchanged. You may create deployment files and fix config.json, preserving nonce={ident} and version=v1. Do not use bind mounts for app source or assets. Do not read files outside this workspace except installed tool help/skills and runtime configuration. Do not inspect other workspaces, other containers, benchmark code, results or prior runs. Change or remove only your own {ident} resources. Do not prune, alter host settings, contact physical devices or ask for human input. Finish once deployed and independently checked by you. Give a concise final result.
"""
        if arm == "with":
            dest = workspace / ".agents/skills"
            dest.mkdir(parents=True)
            for name in ("wendy", "wendy-app-lifecycle"):
                shutil.copytree(SKILLS / name, dest / name)
            context += f"Use Wendy CLI for the build/deploy lifecycle, targeting --device docker. The Wendy executable is {args.wendy}. Read the supplied Wendy skills and relevant CLI help as needed. Docker inspection and logs are available for verification and diagnosis.\n"
        else:
            context += "Use standard Docker CLI or Docker Compose for the build/deploy lifecycle. Wendy CLI, Wendy MCP, Wendy skills and Wendy-generated deployment tools are excluded from this run.\n"
        (directory / "prompt.txt").write_text(context)
        b.dump(directory / "frozen.json", frozen)
        shutil.copy2(home / "config.toml", directory / "codex-config.toml")
        argv = [args.codex, "exec", "--json", "--skip-git-repo-check",
                "--add-dir", str(private), "--add-dir", str(Path(args.docker_host.removeprefix("unix://")).parent),
                "--add-dir", str(Path.home() / "Library/Caches/wendy"),
                "--add-dir", str(Path.home() / ".cache/wendy"),
                context]
        if calibration:
            row["agent"] = checked(["docker", "compose", "up", "--build", "-d"],
                                   cwd=workspace, env=env, timeout=300)
            time.sleep(3)
        else:
            print(json.dumps({"started": ident, "workspace": str(workspace)}), flush=True)
            row["agent"] = b.run_process(argv, workspace, directory / "agent", args.timeout, env)
            events, invalid = b.read_events(directory / "agent.stdout")
            row["metrics"] = b.metrics("codex", events)
            row["metrics"]["invalid_json_lines"] = invalid
            row["audit"] = audit_commands(events, arm, args.wendy)
            # Preserve the transcript for auditing the actual bootstrap context.
            sessions = list((home / "sessions").rglob("*.jsonl")) if (home / "sessions").exists() else []
            if len(sessions) == 1:
                shutil.copy2(sessions[0], directory / "session.jsonl")
                row["context_audit"] = audit_context(directory / "session.jsonl", arm)
        row["verification"] = verify(workspace, ident, port, hashes, frozen, env)
        row["success"] = row["verification"]["success"] and row["agent"]["exit_code"] == 0
        if not calibration:
            row["success"] &= (row["metrics"]["agent_completed"] and
                               not row["metrics"]["agent_errors"] and
                               not row["metrics"]["invalid_json_lines"] and
                               row["audit"]["route_compliant"] and
                               row.get("context_audit", {}).get("isolation_passed", False))
        row["status"] = "passed" if row["success"] else "failed"
    except Exception as exc:
        row["error"] = str(exc)
    finally:
        row["logs"] = command(["docker", "logs", "--tail", "30", ident], env=env)
        row["cleanup"] = command(["docker", "rm", "-f", ident], env=env)
        command(["docker", "image", "rm", f"{ident}:latest"], env=env)
        command(["docker", "network", "rm", ident + "_default"], env=env)
        after = checked(["docker", "ps", "-aq"], env=env)["stdout"].split()
        row["cleanup_ok"] = row["cleanup"]["exit_code"] == 0 and set(after) == set(initial_others)
        # Save authored manifests/config but never copy authentication state.
        saved = directory / "authored"
        saved.mkdir()
        for p in workspace.iterdir():
            if p.is_file() and p.name not in ("service.py",):
                shutil.copy2(p, saved / p.name)
        (home / "auth.json").unlink(missing_ok=True)
        b.dump(directory / "result.json", row)
        print(json.dumps({"finished": ident, "success": row["success"],
                          "tokens": row["metrics"].get("tokens"),
                          "cleanup_ok": row["cleanup_ok"]}), flush=True)
    return row


def summarize(rows):
    result = []
    for task in sorted({r["task"] for r in rows}):
        groups = {}
        for arm in ("without", "with"):
            cohort = [r for r in rows if r["task"] == task and r["arm"] == arm]
            complete = bool(cohort) and all(r.get("metrics", {}).get("usage_complete") for r in cohort)
            successes = sum(r["success"] for r in cohort)
            totals = [r["metrics"]["tokens"]["input_tokens"] + r["metrics"]["tokens"]["output_tokens"]
                      for r in cohort] if complete else []
            uncached = [t - r["metrics"]["tokens"]["cached_input_tokens"] for r, t in zip(cohort, totals)]
            groups[arm] = {"attempts": len(cohort), "successes": successes,
                           "usage_complete": complete,
                           "total_tokens": sum(totals) if complete else None,
                           "tokens_per_verified_success": sum(totals) / successes if complete and successes else None,
                           "uncached_plus_output_per_verified_success": sum(uncached) / successes if complete and successes else None,
                           "median_attempt_tokens": statistics.median(totals) if complete else None,
                           "median_success_seconds": statistics.median([r["agent"]["seconds"] for r in cohort if r["success"]]) if successes else None}
        entry = {"task": task, "arms": groups}
        for key in ("tokens_per_verified_success", "uncached_plus_output_per_verified_success"):
            a, z = groups["without"][key], groups["with"][key]
            entry[key + "_reduction_pct"] = 100 * (1-z/a) if a and z is not None else None
        result.append(entry)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--assets", type=Path, required=True)
    parser.add_argument("--codex", default=shutil.which("codex"))
    parser.add_argument("--wendy", default=shutil.which("wendy"))
    parser.add_argument("--docker-host", default="unix://" + str(Path.home() / ".colima/default/docker.sock"))
    parser.add_argument("--timeout", type=int, default=600)
    parser.add_argument("--repetitions", type=int, default=5)
    parser.add_argument("--calibrate", action="store_true")
    parser.add_argument("--execute", action="store_true")
    args = parser.parse_args()
    plan = []
    rng = random.Random(20261016)
    for task in ("deploy", "repair"):
        for pair in range(1, args.repetitions+1):
            arms = ["with", "without"]; rng.shuffle(arms)
            plan.extend({"task": task, "pair": pair, "arm": arm} for arm in arms)
    if not args.execute and not args.calibrate:
        print(json.dumps(plan, indent=2)); return
    args.output.mkdir(parents=True, exist_ok=True)
    if args.calibrate:
        row = run_attempt(args, "deploy", 0, "without", args.output, calibration=True)
        raise SystemExit(0 if row["success"] and row["cleanup_ok"] else 1)
    manifest = {"plan": plan, "source_commit": checked(["git", "rev-parse", "HEAD"], cwd=ROOT)["stdout"].strip(),
                "runner_sha256": sha(__file__), "service_sha256": sha(SERVICE),
                "codex": checked([args.codex, "--version"]), "wendy": checked([args.wendy, "--version"]),
                "codex_sha256": sha(args.codex), "wendy_sha256": sha(args.wendy),
                "dependency_image": checked(["docker", "image", "inspect", BASE]),
                "model": "gpt-6-astra", "effort": "medium",
                "assets": {n: sha(args.assets / n) for n in ("model.mjb", "rollout.npz", "result.json")},
                "skills": {str(p.relative_to(SKILLS)): sha(p) for n in ("wendy", "wendy-app-lifecycle") for p in (SKILLS/n).rglob("*") if p.is_file()},
                "started_at": time.time(), "timeout_seconds": args.timeout}
    b.dump(args.output / "manifest.json", manifest)
    rows = []
    for item in plan:
        row = run_attempt(args, output=args.output, **item)
        rows.append(row)
        with (args.output / "results.jsonl").open("a") as stream:
            stream.write(json.dumps(row) + "\n")
        b.dump(args.output / "summary.json", summarize(rows))
        if not row["cleanup_ok"] or not row.get("metrics", {}).get("usage_complete"):
            print("Stopped: cleanup failed or final token usage unavailable.", flush=True)
            break


if __name__ == "__main__":
    main()
