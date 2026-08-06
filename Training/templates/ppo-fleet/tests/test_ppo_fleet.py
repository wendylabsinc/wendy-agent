"""Tests for the ppo-fleet template.

The three correctness proofs required by the plan:
1. A learner plus one in-process actor improves the cart-pole return over
   15 weight versions.
2. Resume restores the torch optimizer state exactly, step counts included.
3. Stale rollouts are rejected, and the rejection is visible in ``/status``
   rather than silently dropped.

The tests skip cleanly when PyTorch is not installed; torch is part of the
development requirements so they run locally and in Continuous Integration.
"""

import copy
import importlib.util
import json
import sys
from pathlib import Path

import numpy as np
import pytest

TEMPLATE_DIR = Path(__file__).resolve().parents[1]
SINGLE_DIR = TEMPLATE_DIR.parent / "single"
for entry in (str(TEMPLATE_DIR), str(SINGLE_DIR)):
    if entry not in sys.path:
        sys.path.insert(0, entry)

torch = pytest.importorskip(
    "torch", reason="ppo-fleet tests need PyTorch; install it with: pip install torch"
)

from wendytrain import load_config, mesh, wire  # noqa: E402 (after sys.path setup)
from wendytrain.run import Run  # noqa: E402
from wendytrain.service import serve  # noqa: E402


def _load_module(name: str, path: Path):
    """Import a template file under a unique module name.

    Template directories all contain a ``train.py``; loading through
    ``importlib`` under a distinct name keeps suites from colliding when more
    than one template's tests run in a single pytest session.
    """
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


nets = _load_module("nets", TEMPLATE_DIR / "nets.py")
train = _load_module("ppo_fleet_train", TEMPLATE_DIR / "train.py")


def _test_cfg(**ppo_overrides):
    """A small, fast configuration for in-process tests."""
    data = copy.deepcopy(train.DEFAULTS)
    data["ppo"].update(
        {
            "steps": 400,
            "epochs": 4,
            "minibatch": 100,
            "lr": 1e-3,
            "total_versions": 15,
        }
    )
    data["ppo"].update(ppo_overrides)
    data["policy"]["hidden"] = [24]
    data["run"]["checkpoint_every"] = 5
    return load_config(data, env={})


def _start_learner(cfg, tmp_path, run_id, seed=0):
    torch.manual_seed(seed)
    run = Run(tmp_path, run_id=run_id)
    learner = train.Learner(cfg, run, env={}, seed=seed)
    server = serve(learner.routes(), port=0, host="127.0.0.1")
    return learner, server, server.server_address[1]


def test_flat_params_round_trip():
    torch.manual_seed(0)
    source = nets.build_policy(4, 1, [24])
    target = nets.build_policy(4, 1, [24])
    nets.set_flat_params(target, nets.flat_params(source))
    obs = torch.randn(7, 4)
    with torch.no_grad():
        assert torch.equal(source(obs), target(obs))


def test_weights_blob_carries_version_architecture_and_log_std(tmp_path):
    cfg = _test_cfg()
    learner, server, port = _start_learner(cfg, tmp_path, "meta")
    try:
        blob = mesh.http_get(f"http://127.0.0.1:{port}/weights")
    finally:
        server.shutdown()
    arrays, meta = wire.decode(blob)
    assert meta["version"] == 0
    assert meta["architecture"] == [24]
    assert len(meta["log_std"]) == 1
    assert arrays["policy"].dtype == np.float32
    assert arrays["policy"].shape == nets.flat_params(learner.policy).shape
    assert arrays["value"].shape == nets.flat_params(learner.value).shape


def test_actor_appends_tail_bootstrap_step(tmp_path):
    cfg = _test_cfg(steps=8)
    learner, server, port = _start_learner(cfg, tmp_path, "tail")
    try:
        actor = train.Actor(cfg, f"127.0.0.1:{port}", env={}, seed=3)
        actor.pull_weights()
        arrays, meta = actor.collect()
    finally:
        server.shutdown()
    # Eight steps from a reset never end a cart-pole episode, so the actor
    # must append the synthetic bootstrap step itself (the library's gae
    # bootstraps 0 past the end of the batch).
    assert meta["tail_bootstrap"] is True
    assert arrays["rewards"].shape == (9,)
    assert arrays["dones"][-1] == 1
    assert arrays["rewards"][-1] == pytest.approx(float(arrays["values"][-1]))


