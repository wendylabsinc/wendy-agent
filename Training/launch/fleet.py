"""One-command fleet launcher for the training templates.

Reads a ``fleet.toml``, resolves device asset ids through the ``wendy``
Command Line Interface (CLI), computes each device's mesh identity and role,
stages a self-contained build context, and drives the CLI to deploy it.

Subcommands: ``up``, ``status``, ``logs``, ``down``, ``render``. Start with
``render``: it prints exactly what ``up`` would do, including the staged
context, any rewritten entitlement, and the full per-device environment,
without executing anything.

Transports: ``mesh`` (the default and the requirement) sends ``MESH_PEERS``
as asset ids resolved over the mesh overlay. ``lan`` exists because the mesh
overlay is temporarily unreliable on fleets with mixed agent versions: it
rewrites the staged ``wendy.json``'s network entitlement to
``{"type": "network", "mode": "host"}`` and renders ``MESH_PEERS`` as
``hostname:port`` entries (excluding the device itself, since hostname
entries cannot be self-skipped by asset id).

Runs on the Python standard library plus ``wendytrain`` only. Every
subprocess call goes through an injectable runner so tests never touch the
real CLI.
"""

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import tomllib
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

from wendytrain.mesh import derive_role, http_get

TRAINING_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_MESH_PORT = 8080
COORDINATOR_ROLES = {"coordinator", "learner"}
TRANSPORTS = {"mesh", "lan"}
STATE_FILE = ".fleet-state.json"
MANIFEST_NAME = "stage-manifest.json"
LAN_ENTITLEMENT = {"type": "network", "mode": "host"}
COPY_IGNORES = shutil.ignore_patterns(
    "__pycache__", "*.pyc", "*.egg-info", "tests", ".pytest_cache", ".venv", ".git"
)


class FleetError(Exception):
    """A configuration or execution problem the user must fix."""


def default_runner(cmd: list[str], env: dict[str, str] | None = None,
                   capture: bool = False) -> str:
    """Run ``cmd`` with ``env`` merged over the current environment.

    With ``capture`` the standard output is returned; otherwise output goes
    straight to the terminal. A non-zero exit raises ``FleetError``.
    """
    merged = dict(os.environ)
    merged.update(env or {})
    result = subprocess.run(cmd, env=merged, capture_output=capture, text=True)
    if result.returncode != 0:
        detail = f": {result.stderr.strip()}" if capture and result.stderr else ""
        raise FleetError(f"command failed ({result.returncode}): {' '.join(cmd)}{detail}")
    return result.stdout if capture else ""


@dataclass
class Device:
    host: str
    asset_id: int | None = None
    role: str | None = None


@dataclass
class FleetConfig:
    template: str
    template_dir: Path
    devices: list[Device]
    transport: str
    env: dict[str, str]
    sweep_params: list[dict] | None
    config_dir: Path

    @property
    def app_id(self) -> str:
        return json.loads((self.template_dir / "wendy.json").read_text())["appId"]

    @property
    def is_sweep(self) -> bool:
        return self.template_dir.name == "sweep"

    @property
    def mesh_port(self) -> int:
        return int(self.env.get("MESH_PORT", DEFAULT_MESH_PORT))


@dataclass
class DevicePlan:
    host: str
    asset_id: int
    role: str
    env: dict[str, str]


@dataclass
class FleetPlan:
    devices: list[DevicePlan]
    warnings: list[str] = field(default_factory=list)


