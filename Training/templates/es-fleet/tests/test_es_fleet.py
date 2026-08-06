"""Tests for the es-fleet template.

The five correctness requirements from the implementation plan each carry a
test here: resume of theta, Adam state, and generation; honest partial
generations under a timeout; the Evolution Strategies (ES) gradient coming
from wendytrain.es only; the architecture travelling in the wire metadata;
and stale posts being dropped and counted. An end-to-end in-process fleet
(coordinator plus two worker threads over HyperText Transfer Protocol (HTTP)
loopback) covers the whole protocol.

The cartpole environment is imported via its path in templates/single, the
same way the launcher's staged build context lays the files out side by side
inside the deployed image.
"""

import importlib.util
import json
import sys
import threading
from pathlib import Path

import numpy as np
import pytest

from wendytrain import Run, mesh, verify_manifest, wire
from wendytrain.config import Config

TEMPLATE_DIR = Path(__file__).resolve().parents[1]
SINGLE_DIR = TEMPLATE_DIR.parent / "single"

for _path in (str(SINGLE_DIR), str(TEMPLATE_DIR)):
    if _path not in sys.path:
        sys.path.insert(0, _path)


def _load_train():
    """Load the template's train.py under a collision-proof module name."""
    spec = importlib.util.spec_from_file_location("es_fleet_train", TEMPLATE_DIR / "train.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules["es_fleet_train"] = module
    spec.loader.exec_module(module)
    return module


train = _load_train()


def make_cfg(
    *,
    pop: int = 4,
    sigma: float = 0.1,
    lr: float = 0.02,
    gen_timeout_s: float = 30.0,
    max_generations: int = 1,
    checkpoint_every: int = 1,
    hidden: list | None = None,
    seed: int = 0,
) -> Config:
    return Config(
        {
            "es": {"pop": pop, "sigma": sigma, "lr": lr, "gen_timeout_s": gen_timeout_s},
            "run": {
                "max_generations": max_generations,
                "checkpoint_every": checkpoint_every,
                "seed": seed,
            },
            "policy": {"hidden": list(hidden) if hidden is not None else [4]},
        }
    )


def _status(port: int) -> dict:
    return json.loads(mesh.http_get(f"http://127.0.0.1:{port}/status", retries=2))


def _metrics(run: Run) -> list[dict]:
    text = (run.dir / "metrics.jsonl").read_text()
    return [json.loads(line) for line in text.splitlines()]


# --- Policy plumbing ---------------------------------------------------------


def test_episode_return_is_deterministic_under_seed():
    sizes = train.layer_sizes([4])
    theta = train.init_theta(sizes, seed=7)
    first = train.episode_return(theta, sizes, seed=3)
    second = train.episode_return(theta, sizes, seed=3)
    assert first == second
    assert np.isfinite(first)


def test_policy_action_stays_in_the_force_range():
    sizes = train.layer_sizes([8, 6])
    theta = train.init_theta(sizes, seed=0) * 50.0  # exaggerate to test saturation
    action = train.policy_action(theta, sizes, np.array([2.0, -1.0, 0.3, 4.0], np.float32))
    assert action.shape == (1,)
    assert np.all(np.abs(action) <= 1.0)


# --- Requirement 4: architecture travels in the wire metadata ----------------


def test_architecture_travels_in_wire_meta(tmp_path):
    cfg = make_cfg(pop=2, hidden=[8, 6])
    coord = train.Coordinator(
        cfg, Run(tmp_path, "arch"), n_nodes=1, host="127.0.0.1", port=0, loopback=False
    )
    coord.start()
    try:
        blob = mesh.http_get(f"http://127.0.0.1:{coord.port}/params", retries=2)
    finally:
        coord.stop()
    arrays, meta = wire.decode(blob)
    assert meta["architecture"] == [8, 6]
    assert meta["generation"] == 0
    assert "seed_base" in meta
    # Workers construct the policy from the metadata, never from counts.
    sizes = train.sizes_from_meta(meta, arrays["theta"].size)
    assert sizes == [4, 8, 6, 1]
    with pytest.raises(ValueError):
        train.sizes_from_meta(meta, arrays["theta"].size + 1)
    with pytest.raises(ValueError):
        train.sizes_from_meta({"generation": 0}, arrays["theta"].size)


# --- Requirement 5: stale posts are dropped and counted ----------------------


def test_stale_posts_are_dropped_and_counted(tmp_path):
    cfg = make_cfg(pop=4, hidden=[4])
    coord = train.Coordinator(
        cfg, Run(tmp_path, "stale"), n_nodes=1, host="127.0.0.1", port=0, loopback=False
    )
    coord.start()
    try:
        arrays = {
            "seeds": np.array([0], np.int64),
            "returns_plus": np.array([1.0], np.float32),
            "returns_minus": np.array([2.0], np.float32),
        }
        stale = wire.encode(arrays, meta={"generation": 99})
        response = json.loads(
            mesh.http_post(f"http://127.0.0.1:{coord.port}/returns", stale, retries=2)
        )
        assert response["accepted"] is False
        status = _status(coord.port)
        assert status["stale_posts"] == 1
        assert status["pending_contributions"] == 0

        fresh = wire.encode(arrays, meta={"generation": 0})
        response = json.loads(
            mesh.http_post(f"http://127.0.0.1:{coord.port}/returns", fresh, retries=2)
        )
        assert response["accepted"] is True
        status = _status(coord.port)
        assert status["stale_posts"] == 1
        assert status["pending_contributions"] == 1
    finally:
        coord.stop()


# --- Requirement 3: gradient comes from wendytrain.es only -------------------


def test_gradient_comes_from_wendytrain_es(tmp_path, monkeypatch):
    calls = {}
    real_gradient = train.es.gradient

    def spy(returns_plus, returns_minus, seeds, num_params, sigma):
        calls["seeds"] = list(seeds)
        calls["num_params"] = num_params
        calls["sigma"] = sigma
        return real_gradient(returns_plus, returns_minus, seeds, num_params, sigma)

    monkeypatch.setattr(train.es, "gradient", spy)
    cfg = make_cfg(pop=4, hidden=[4], max_generations=1)
    run = Run(tmp_path, "grad")
    coord = train.Coordinator(cfg, run, n_nodes=1, host="127.0.0.1", port=0)
    coord.start()
    try:
        coord.train()
    finally:
        coord.stop()
    # Generation 0 with population 4 draws seeds seed_base + [0..3] = [0..3].
    assert calls["seeds"] == [0, 1, 2, 3]
    assert calls["num_params"] == coord.theta.size
    assert calls["sigma"] == pytest.approx(0.1)


# --- Requirement 1: resume restores theta, Adam state, and generation --------


def test_restart_resumes_theta_adam_and_generation(tmp_path):
    cfg = make_cfg(pop=4, hidden=[4], lr=0.03, max_generations=5, checkpoint_every=1)
    first = train.Coordinator(cfg, Run(tmp_path, "resume"), n_nodes=1, host="127.0.0.1", port=0)
    assert first.resumed is False
    first.start()
    try:
        first.train(max_generations=3)  # killed after generation 2 completes
    finally:
        first.stop()
    theta_at_kill = first.theta.copy()
    adam_at_kill = first.adam_state
    assert int(adam_at_kill["t"]) == 3

    second = train.Coordinator(cfg, Run(tmp_path, "resume"), n_nodes=1, host="127.0.0.1", port=0)
    assert second.resumed is True
    assert second.generation == 3
    np.testing.assert_array_equal(second.theta, theta_at_kill)
    assert int(second.adam_state["t"]) == 3
    np.testing.assert_array_equal(second.adam_state["m"], adam_at_kill["m"])
    np.testing.assert_array_equal(second.adam_state["v"], adam_at_kill["v"])

    second.start()
    try:
        second.train()  # continues to the configured 5 generations
    finally:
        second.stop()
    assert second.generation == 5
    assert int(second.adam_state["t"]) == 5
    records = _metrics(Run(tmp_path, "resume"))
    assert [r["generation"] for r in records] == [0, 1, 2, 3, 4]

    # The exported artifact verifies, closing the deployment boundary.
    run = Run(tmp_path, "resume")
    train.export_artifact(second, run)
    verify_manifest(run.dir)


# --- Requirement 2: honest partial generations under the timeout -------------


def test_partial_generation_is_recorded_honestly(tmp_path):
    # Two nodes are declared but only the coordinator's loopback worker runs;
    # the second worker is deliberately dead. The coordinator must advance on
    # the timeout with what arrived and say so in metrics and /status.
    cfg = make_cfg(pop=8, hidden=[4], max_generations=1, gen_timeout_s=1.0)
    run = Run(tmp_path, "partial")
    coord = train.Coordinator(cfg, run, n_nodes=2, host="127.0.0.1", port=0)
    theta_before = coord.theta.copy()
    coord.start()
    try:
        coord.train()
        status = _status(coord.port)
    finally:
        coord.stop()

    # The loopback worker owns slice 0 of 2: pairs [0, 4) of population 8.
    records = _metrics(run)
    assert len(records) == 1
    record = records[0]
    assert record["generation"] == 0
    assert record["n_contributed"] == 4
    assert record["population"] == 8
    assert record["n_contributed"] < record["population"]
    assert status["n_contributed"] == 4
    assert status["population"] == 8
    # The update was applied from the partial set, not skipped and not lied about.
    assert not np.array_equal(coord.theta, theta_before)
    assert int(coord.adam_state["t"]) == 1


# --- End to end: coordinator, loopback worker, two remote workers ------------


def test_fleet_end_to_end_improves_cartpole(tmp_path):
    cfg = make_cfg(
        pop=8,
        sigma=0.1,
        lr=0.05,
        gen_timeout_s=10.0,
        max_generations=5,
        checkpoint_every=1,
        hidden=[8],
        seed=0,
    )
    run = Run(tmp_path, "e2e")
    coord = train.Coordinator(cfg, run, n_nodes=3, host="127.0.0.1", port=0)
    coord.start()
    stops = [threading.Event(), threading.Event()]
    workers = [
        threading.Thread(
            target=train.worker_loop,
            args=(f"127.0.0.1:{coord.port}", index, 3),
            kwargs={"stop": stops[index - 1]},
            daemon=True,
        )
        for index in (1, 2)
    ]
    for worker in workers:
        worker.start()
    try:
        coord.train()
        status = _status(coord.port)
    finally:
        for stop in stops:
            stop.set()
        coord.stop()

    records = _metrics(run)
    assert [r["generation"] for r in records] == [0, 1, 2, 3, 4]
    for record in records:
        # Every slice contributed every generation: no timeouts on loopback.
        assert record["n_contributed"] == 8
        assert record["population"] == 8
        assert np.isfinite(record["mean_return"])
    # Learning signal: deterministic under the fixed seeds.
    assert records[-1]["mean_return"] > records[0]["mean_return"]

    assert status["generation"] == 5
    assert status["done"] is True
    assert status["n_contributed"] == 8

    # The run checkpointed and resumes at the right place.
    reopened = Run(tmp_path, "e2e")
    arrays, meta, iteration = reopened.load_latest()
    assert iteration == 4
    assert meta["architecture"] == [8]
    assert int(arrays["adam_t"]) == 5


# --- Configuration and topology glue -----------------------------------------


def test_es_environment_overrides_config():
    cfg = train.load_settings(
        env={"ES_POP": "16", "ES_GEN_TIMEOUT_S": "2.5", "ES_LR": "0.5"},
        path=None,
    )
    assert cfg.es.pop == 16
    assert cfg.es.gen_timeout_s == 2.5
    assert cfg.es.lr == 0.5
    # Untouched keys keep their defaults.
    assert cfg.es.sigma == train.DEFAULTS["es"]["sigma"]


def test_resolve_topology_from_numeric_asset_ids():
    env = {"MESH_SELF": "283", "MESH_PEERS": "334,211,283", "MESH_PORT": "8080"}
    coordinator, index, count = train.resolve_topology(env)
    # Sorted ids are [211, 283, 334]: 211 coordinates, 283 is rank 1 of 3.
    assert coordinator == "device-211.cloud.wendy.dev:8080"
    assert (index, count) == (1, 3)


def test_resolve_topology_explicit_overrides_win():
    env = {
        "MESH_SELF": "somehost",
        "MESH_PEERS": "otherhost:9000",
        "ES_COORDINATOR": "otherhost:9000",
        "ES_WORKER_INDEX": "1",
        "ES_WORKER_COUNT": "2",
    }
    coordinator, index, count = train.resolve_topology(env)
    assert coordinator == "otherhost:9000"
    assert (index, count) == (1, 2)


def test_resolve_topology_refuses_to_guess():
    env = {"MESH_SELF": "somehost", "MESH_PEERS": "otherhost"}
    with pytest.raises(ValueError):
        train.resolve_topology(env)


# --- Deployment declaration ---------------------------------------------------


def test_wendy_json_declares_the_contract():
    spec = json.loads((TEMPLATE_DIR / "wendy.json").read_text())
    assert spec["appId"] == "sh.wendy.training.es-fleet"
    assert spec["isolation"] == "isolated"
    service = spec["services"]["trainer"]
    entitlements = {e["type"]: e for e in service["entitlements"]}
    assert entitlements["network"]["mode"] == "mesh"
    assert entitlements["network"]["serviceCIDR"] == "10.99.0.0/16"
    assert {"host": 8080, "container": 8080} in entitlements["network"]["ports"]
    assert entitlements["persist"]["name"] == "wt-es-ckpt"
    assert entitlements["persist"]["path"] == "/data/checkpoints"
    env = service["env"]
    for name in (
        "MESH_PEERS",
        "MESH_SELF",
        "MESH_PORT",
        "WT_ROLE",
        "WT_RUN_ID",
        "WT_CKPT_DIR",
        "WT_CONFIG",
        "ES_POP",
        "ES_GEN_TIMEOUT_S",
    ):
        assert env[name] == "${" + name + "}"


def test_topology_accepts_the_launchers_generic_trio():
    """Hostname peers plus WT_COORDINATOR/WT_NODE_INDEX/WT_NODE_COUNT resolve.

    This is the lan transport shape that aborted on hardware before the
    launcher emitted the trio: MESH_SELF is numeric but MESH_PEERS entries
    are hostnames, so numeric derivation refuses; the generic contract must
    win before that refusal. Template-specific ES_* variables still win over
    the generic ones.
    """

    env = {
        "MESH_SELF": "334",
        "MESH_PEERS": "spark-48fd.local:8080,spark-edeb.local:8080",
        "WT_COORDINATOR": "spark-48fd.local:8080",
        "WT_NODE_INDEX": "2",
        "WT_NODE_COUNT": "3",
    }
    assert train.resolve_topology(env) == ("spark-48fd.local:8080", 2, 3)

    env["ES_COORDINATOR"] = "elsewhere:9"
    env["ES_WORKER_INDEX"] = "1"
    env["ES_WORKER_COUNT"] = "4"
    assert train.resolve_topology(env) == ("elsewhere:9", 1, 4)