def test_learner_and_actor_improve_return_over_15_versions(tmp_path):
    cfg = _test_cfg()
    learner, server, port = _start_learner(cfg, tmp_path, "improve")
    try:
        actor = train.Actor(cfg, f"127.0.0.1:{port}", env={}, seed=1)
        while learner.version < 15:
            reply = actor.run_once()
            assert reply["accepted"] is True
            assert learner.process_one(timeout=10.0)
    finally:
        server.shutdown()
    history = [h for h in learner.return_history if h is not None]
    assert len(history) >= 10
    first = float(np.mean(history[:3]))
    last = float(np.mean(history[-3:]))
    assert last > first * 1.5, (
        f"return did not improve enough: first={first:.2f} last={last:.2f}"
    )


def test_resume_restores_optimizer_state_exactly(tmp_path):
    cfg = _test_cfg()
    learner, server, port = _start_learner(cfg, tmp_path, "resume")
    try:
        actor = train.Actor(cfg, f"127.0.0.1:{port}", env={}, seed=2)
        while learner.version < 3:
            actor.run_once()
            assert learner.process_one(timeout=10.0)
    finally:
        server.shutdown()
    learner.checkpoint()

    # A different construction seed proves the restore overwrites the fresh
    # initialization rather than accidentally matching it.
    torch.manual_seed(999)
    resumed = train.Learner(cfg, Run(tmp_path, run_id="resume"), env={}, seed=999)

    assert resumed.version == 3
    before = learner.optimizer.state_dict()
    after = resumed.optimizer.state_dict()
    assert len(before["state"]) == len(after["state"]) > 0
    for key, state in before["state"].items():
        assert int(state["step"]) == int(after["state"][key]["step"])
        assert torch.equal(state["exp_avg"], after["state"][key]["exp_avg"])
        assert torch.equal(state["exp_avg_sq"], after["state"][key]["exp_avg_sq"])
    assert np.array_equal(nets.flat_params(learner.policy), nets.flat_params(resumed.policy))
    assert np.array_equal(nets.flat_params(learner.value), nets.flat_params(resumed.value))
    assert torch.equal(learner.log_std.detach(), resumed.log_std.detach())


def test_stale_rollout_rejected_and_counted(tmp_path):
    cfg = _test_cfg(max_staleness=0)
    learner, server, port = _start_learner(cfg, tmp_path, "stale")
    try:
        actor = train.Actor(cfg, f"127.0.0.1:{port}", env={}, seed=4)
        reply = actor.run_once()
        assert reply["accepted"] is True
        assert learner.process_one(timeout=10.0)
        assert learner.version == 1

        stale_blob = wire.encode(
            {"obs": np.zeros((1, 4), np.float32)}, meta={"weights_version": 0}
        )
        reply = json.loads(
            mesh.http_post(f"http://127.0.0.1:{port}/rollout", stale_blob)
        )
        assert reply["accepted"] is False
        assert reply["reason"] == "stale"

        status = json.loads(mesh.http_get(f"http://127.0.0.1:{port}/status"))
    finally:
        server.shutdown()
    assert status["version"] == 1
    assert status["accepted_rollouts"] == 1
    assert status["stale_rollouts"] == 1


def test_learner_target_accepts_the_launchers_generic_coordinator():
    """PPO_LEARNER wins, WT_COORDINATOR is next, numeric derivation last."""

    env = {
        "MESH_PEERS": "spark-48fd.local:8080,spark-edeb.local:8080",
        "WT_COORDINATOR": "spark-48fd.local:8080",
    }
    assert train.learner_target(env) == "spark-48fd.local:8080"
    env["PPO_LEARNER"] = "elsewhere:9"
    assert train.learner_target(env) == "elsewhere:9"
