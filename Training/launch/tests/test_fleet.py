"""Tests for the fleet launcher.

Every subprocess call goes through an injectable runner; these tests never
invoke the real ``wendy`` Command Line Interface (CLI). Device hostnames and
asset ids below are fixtures, mirroring the fleet.toml.example devices.
"""

import hashlib
import json
import sys
from pathlib import Path

import pytest

LAUNCH_DIR = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(LAUNCH_DIR))

import fleet  # noqa: E402 (path set up above)

TRAINING_ROOT = LAUNCH_DIR.parent

HOSTS = ["spark-3011.local", "spark-48fd.local", "spark-edeb.local"]
ASSET_IDS = {"spark-3011.local": 334, "spark-48fd.local": 211, "spark-edeb.local": 283}

DEVICE_LIST_JSON = json.dumps(
    {
        "usbDevices": [],
        "lanDevices": [
            {"hostname": "spark-3011.local", "assetId": 334, "isWendyDevice": True},
            {"hostname": "spark-48fd.local", "assetId": 211, "isWendyDevice": True},
            {"hostname": "spark-edeb.local", "assetId": 283, "isWendyDevice": True},
            {"hostname": "unrelated.local", "assetId": 999, "isWendyDevice": True},
        ],
        "bluetoothDevices": None,
        "externalDevices": None,
    }
)


class FakeRunner:
    """Records every command; answers device and app listings from fixtures."""

    def __init__(self, apps_by_host=None):
        self.calls = []
        self.apps_by_host = apps_by_host or {}

    def __call__(self, cmd, env=None, capture=False):
        self.calls.append({"cmd": list(cmd), "env": dict(env or {}), "capture": capture})
        if cmd[:4] == ["wendy", "cloud", "device", "list"]:
            return DEVICE_LIST_JSON
        if "apps" in cmd and "list" in cmd:
            host = cmd[cmd.index("--device") + 1]
            return json.dumps(self.apps_by_host.get(host, []))
        return ""

    def commands(self):
        return [call["cmd"] for call in self.calls]


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_fleet_toml(directory: Path, template: str, transport: str | None = None,
                     devices: list[str] | None = None, extra: str = "") -> Path:
    lines = ["[fleet]", f'template = "{template}"']
    if transport is not None:
        lines.append(f'transport = "{transport}"')
    device_list = ", ".join(f'"{h}"' for h in (devices or HOSTS))
    lines.append(f"devices = [{device_list}]")
    lines.append("")
    lines.append("[env]")
    lines.append('WT_RUN_ID = "demo-1"')
    lines.append('ES_POP = "64"')
    lines.append(extra)
    path = directory / "fleet.toml"
    path.write_text("\n".join(lines) + "\n")
    return path


def make_template(directory: Path, name: str, dockerfile: str = "FROM python:3.12-slim\nCOPY . /app/\n") -> Path:
    template_dir = directory / name
    template_dir.mkdir(parents=True)
    (template_dir / "wendy.json").write_text(json.dumps({
        "appId": f"sh.wendy.training.{name}",
        "platform": "linux",
        "version": "0.1.0",
        "entitlements": [
            {"type": "network", "mode": "mesh", "serviceCIDR": "10.99.0.0/16",
             "ports": [{"host": 8080, "container": 8080}]},
            {"type": "persist", "name": "ckpt", "path": "/data/checkpoints"},
        ],
    }, indent=2))
    (template_dir / "Dockerfile").write_text(dockerfile)
    return template_dir


def load_plan(config_path: Path, runner) -> "fleet.FleetPlan":
    config = fleet.load_fleet_config(config_path)
    return fleet.plan_fleet(config, runner)


BYO_TEMPLATE = str(TRAINING_ROOT / "templates" / "byo")


# --- role assignment -------------------------------------------------------

