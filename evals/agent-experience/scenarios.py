"""Short tool-choice scenarios: loading, readiness, the shared run VM or spare board, and one attempt.

An attempt runs the agent against the `wendy` shim (eval_wendy.py): MCP calls go through the recording
proxy and shell `wendy` calls through the CLI wrapper, so risky calls are canned and every call is
logged. Python standard library only.
"""
import contextlib
import copy
import json
import math
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import uuid

import benchmark as b
from canned import load_capture
from grading import grade, validate_expect
from mcp_client import StdioMCP
from retry_guard import RetryGuard
import robot_diagnostics
import robot_fixtures
from tool_trace import build_trace, read_jsonl

HERE = Path(__file__).resolve().parent
CANNED = HERE / "canned"
CLI_SAFE = HERE / "cli_safe.json"
GROUPS = ("core", "setup", "simulator", "hardware", "robotics", "observability", "cloud")
ENVIRONMENTS = ("simulator", "physical", "cloud")
VM_PROFILES = ("generic", "go2", "g1")
REQUIRED = ("id", "group", "environments", "prompt", "expect")
DEFAULTS = {"timeout_seconds": 180, "max_tool_calls": 20, "canned": [], "captures": [], "allow_real": [],
            "requires": [], "files": {}, "vm_profile": "generic"}
OPTIONAL = frozenset({"canned_set", "notes"})
# An attempt that ends this way never counts as a success, whatever its tool choice.
NOT_SUCCESS = frozenset({"mcp_contaminated", "mcp_unverified", "host_state_changed", "interrupted"})
_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")

# The agent's hardware discoverer (go/internal/agent/hardware/discoverer.go, ListHardwareCapabilities)
# emits the categories gpu, usb, i2c, spi, gpio, camera, audio, network and storage. Only camera maps
# directly; the other names here are explicit synonyms in case a category is added under them.
CATEGORY_TAGS = {"camera": "camera", "cameras": "camera",
                 "wifi": "wifi", "wi-fi": "wifi", "wlan": "wifi", "wireless": "wifi",
                 "bluetooth": "bluetooth", "bt": "bluetooth"}


def _check(condition, message):
    if not condition:
        raise ValueError(message)


def _names(value, key):
    _check(isinstance(value, list) and all(isinstance(v, str) and v for v in value),
           f"{key} must be a list of names")


def _relative(name):
    parts = name.split("/") if isinstance(name, str) else [""]
    _check(not name.startswith("/") and all(p not in ("", ".", "..") for p in parts),
           f"files names must be plain relative paths: {name!r}")


def _scenario(item):
    _check(isinstance(item, dict), "a scenario must be an object")
    unknown = set(item) - set(REQUIRED) - set(DEFAULTS) - OPTIONAL
    _check(not unknown, f"unknown keys {sorted(unknown)}")
    missing = [key for key in REQUIRED if key not in item]
    _check(not missing, "missing " + ", ".join(missing))
    scenario = copy.deepcopy(DEFAULTS)
    scenario.update(copy.deepcopy(item))
    scenario.setdefault("canned_set", scenario["id"])
    for key in ("id", "canned_set"):
        _check(isinstance(scenario[key], str) and _NAME.match(scenario[key]), f"{key} must be a plain name")
    _check(scenario["group"] in GROUPS, "group must be one of " + ", ".join(GROUPS))
    environments = scenario["environments"]
    _check(isinstance(environments, list) and environments and all(e in ENVIRONMENTS for e in environments),
           "environments must be a non-empty subset of " + ", ".join(ENVIRONMENTS))
    _check(isinstance(scenario["prompt"], str) and scenario["prompt"].strip(), "prompt must be text")
    validate_expect(scenario["expect"])
    timeout = scenario["timeout_seconds"]
    _check(not isinstance(timeout, bool) and isinstance(timeout, (int, float)) and math.isfinite(timeout)
           and timeout > 0, "timeout_seconds must be a positive number")
    cap = scenario["max_tool_calls"]
    _check(not isinstance(cap, bool) and isinstance(cap, int) and cap > 0, "max_tool_calls must be a positive integer")
    for key in ("canned", "captures", "allow_real", "requires"):
        _names(scenario[key], key)
    _check(isinstance(scenario["files"], dict) and all(isinstance(v, str) for v in scenario["files"].values()),
           "files must map names to text")
    for name in scenario["files"]:
        _relative(name)
    _check(scenario["vm_profile"] in VM_PROFILES, "vm_profile must be one of " + ", ".join(VM_PROFILES))
    return scenario