def load_fleet_config(path: str | Path) -> FleetConfig:
    """Parse and validate ``fleet.toml``."""
    path = Path(path)
    if not path.exists():
        raise FleetError(f"no fleet file at {path}")
    data = tomllib.loads(path.read_text())
    fleet_section = data.get("fleet")
    if not fleet_section:
        raise FleetError(f"{path} has no [fleet] section")
    template = fleet_section.get("template")
    if not template:
        raise FleetError("[fleet] template is required")
    template_dir = _resolve_template_dir(template, path.parent)
    transport = fleet_section.get("transport", "mesh")
    if transport not in TRANSPORTS:
        raise FleetError(
            f"unknown transport {transport!r}; expected one of {sorted(TRANSPORTS)}"
        )
    raw_devices = fleet_section.get("devices") or []
    if not raw_devices:
        raise FleetError("[fleet] devices must name at least one device")
    devices = [_parse_device(entry) for entry in raw_devices]
    env = {key: _env_str(value) for key, value in (data.get("env") or {}).items()}
    sweep_params = (data.get("sweep") or {}).get("params")
    config = FleetConfig(
        template=template,
        template_dir=template_dir,
        devices=devices,
        transport=transport,
        env=env,
        sweep_params=sweep_params,
        config_dir=path.parent,
    )
    if config.is_sweep:
        if not sweep_params:
            raise FleetError("the sweep template requires [sweep] params in fleet.toml")
        if len(sweep_params) != len(devices):
            raise FleetError(
                f"[sweep] params has {len(sweep_params)} entries for "
                f"{len(devices)} devices; give each device exactly one"
            )
    return config


def _resolve_template_dir(template: str, config_dir: Path) -> Path:
    named = TRAINING_ROOT / "templates" / template
    candidate = named if named.is_dir() else (config_dir / template).resolve()
    if not candidate.is_dir():
        raise FleetError(
            f"template {template!r} is neither a directory under "
            f"{TRAINING_ROOT / 'templates'} nor a path relative to the fleet file"
        )
    if not (candidate / "wendy.json").exists():
        raise FleetError(f"template directory {candidate} has no wendy.json")
    return candidate


def _parse_device(entry) -> Device:
    if isinstance(entry, str):
        return Device(host=entry)
    if isinstance(entry, dict):
        host = entry.get("host")
        if not host:
            raise FleetError(f"device table {entry!r} needs a host")
        asset_id = entry.get("asset_id")
        if asset_id is not None and not isinstance(asset_id, int):
            raise FleetError(f"asset_id for {host} must be an integer, got {asset_id!r}")
        return Device(host=host, asset_id=asset_id, role=entry.get("role"))
    raise FleetError(f"device entry {entry!r} must be a hostname string or a table")


def _env_str(value) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


# --- asset id resolution ----------------------------------------------------

def resolve_asset_ids(config: FleetConfig, runner=default_runner) -> None:
    """Fill in missing asset ids from the state cache or the CLI.

    Resolved ids are cached in ``.fleet-state.json`` next to ``fleet.toml``
    so repeated invocations do not consult the CLI again.
    """
    state_path = config.config_dir / STATE_FILE
    cache: dict[str, int] = {}
    if state_path.exists():
        cache = json.loads(state_path.read_text()).get("asset_ids", {})
    missing = [d for d in config.devices if d.asset_id is None and d.host not in cache]
    if missing:
        stdout = runner(["wendy", "cloud", "device", "list", "--json"], capture=True)
        cache.update(_parse_device_listing(stdout))
        state_path.write_text(json.dumps({
            "asset_ids": cache,
            "resolved_at": datetime.now(timezone.utc).isoformat(),
        }, indent=2, sort_keys=True) + "\n")
    for device in config.devices:
        if device.asset_id is None:
            if device.host not in cache:
                raise FleetError(
                    f"no asset id found for {device.host}; is the device enrolled? "
                    f"(checked `wendy cloud device list --json` and {state_path})"
                )
            device.asset_id = cache[device.host]


def _parse_device_listing(stdout: str) -> dict[str, int]:
    """Extract hostname to asset id pairs from every device category."""
    listing = json.loads(stdout)
    found: dict[str, int] = {}
    if not isinstance(listing, dict):
        return found
    for category in listing.values():
        if not isinstance(category, list):
            continue
        for entry in category:
            host = entry.get("hostname")
            asset_id = entry.get("assetId")
            if host and isinstance(asset_id, int):
                found[host] = asset_id
    return found


# --- planning ----------------------------------------------------------------

def default_resolver(host: str) -> str:
    """Resolve ``host`` to an Internet Protocol (IP) address, once, here.

    The lan transport must hand containers addresses they can actually use.
    Device hostnames are multicast Domain Name System (mDNS) ``.local`` names,
    which the operator's machine resolves but a slim container image cannot
    ("Name or service not known" on real hardware, from every worker). The
    launcher resolves them while it still can and ships plain addresses.
    """
    import socket

    infos = socket.getaddrinfo(host, None, family=socket.AF_INET)
    return infos[0][4][0]