def test_render_assigns_exactly_one_coordinator(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    plan = load_plan(config_path, FakeRunner())
    coordinators = [d for d in plan.devices if d.role == "coordinator"]
    assert len(coordinators) == 1


def test_coordinator_is_lowest_asset_id(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    plan = load_plan(config_path, FakeRunner())
    coordinator = next(d for d in plan.devices if d.role == "coordinator")
    assert coordinator.host == "spark-48fd.local"
    assert coordinator.asset_id == 211
    workers = [d for d in plan.devices if d.role == "worker"]
    assert sorted(d.asset_id for d in workers) == [283, 334]


def test_explicit_role_overrides_honored(tmp_path):
    config_path = tmp_path / "fleet.toml"
    config_path.write_text(
        "[fleet]\n"
        f'template = "{BYO_TEMPLATE}"\n'
        "devices = [\n"
        '  { host = "spark-3011.local", asset_id = 334, role = "coordinator" },\n'
        '  { host = "spark-48fd.local", asset_id = 211, role = "worker" },\n'
        '  { host = "spark-edeb.local", asset_id = 283, role = "worker" },\n'
        "]\n"
    )
    runner = FakeRunner()
    plan = load_plan(config_path, runner)
    roles = {d.host: d.role for d in plan.devices}
    assert roles["spark-3011.local"] == "coordinator"
    assert roles["spark-48fd.local"] == "worker"
    # All asset ids were explicit, so the CLI is never consulted.
    assert runner.calls == []


def test_conflicting_coordinator_overrides_raise(tmp_path):
    config_path = tmp_path / "fleet.toml"
    config_path.write_text(
        "[fleet]\n"
        f'template = "{BYO_TEMPLATE}"\n'
        "devices = [\n"
        '  { host = "spark-3011.local", asset_id = 334, role = "coordinator" },\n'
        '  { host = "spark-48fd.local", asset_id = 211 },\n'
        "]\n"
    )
    with pytest.raises(fleet.FleetError):
        load_plan(config_path, FakeRunner())


# --- peers and environment -------------------------------------------------

def test_mesh_peer_lists_are_complete_including_self(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    plan = load_plan(config_path, FakeRunner())
    for device in plan.devices:
        peers = device.env["MESH_PEERS"].split(",")
        assert peers == ["334", "211", "283"]
        assert device.env["MESH_SELF"] == str(ASSET_IDS[device.host])
        assert device.env["WT_ROLE"] == device.role


def test_env_section_forwarded_to_every_device(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    plan = load_plan(config_path, FakeRunner())
    for device in plan.devices:
        assert device.env["WT_RUN_ID"] == "demo-1"
        assert device.env["ES_POP"] == "64"


def test_asset_ids_cached_in_fleet_state(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    first = FakeRunner()
    load_plan(config_path, first)
    assert ["wendy", "cloud", "device", "list", "--json"] in first.commands()
    state = json.loads((tmp_path / ".fleet-state.json").read_text())
    assert state["asset_ids"]["spark-3011.local"] == 334
    second = FakeRunner()
    load_plan(config_path, second)
    assert second.calls == []


# --- sweep -----------------------------------------------------------------

SWEEP_EXTRA = "\n[sweep]\nparams = [{ seed = 1 }, { seed = 2 }, { seed = 3 }]\n"


def test_sweep_vars_for_sweep_template(tmp_path):
    template_dir = make_template(tmp_path, "sweep")
    config_path = write_fleet_toml(tmp_path, str(template_dir), extra=SWEEP_EXTRA)
    plan = load_plan(config_path, FakeRunner())
    indices = sorted(int(d.env["WT_SWEEP_INDEX"]) for d in plan.devices)
    assert indices == [0, 1, 2]
    for device in plan.devices:
        params = json.loads(device.env["WT_SWEEP_PARAMS"])
        assert params == [{"seed": 1}, {"seed": 2}, {"seed": 3}]


def test_no_sweep_vars_for_non_sweep_template(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE, extra=SWEEP_EXTRA)
    plan = load_plan(config_path, FakeRunner())
    for device in plan.devices:
        assert "WT_SWEEP_INDEX" not in device.env
        assert "WT_SWEEP_PARAMS" not in device.env


def test_sweep_template_requires_matching_param_count(tmp_path):
    template_dir = make_template(tmp_path, "sweep")
    extra = "\n[sweep]\nparams = [{ seed = 1 }]\n"
    config_path = write_fleet_toml(tmp_path, str(template_dir), extra=extra)
    with pytest.raises(fleet.FleetError):
        load_plan(config_path, FakeRunner())


# --- staged build context --------------------------------------------------

def test_staged_context_checksums_match_source_tree(tmp_path):
    stage = tmp_path / "stage"
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    config = fleet.load_fleet_config(config_path)
    fleet.stage_context(config, stage)

    assert (stage / "wendy.json").exists()
    assert (stage / "main.py").exists()
    assert (stage / "wendytrain" / "pyproject.toml").exists()

    source_pkg = TRAINING_ROOT / "wendytrain" / "wendytrain"
    staged_pkg = stage / "wendytrain" / "wendytrain"
    source_files = sorted(p.name for p in source_pkg.glob("*.py"))
    staged_files = sorted(p.name for p in staged_pkg.glob("*.py"))
    assert staged_files == source_files
    for name in source_files:
        assert sha256(staged_pkg / name) == sha256(source_pkg / name)

    manifest = json.loads((stage / "stage-manifest.json").read_text())
    staged = sorted(
        str(p.relative_to(stage))
        for p in stage.rglob("*")
        if p.is_file() and p.name != "stage-manifest.json"
    )
    assert sorted(manifest["files"]) == staged
    for rel, entry in manifest["files"].items():
        assert entry["sha256"] == sha256(stage / rel), rel


def test_staged_context_excludes_tests_and_egg_info(tmp_path):
    stage = tmp_path / "stage"
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    config = fleet.load_fleet_config(config_path)
    fleet.stage_context(config, stage)
    staged = [str(p.relative_to(stage)) for p in stage.rglob("*")]
    assert not any("egg-info" in p for p in staged)
    assert not any(p == "wendytrain/tests" or p.startswith("wendytrain/tests/") for p in staged)
    assert not any("__pycache__" in p for p in staged)


def test_cartpole_staged_when_referenced(tmp_path):
    template_dir = make_template(
        tmp_path, "es-like",
        dockerfile="FROM python:3.12-slim\nCOPY . /app/\n# uses cartpole.py\n",
    )
    config_path = write_fleet_toml(tmp_path, str(template_dir))
    config = fleet.load_fleet_config(config_path)
    stage = tmp_path / "stage"
    fleet.stage_context(config, stage)
    staged_cartpole = stage / "cartpole.py"
    source_cartpole = TRAINING_ROOT / "templates" / "single" / "cartpole.py"
    assert staged_cartpole.exists()
    assert sha256(staged_cartpole) == sha256(source_cartpole)


def test_cartpole_not_staged_when_unreferenced(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    config = fleet.load_fleet_config(config_path)
    stage = tmp_path / "stage"
    fleet.stage_context(config, stage)
    assert not (stage / "cartpole.py").exists()


# --- lan transport ---------------------------------------------------------

def test_lan_transport_rewrites_network_entitlement(tmp_path):
    stage = tmp_path / "stage"
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE, transport="lan")
    config = fleet.load_fleet_config(config_path)
    fleet.stage_context(config, stage)
    staged = json.loads((stage / "wendy.json").read_text())
    network = [e for e in staged["entitlements"] if e["type"] == "network"]
    assert network == [{"type": "network", "mode": "host"}]
    persist = [e for e in staged["entitlements"] if e["type"] == "persist"]
    assert persist, "non-network entitlements must survive the rewrite"
    # The source template keeps its mesh entitlement untouched.
    source = json.loads((Path(BYO_TEMPLATE) / "wendy.json").read_text())
    assert any(e.get("mode") == "mesh" for e in source["entitlements"])


def test_lan_transport_renders_hostname_peers_excluding_self(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE, transport="lan")
    plan = load_plan(config_path, FakeRunner())
    by_host = {d.host: d for d in plan.devices}
    peers = by_host["spark-3011.local"].env["MESH_PEERS"].split(",")
    assert peers == ["spark-48fd.local:8080", "spark-edeb.local:8080"]
    assert by_host["spark-3011.local"].env["MESH_SELF"] == "334"
    # Roles still derive from asset ids even though peers are hostnames.
    assert by_host["spark-48fd.local"].role == "coordinator"


def test_mesh_is_the_default_transport(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    config = fleet.load_fleet_config(config_path)
    assert config.transport == "mesh"


def test_unknown_transport_raises(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE, transport="carrier-pigeon")
    with pytest.raises(fleet.FleetError):
        fleet.load_fleet_config(config_path)


# --- render subcommand -----------------------------------------------------

def test_render_prints_commands_without_executing(tmp_path, capsys):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    runner = FakeRunner()
    exit_code = fleet.main(
        ["render", "--config", str(config_path), "--stage-dir", str(tmp_path / "stage")],
        runner=runner,
    )
    assert exit_code == 0
    out = capsys.readouterr().out
    for host in HOSTS:
        assert f"wendy --device {host} run --detach -y --prefix" in out
    # Only the device listing ran; no deploy, stop, or logs command.
    assert runner.commands() == [["wendy", "cloud", "device", "list", "--json"]]


def test_render_shows_lan_entitlement_and_env(tmp_path, capsys):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE, transport="lan")
    exit_code = fleet.main(
        ["render", "--config", str(config_path), "--stage-dir", str(tmp_path / "stage")],
        runner=FakeRunner(),
    )
    assert exit_code == 0
    out = capsys.readouterr().out
    assert '{"type": "network", "mode": "host"}' in out
    assert "MESH_PEERS=spark-48fd.local:8080,spark-edeb.local:8080" in out
    assert "MESH_SELF=334" in out
    assert "WT_RUN_ID=demo-1" in out


# --- up and down -----------------------------------------------------------

def test_up_deploys_each_device_with_computed_env(tmp_path):
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    runner = FakeRunner()
    exit_code = fleet.main(
        ["up", "--config", str(config_path), "--stage-dir", str(tmp_path / "stage")],
        runner=runner,
    )
    assert exit_code == 0
    deploys = [c for c in runner.calls if c["cmd"][:2] == ["wendy", "--device"]]
    assert [c["cmd"][2] for c in deploys] == HOSTS
    for call in deploys:
        assert call["cmd"][3:6] == ["run", "--detach", "-y"]
        assert call["env"]["MESH_SELF"] == str(ASSET_IDS[call["cmd"][2]])
        assert call["env"]["WT_RUN_ID"] == "demo-1"


def test_down_stops_only_matching_app_ids(tmp_path):
    apps = {
        host: [
            {"name": "go2-artifacts-export", "version": "0.1.0", "runningState": "RUNNING"},
            {"name": "sh.wendy.training.byo", "version": "0.1.0", "runningState": "RUNNING"},
            {"name": "sh.wendy.training.byo_node", "version": "0.1.0", "runningState": "RUNNING"},
            {"name": "sh.wendy.training.byoish", "version": "0.1.0", "runningState": "RUNNING"},
        ]
        for host in HOSTS
    }
    config_path = write_fleet_toml(tmp_path, BYO_TEMPLATE)
    runner = FakeRunner(apps_by_host=apps)
    exit_code = fleet.main(["down", "--config", str(config_path)], runner=runner)
    assert exit_code == 0
    stops = [c for c in runner.commands() if "stop" in c]
    stopped = sorted(c[c.index("stop") + 1] for c in stops)
    assert stopped == sorted(
        ["sh.wendy.training.byo", "sh.wendy.training.byo_node"] * len(HOSTS)
    )
    assert all("go2-artifacts-export" not in c for c in stops)
    assert all("sh.wendy.training.byoish" not in " ".join(c) for c in stops)


def test_sweep_staging_includes_single_train(tmp_path):
    """The real sweep template imports single_train; a staged context must carry it.

    Found at merge time: the sweep template reuses the single template's train
    loop as the module single_train, but the staging step only knew about
    cartpole.py. A staged sweep image would have crashed on import.
    """

    config_path = write_fleet_toml(
        tmp_path, str(TRAINING_ROOT / "templates" / "sweep"), extra=SWEEP_EXTRA
    )
    config = fleet.load_fleet_config(config_path)
    stage = tmp_path / "stage"
    fleet.stage_context(config, stage)
    staged = stage / "single_train.py"
    source = TRAINING_ROOT / "templates" / "single" / "train.py"
    assert staged.exists()
    assert sha256(staged) == sha256(source)
    assert (stage / "cartpole.py").exists()
    manifest = json.loads((stage / "stage-manifest.json").read_text())
    assert "single_train.py" in manifest["files"]