def load_scenarios(path):
    data = json.loads(Path(path).read_text())
    _check(isinstance(data, list), "the scenario file must hold a list of scenarios")
    scenarios, seen = [], set()
    for index, item in enumerate(data):
        label = item.get("id", f"#{index}") if isinstance(item, dict) else f"#{index}"
        try:
            scenario = _scenario(item)
        except ValueError as error:
            raise ValueError(f"scenario {label}: {error}") from None
        _check(scenario["id"] not in seen, f"duplicate scenario id {scenario['id']!r}")
        seen.add(scenario["id"])
        scenarios.append(scenario)
    return scenarios


def scenario_readiness(scenario, target, canned_dir):
    """None when the scenario can run on target, else the reason it is unconfigured.

    An invalid capture raises ValueError here, before a run, rather than failing an attempt.
    """
    required = set(scenario["requires"])
    if required:
        capabilities = target.get("capabilities", [])  # None: not read yet, as for a --device board
        if capabilities is None:
            return "capabilities not checked (add --execute)"
        missing = sorted(required - set(capabilities))
        if missing:
            return "target capabilities required: " + ", ".join(missing)
    uncaptured = []
    for tool in scenario["captures"]:
        try:
            capture = load_capture(canned_dir, scenario["canned_set"], tool)
            _check(capture is None or isinstance(capture, dict), "a capture must be an object")
            replies = None if capture is None else capture.get("replies")
            _check(replies is None or (isinstance(replies, list) and all(isinstance(r, dict) for r in replies)),
                   "replies must be a list of objects")
        except ValueError as error:
            raise ValueError(f"scenario {scenario['id']}: invalid capture for {tool}: {error!r}") from None
        if not replies:
            uncaptured.append(tool)
    if uncaptured:
        return "canned replies not captured: " + ", ".join(uncaptured)
    return None


def scenario_coverage(scenarios, targets, selected, selected_targets, canned_dir):
    coverage = []
    for scenario in scenarios:
        if scenario["id"] not in selected:
            continue
        matching = [name for name in selected_targets if targets[name]["kind"] in scenario["environments"]]
        if not matching:
            coverage.append({"task": scenario["id"], "kind": "scenario", "target": None,
                             "status": "unconfigured", "reason": "no matching target configured"})
        for name in matching:
            reason = scenario_readiness(scenario, targets[name], canned_dir)
            coverage.append({"task": scenario["id"], "kind": "scenario", "target": name,
                             "status": "unconfigured" if reason else "ready", "reason": reason})
    return coverage


def board_target(selector):
    return {"kind": "cloud" if selector.startswith("cloud://") else "physical", "device": selector,
            "capabilities": None, "fixtures": {}, "cache_state": "spare board named with --device"}


def _capability_payload(result):
    if not isinstance(result, dict):
        return {}
    if isinstance(result.get("structuredContent"), dict):
        return result["structuredContent"]
    if "capabilities" in result:
        return result
    for item in result.get("content") or []:
        if isinstance(item, dict) and item.get("type") == "text":
            try:
                parsed = json.loads(item.get("text") or "")
            except (TypeError, ValueError):
                continue
            if isinstance(parsed, dict):
                return parsed
    return {}


def detect_capabilities(result):
    """Map a hardware_capabilities result to the tags scenarios require: wifi, bluetooth, camera.

    The agent reports no wifi or bluetooth category, so those come from evidence inside other
    categories: a network interface named wl* (the kernel's wlanN and systemd's wlp*/wlx* names
    for WLAN), a USB device whose product says Bluetooth, or a PipeWire bluez_* audio node.
    """
    if not isinstance(result, dict) or result.get("isError") is True:
        return []
    capabilities = _capability_payload(result).get("capabilities")
    tags = set()
    for cap in capabilities if isinstance(capabilities, list) else []:
        if not isinstance(cap, dict):
            continue
        category = str(cap.get("category") or "").strip().lower()
        path, description = str(cap.get("device_path") or ""), str(cap.get("description") or "")
        interface = os.path.basename(path) or (description.split() or [""])[0]
        if category in CATEGORY_TAGS:
            tags.add(CATEGORY_TAGS[category])
        elif category == "network" and interface.startswith("wl"):
            tags.add("wifi")
        elif category == "usb" and "bluetooth" in description.lower():
            tags.add("bluetooth")
        elif category == "audio" and path.startswith("bluez_"):
            tags.add("bluetooth")
    return sorted(tags)


def private_env(state):
    return dict(os.environ, WENDY_ANALYTICS="false", WENDY_CONFIG_DIR=str(state), WENDY_SECRET_STORE="file")