def plan_fleet(config: FleetConfig, runner=default_runner,
               resolver=None) -> FleetPlan:
    """Resolve ids, derive roles, and compute the per-device environment."""
    if resolver is None:
        resolver = default_resolver  # looked up at call time, so tests can patch it
    resolve_asset_ids(config, runner)
    addresses: dict[str, str] = {}
    if config.transport == "lan":
        addresses = {d.host: resolver(d.host) for d in config.devices}
    peers_raw = ",".join(str(d.asset_id) for d in config.devices)
    roles: dict[str, str] = {}
    for device in config.devices:
        roles[device.host] = derive_role(
            str(device.asset_id), peers_raw, explicit=device.role or "auto"
        )
    coordinators = [h for h, r in roles.items() if r in COORDINATOR_ROLES]
    if len(coordinators) != 1:
        raise FleetError(
            f"the plan needs exactly one coordinator, got {len(coordinators)} "
            f"({', '.join(coordinators) or 'none'}); pin role per device in "
            "fleet.toml until exactly one device is the coordinator"
        )

    plan = FleetPlan(devices=[])
    if config.sweep_params and not config.is_sweep:
        plan.warnings.append(
            "[sweep] params are ignored: only the sweep template reads them"
        )
    for index, device in enumerate(config.devices):
        env = dict(config.env)
        env["MESH_SELF"] = str(device.asset_id)
        if config.transport == "lan":
            env["MESH_PEERS"] = ",".join(
                f"{addresses[peer.host]}:{config.mesh_port}"
                for peer in config.devices if peer.host != device.host
            )
        else:
            env["MESH_PEERS"] = peers_raw
        env["WT_ROLE"] = roles[device.host]
        # Generic topology contract, emitted for every transport: templates
        # prefer their own explicit variables, fall back to these, and only
        # then try numeric derivation. Without them a lan fleet has hostname
        # peers that numeric derivation rightly refuses to interpret.
        ranked = sorted(config.devices, key=lambda d: d.asset_id)
        coordinator_device = next(
            d for d in config.devices if roles[d.host] == "coordinator"
        )
        if config.transport == "lan":
            env["WT_COORDINATOR"] = (
                f"{addresses[coordinator_device.host]}:{config.mesh_port}"
            )
        else:
            env["WT_COORDINATOR"] = (
                f"device-{coordinator_device.asset_id}.cloud.wendy.dev:{config.mesh_port}"
            )
        env["WT_NODE_INDEX"] = str([d.host for d in ranked].index(device.host))
        env["WT_NODE_COUNT"] = str(len(ranked))
        if config.is_sweep:
            env["WT_SWEEP_INDEX"] = str(index)
            env["WT_SWEEP_PARAMS"] = json.dumps(config.sweep_params)
        plan.devices.append(DevicePlan(
            host=device.host, asset_id=device.asset_id,
            role=roles[device.host], env=env,
        ))
    return plan


def deploy_command(host: str, staged_dir: Path) -> list[str]:
    # The CLI's --deploy flag creates a container without starting it;
    # --detach starts it without streaming logs, which is what `up` means.
    return ["wendy", "--device", host, "run", "--detach", "-y",
            "--prefix", str(staged_dir)]


# --- staging -----------------------------------------------------------------