def detect_board(wendy, target, timeout=120):
    """Read a board's capabilities in a throwaway private state. Returns (tags or None, evidence)."""
    state = b.isolated_device_state(target)
    evidence = {"device": target["device"]}
    try:
        with StdioMCP([wendy, "mcp", "serve"], env=private_env(state), timeout=timeout) as mcp:
            mcp.initialize()
            evidence["device_connect"] = mcp.call("device_connect", {"device": target["device"]})
            if evidence["device_connect"].get("isError"):
                return None, evidence
            evidence["hardware_capabilities"] = mcp.call("hardware_capabilities", {})
        if evidence["hardware_capabilities"].get("isError"):
            return None, evidence
        evidence["capabilities"] = detect_capabilities(evidence["hardware_capabilities"])
        return evidence["capabilities"], evidence
    except (OSError, RuntimeError, TimeoutError, ValueError) as error:
        evidence["error"] = repr(error)
        return None, evidence
    finally:
        shutil.rmtree(state, ignore_errors=True)


class ToolCapGuard:
    """A run_process progress guard: stop once the proxy and wrapper logs hold `limit` calls."""

    def __init__(self, proxy_log, cli_log, limit):
        if isinstance(limit, bool) or not isinstance(limit, int) or limit < 1:
            raise ValueError("max_tool_calls must be a positive integer")
        self.paths, self.limit = (Path(proxy_log), Path(cli_log)), limit

    def __call__(self):
        calls = 0
        for path in self.paths:
            try:
                calls += len(read_jsonl(path))
            except OSError:
                pass  # not written yet
        return {"kind": "tool_call_cap", "calls": calls, "limit": self.limit} if calls >= self.limit else None


def first_reason(*guards):
    def check():
        for guard in guards:
            reason = guard()
            if reason:
                return reason
        return None
    return check


def _run_vm_dir(output, profile):
    directory = Path(output) / f"run-vm-{profile}"
    directory.mkdir(parents=True, exist_ok=True)
    return directory