def stage_context(config: FleetConfig, dest: str | Path | None = None) -> Path:
    """Stage a self-contained build context and checksum it.

    The CLI rejects build contexts that reach outside the wendy.json
    directory, so the template cannot reference ``Training/wendytrain`` in
    place. Instead the template directory is copied to ``dest`` (a fresh
    temporary directory when None), ``Training/wendytrain`` is added as
    ``wendytrain/`` (package plus pyproject.toml, tests excluded),
    ``templates/single/cartpole.py`` is added when the template references
    it, the network entitlement is rewritten for the lan transport, and
    ``stage-manifest.json`` records the SHA-256 of every staged file.
    """
    if dest is None:
        dest = Path(tempfile.mkdtemp(prefix=f"wendy-fleet-{config.template_dir.name}-"))
    dest = Path(dest)
    shutil.copytree(config.template_dir, dest, dirs_exist_ok=True, ignore=COPY_IGNORES)

    library_root = TRAINING_ROOT / "wendytrain"
    staged_library = dest / "wendytrain"
    staged_library.mkdir(exist_ok=True)
    shutil.copy2(library_root / "pyproject.toml", staged_library / "pyproject.toml")
    shutil.copytree(
        library_root / "wendytrain", staged_library / "wendytrain",
        dirs_exist_ok=True, ignore=COPY_IGNORES,
    )

    cartpole = TRAINING_ROOT / "templates" / "single" / "cartpole.py"
    if _references_cartpole(config.template_dir) and not (dest / "cartpole.py").exists():
        shutil.copy2(cartpole, dest / "cartpole.py")

    # The sweep template reuses the single template's train loop; in the
    # repository checkout it imports it by file path, but a staged context is
    # flat, so the file is staged as single_train.py (the module name the
    # template imports first).
    single_train = TRAINING_ROOT / "templates" / "single" / "train.py"
    if _references_single_train(config.template_dir) and not (dest / "single_train.py").exists():
        shutil.copy2(single_train, dest / "single_train.py")

    if config.transport == "lan":
        _rewrite_network_entitlements_for_lan(dest / "wendy.json")

    _write_stage_manifest(dest)
    return dest


def _references_single_train(template_dir: Path) -> bool:
    candidates = [template_dir / "Dockerfile", template_dir / "Containerfile"]
    candidates += sorted(template_dir.glob("*.py"))
    return any(
        path.exists() and "single_train" in path.read_text() for path in candidates
    )


def _references_cartpole(template_dir: Path) -> bool:
    candidates = [template_dir / "Dockerfile", template_dir / "Containerfile"]
    candidates += sorted(template_dir.glob("*.py"))
    return any(
        path.exists() and "cartpole" in path.read_text() for path in candidates
    )


def _rewrite_network_entitlements_for_lan(wendy_json: Path) -> None:
    data = json.loads(wendy_json.read_text())
    rewritten = 0

    def rewrite(entitlements: list) -> None:
        nonlocal rewritten
        for i, entitlement in enumerate(entitlements):
            if entitlement.get("type") == "network":
                entitlements[i] = dict(LAN_ENTITLEMENT)
                rewritten += 1

    rewrite(data.get("entitlements") or [])
    for service in (data.get("services") or {}).values():
        rewrite(service.get("entitlements") or [])
    if rewritten == 0:
        raise FleetError(
            f"transport is lan but {wendy_json} has no network entitlement to rewrite"
        )
    wendy_json.write_text(json.dumps(data, indent=4) + "\n")


def _write_stage_manifest(dest: Path) -> None:
    files = {}
    for path in sorted(dest.rglob("*")):
        if not path.is_file() or path.name == MANIFEST_NAME:
            continue
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        files[str(path.relative_to(dest))] = {
            "sha256": digest, "bytes": path.stat().st_size,
        }
    manifest = {
        "created": datetime.now(timezone.utc).isoformat(),
        "files": files,
    }
    (dest / MANIFEST_NAME).write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")


# --- subcommands ---------------------------------------------------------------

def _print_plan(config: FleetConfig, plan: FleetPlan, staged_dir: Path) -> None:
    print(f"template:  {config.template_dir.name} ({config.template_dir})")
    print(f"app id:    {config.app_id}")
    print(f"transport: {config.transport}")
    print(f"staged:    {staged_dir} ({MANIFEST_NAME} written)")
    if config.transport == "lan":
        print(f"network entitlement rewritten to: {json.dumps(LAN_ENTITLEMENT)}")
    for warning in plan.warnings:
        print(f"warning: {warning}")
    for device in plan.devices:
        print()
        print(f"device {device.host} (asset {device.asset_id}, role {device.role})")
        for key in sorted(device.env):
            print(f"  env {key}={device.env[key]}")
        print(f"  command: {' '.join(deploy_command(device.host, staged_dir))}")


def cmd_render(args, runner) -> int:
    config = load_fleet_config(args.config)
    plan = plan_fleet(config, runner)
    staged_dir = stage_context(config, args.stage_dir)
    _print_plan(config, plan, staged_dir)
    print()
    print("nothing was executed; `up` runs the commands above in order")
    return 0


def cmd_up(args, runner) -> int:
    config = load_fleet_config(args.config)
    plan = plan_fleet(config, runner)
    staged_dir = stage_context(config, args.stage_dir)
    _print_plan(config, plan, staged_dir)
    for device in plan.devices:
        print()
        print(f"deploying to {device.host} (role {device.role})", flush=True)
        runner(deploy_command(device.host, staged_dir), env=device.env)
    print()
    print(f"deployed {config.app_id} to {len(plan.devices)} device(s); "
          "check with `status`, follow with `logs`")
    return 0


def cmd_status(args, runner, fetch=http_get) -> int:
    config = load_fleet_config(args.config)
    plan = plan_fleet(config, runner)
    port = config.mesh_port
    print(f"{'host':<24} {'asset':>5}  {'role':<12} status")
    for device in plan.devices:
        summary = "unreachable"
        for route in ("/status", "/healthz"):
            try:
                body = fetch(f"http://{device.host}:{port}{route}",
                             timeout=3.0, retries=1)
                summary = " ".join(body.decode(errors="replace").split())[:120]
                break
            except Exception:
                continue
        print(f"{device.host:<24} {device.asset_id:>5}  {device.role:<12} {summary}")
    return 0


def cmd_logs(args, runner) -> int:
    config = load_fleet_config(args.config)
    plan = plan_fleet(config, runner)
    host = args.host
    if host is None:
        host = next(
            (d.host for d in plan.devices if d.role in COORDINATOR_ROLES),
            plan.devices[0].host,
        )
    runner(["wendy", "--device", host, "device", "logs",
            "--app", config.app_id, "--tail", str(args.tail)])
    return 0


def cmd_down(args, runner) -> int:
    config = load_fleet_config(args.config)
    app_id = config.app_id
    for device in config.devices:
        stdout = runner(["wendy", "--device", device.host,
                         "device", "apps", "list", "--json"], capture=True)
        apps = json.loads(stdout) or []
        # Containers are named appId, or appId_service for multi-service
        # apps. Match exactly those; never touch anything else on the device.
        matches = [
            app["name"] for app in apps
            if app.get("name") == app_id or app.get("name", "").startswith(app_id + "_")
        ]
        if not matches:
            print(f"{device.host}: nothing deployed as {app_id}")
            continue
        for name in matches:
            print(f"{device.host}: stopping {name}")
            runner(["wendy", "--device", device.host, "device", "apps", "stop", name])
    return 0


def main(argv: list[str] | None = None, runner=default_runner, fetch=http_get) -> int:
    parser = argparse.ArgumentParser(
        prog="fleet.py",
        description="Deploy a training template to the devices in fleet.toml.",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    def add(name: str, help_text: str):
        sub = subparsers.add_parser(name, help=help_text)
        sub.add_argument("--config", default="fleet.toml",
                         help="path to fleet.toml (default: ./fleet.toml)")
        return sub

    render = add("render", "print the full plan without executing anything")
    render.add_argument("--stage-dir", default=None,
                        help="stage the build context here instead of a temp dir")
    up = add("up", "stage, then build and deploy to every device in order")
    up.add_argument("--stage-dir", default=None,
                    help="stage the build context here instead of a temp dir")
    add("status", "poll every device's /status or /healthz")
    logs = add("logs", "stream one device's app logs (default: the coordinator)")
    logs.add_argument("--host", default=None, help="device hostname to stream from")
    logs.add_argument("--tail", type=int, default=20,
                      help="replay the last N log batches before streaming")
    add("down", "stop this template's app on every device, and nothing else")

    args = parser.parse_args(argv)
    try:
        if args.command == "render":
            return cmd_render(args, runner)
        if args.command == "up":
            return cmd_up(args, runner)
        if args.command == "status":
            return cmd_status(args, runner, fetch)
        if args.command == "logs":
            return cmd_logs(args, runner)
        if args.command == "down":
            return cmd_down(args, runner)
    except FleetError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    raise AssertionError(f"unhandled command {args.command}")


if __name__ == "__main__":
    sys.exit(main())