def _robot_vm(wendy, name, profile, directory, env):
    # The same commands and readiness check as robot_diagnostics.prepare, with the ARM64 agent the
    # robot tasks in tasks.json install.
    agent_binary = str(b.ROOT / "go/bin/wendy-agent-linux-arm64")
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    stages = {}
    for phase, argv, timeout in robot_diagnostics.robot_vm_commands(wendy, name, profile, agent_binary, port):
        stages[phase] = b.run_process(argv, directory, directory / phase, timeout, env)
        if stages[phase]["exit_code"] != 0:
            return stages, False

    def cli(args, *argv, timeout=60):
        done = subprocess.run([args.wendy, *argv], env=env, stdin=subprocess.DEVNULL,
                              capture_output=True, text=True, timeout=timeout)
        if done.returncode:
            raise RuntimeError(done.stderr + done.stdout)
        return done.stdout

    args = SimpleNamespace(wendy=wendy, vm_name=name, task=f"create-{profile}-simulator")
    try:
        with (directory / "vm-robot-verify.stdout").open("w") as out, contextlib.redirect_stdout(out):
            robot_fixtures.verify(args, cli)
    except (RuntimeError, ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        stages["vm-robot-verify"] = {"exit_code": 1, "error": str(error)}
        return stages, False
    stages["vm-robot-verify"] = {"exit_code": 0}
    return stages, True


def prepare_run_vm(config, wendy, output, profile):
    """Create the run's shared VM for one profile, in its own private state (never the host's ~/.wendy)."""
    if profile not in VM_PROFILES:
        raise ValueError(f"unknown vm_profile {profile!r}")
    state = b.isolated_device_state({"kind": "simulator"})
    name = "wendy-eval-" + uuid.uuid4().hex[:12]
    directory, env = _run_vm_dir(output, profile), private_env(state)
    if profile == "generic":
        stages, ready = b.fixture_vm(config, {"wendy": wendy, "vm_name": name}, directory, directory, env)
    else:
        stages, ready = _robot_vm(wendy, name, profile, directory, env)
    run_vm = {"state": state, "name": name, "device": "vm:" + name, "profile": profile,
              "stages": stages, "ready": ready}
    b.dump(directory / "prepare.json", {**run_vm, "state": str(state)})
    return run_vm


def remove_run_vm(run_vm, wendy, output):
    directory, state = _run_vm_dir(output, run_vm.get("profile", "generic")), Path(run_vm["state"])
    try:
        result = b.remove_fixture_vm({"wendy": wendy, "vm_name": run_vm["name"]}, directory, directory,
                                     private_env(state))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        result = {"exit_code": None, "error": repr(error)}
    ok = result.get("exit_code") == 0
    if state.exists():
        b.discard_device_state(state, ok)
    if not ok:
        result["device_state_retained"] = str(state)
    return result


def attempt_state(target, run_vm):
    """A fresh private Wendy state; with a run VM, its VM store is the shared one, by symlink."""
    state = b.isolated_device_state(target)
    if run_vm is not None:
        (state / "vms").symlink_to(Path(run_vm["state"]) / "vms", target_is_directory=True)
    return state


def vm_snapshot(wendy, device, env):
    done = subprocess.run([wendy, "--json", "--device", device, "device", "apps", "list"], env=env,
                          stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)
    if done.returncode != 0:
        raise RuntimeError(f"listing apps on {device} failed: {(done.stderr or done.stdout).strip()}")
    apps = json.loads(done.stdout)
    if not isinstance(apps, list):
        raise RuntimeError(f"unexpected app list from {device}")
    entries = [{"name": app.get("name"), "version": app.get("version"), "state": app.get("runningState")}
               for app in apps if isinstance(app, dict)]
    return sorted(entries, key=lambda entry: json.dumps(entry, sort_keys=True))


def _target_snapshot(wendy, device, target, run_vm):
    # The runner's own state, never the attempt's, so the check leaves nothing for the agent to find.
    if run_vm is not None:
        return vm_snapshot(wendy, device, private_env(run_vm["state"]))
    state = b.isolated_device_state(target)
    try:
        return vm_snapshot(wendy, device, private_env(state))
    finally:
        shutil.rmtree(state, ignore_errors=True)


def _tmp_root():
    return "/private/tmp" if sys.platform == "darwin" else "/tmp"


def scenario_trial(config, agent_name, target_name, scenario, output, real_wendy, shim, run_vm, tools_path,
                   canned_dir, treatment_root=None):
    treatment_root = treatment_root or b.ROOT
    agent, target = config["agents"][agent_name], config["targets"][target_name]
    if (target["kind"] == "simulator") != (run_vm is not None):
        raise ValueError("simulator scenarios run on the shared run VM, and board scenarios never do")
    adapter = agent["adapter"]
    real_wendy, shim = os.path.abspath(real_wendy), os.path.abspath(shim)
    ident = uuid.uuid4().hex[:12]
    directory = Path(output) / f"{agent_name}-{target_name}-{scenario['id']}-{ident}"
    directory.mkdir()
    workspace = Path(tempfile.mkdtemp(prefix="wendy-eval-"))
    # The logs start outside the results tree: Codex's sandboxed shell may write only to its
    # workspace, its added dirs and /tmp, and the wrapper logs from inside that shell.
    logs = Path(tempfile.mkdtemp(prefix="we-log-", dir=_tmp_root()))
    proxy_log, cli_log = logs / "proxy.jsonl", logs / "cli.jsonl"
    proxy_log.touch()
    cli_log.touch()  # empty logs mean no calls, so transcript MCP and CLI entries never stand in for them
    state = attempt_state(target, run_vm)
    device = run_vm["device"] if run_vm is not None else target["device"]
    scenario_config = directory / "scenario.json"
    variables = {"root": b.ROOT, "here": HERE, "treatment_root": treatment_root, "plugin_dir": directory / "plugin",
                 "workspace": workspace, "wendy": shim, "wendy_home": state,
                 "wendy_vm_home": run_vm["state"] if run_vm is not None else state,
                 "scenario_config": scenario_config,
                 "wendy_build_cache": Path.home() / ".cache/wendy",
                 "docker_buildx_cache": Path(os.environ.get("DOCKER_CONFIG", Path.home() / ".docker")) / "buildx",
                 "wendy_cache": (Path.home() / "Library/Caches/wendy" if sys.platform == "darwin" else
                                 Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "wendy"),
                 "device": device, "run_id": ident, "app_id": f"dev.wendy.eval.{ident}",
                 "vm_name": run_vm["name"] if run_vm is not None else f"wendy-eval-{ident}",
                 "port": config.get("port", 18765), "model": agent["model"],
                 "evidence_dir": directory, "agent_events": directory / "agent.stdout",
                 "target_config": directory / "target.json"}
    b.dump(directory / "target.json", target)
    prompt = b.expand(scenario["prompt"], variables)
    prompt += f"\nThe Wendy executable for this environment is {shim}. Use that absolute path for Wendy CLI commands."
    if config.get("environment_context"):
        prompt += "\n" + b.expand(config["environment_context"], variables)
    prompt += (f"\nWendy device/cloud configuration and its VM store are isolated at {state} "
               "through WENDY_CONFIG_DIR. Preserve that environment setting. Do not change the host's ~/.wendy configuration or identity pins.")
    variables["prompt"] = prompt
    row = {"id": ident, "kind": "scenario", "agent": agent_name, "target": target_name, "task": scenario["id"],
           "group": scenario["group"], "target_config": target, "model_requested": agent["model"],
           "workspace": str(workspace), "device_state_isolated": True, "device_selector": device,
           "agent_timeout_seconds": b.agent_timeout(config, scenario), "max_tool_calls": scenario["max_tool_calls"],
           "human_messages_after_start": 0, "human_approvals": 0, "success": False,
           "status": "setup_error", "task_seconds": 0, "metrics": {}, "stages": {}, "tool_choice": None}
    b.dump(directory / "task.json", {**scenario, "prompt": prompt})
    (directory / "prompt.txt").write_text(prompt + "\n")
    expect = scenario["expect"]
    b.dump(scenario_config, {
        "real_wendy": real_wendy, "tools": str(tools_path), "canned_dir": str(canned_dir),
        "canned_set": scenario["canned_set"], "canned": scenario["canned"], "allow_real": scenario["allow_real"],
        "block": [m for m in expect.get("right", []) + expect.get("wrong", []) if m.get("block") is True],
        "context": {"device": device}, "proxy_log": str(proxy_log), "cli_log": str(cli_log),
        "cli_safe": str(CLI_SAFE), "shim": shim})
    env = private_env(state)
    env["WENDY_EVAL_SCENARIO"] = str(scenario_config)
    env["PATH"] = str(Path(shim).parent) + os.pathsep + env.get("PATH", "")
    before = None
    try:
        try:
            before = _target_snapshot(real_wendy, device, target, run_vm)
        except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
            row["stages"]["reset"] = {"exit_code": 1, "error": f"reading the target before the attempt: {error}"}
            return row
        for name, content in scenario["files"].items():
            path = workspace / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(b.expand(content, variables))
        b.stage_context(workspace, adapter, shim, state, treatment_root,
                        server_env={"WENDY_EVAL_SCENARIO": str(scenario_config)})
        if adapter == "claude":
            b.stage_plugin(treatment_root, directory / "plugin")
        guard = first_reason(RetryGuard(directory / "agent.stdout", config.get("syntax_error_limit", 3)),
                             ToolCapGuard(proxy_log, cli_log, scenario["max_tool_calls"]))
        result = b.run_process(b.expand(agent["command"], variables), workspace, directory / "agent",
                               row["agent_timeout_seconds"], env, b.host_pin_guard(), guard)
        row["stages"]["agent"] = result
        row["metrics"], events = b.agent_metrics(adapter, directory / "agent.stdout", result,
                                                 agent.get("prices_per_million"))
        trace = build_trace(adapter, events, proxy_records=read_jsonl(proxy_log), cli_records=read_jsonl(cli_log))
        row["metrics"]["tool_trace"] = trace
        row["task_seconds"] = result["seconds"]
        row["tool_choice"] = grade(trace, expect, row["metrics"]["final_text"], row["metrics"]["agent_completed"],
                                   group=scenario["group"], adapter=adapter)
        row["status"] = b.mcp_status(adapter, row["metrics"]) or b.attempt_status(result)
        row["success"] = row["tool_choice"]["outcome"] in ("right", "asked") and row["status"] not in NOT_SUCCESS
        if row["success"]:
            row["status"] = "passed"
    finally:
        if before is not None:
            # The shared VM or board must be back in its prepared state; anything else stops the run.
            try:
                after = _target_snapshot(real_wendy, device, target, run_vm)
                row["stages"]["reset"] = {"exit_code": 0 if after == before else 1, "before": before, "after": after}
            except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
                row["stages"]["reset"] = {"exit_code": 1, "before": before,
                                          "error": f"reading the target after the attempt: {error}"}
        row["cleanup_ok"] = row["stages"].get("reset", {}).get("exit_code") == 0
        b.discard_device_state(state, row["cleanup_ok"])  # unlinks the shared store; the VM stays
        if not row["cleanup_ok"]:
            row["device_state_retained"] = str(state)
        for path in (proxy_log, cli_log):
            if path.exists():
                shutil.move(str(path), str(directory / path.name))
        shutil.rmtree(logs, ignore_errors=True)
        b.dump(directory / "result.json", row)
        # Keep the workspace for diagnosis; it contains only this attempt's files.
    return row
