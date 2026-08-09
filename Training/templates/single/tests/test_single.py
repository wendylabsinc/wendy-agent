"""Tests for the single-device Evolution Strategies (ES) template.

``train`` and ``cartpole`` resolve to ``Training/templates/single/`` through
``conftest.py``. Everything runs in-process and fully deterministic; no device,
no Docker, no network.
"""

import json
from pathlib import Path

import numpy as np
import pytest

import cartpole as cartpole_mod
import train

from wendytrain import Run, load_config, wire
from wendytrain.manifest import verify_manifest

TEMPLATE_DIR = Path(train.__file__).resolve().parent

# A configuration small enough that a full run takes well under a second.
FAST = {
    "WT_ES__POP": 4,
    "WT_ES__SIGMA": 0.1,
    "WT_ES__LR": 0.02,
    "WT_POLICY__HIDDEN": "[8]",
    "WT_RUN__MAX_ITERATIONS": 4,
    "WT_RUN__CHECKPOINT_EVERY": 2,
}


def make_cfg(overrides: dict):
    env = {key: str(value) for key, value in overrides.items()}
    return load_config(train.DEFAULTS, env=env)


def load_policy(run: Run):
    arrays, meta = wire.decode((run.dir / "policy.wtw").read_bytes())
    return arrays, meta


def metric_lines(run: Run) -> list[dict]:
    text = (run.dir / "metrics.jsonl").read_text()
    return [json.loads(line) for line in text.splitlines()]


class TestCartPole:
    def test_same_seed_gives_the_same_ten_step_trajectory(self):
        actions = [0.5, -0.5, 1.0, -1.0, 0.0, 0.3, -0.3, 0.9, -0.9, 0.1]
        trajectories = []
        for _ in range(2):
            env = cartpole_mod.CartPole(seed=3)
            obs = env.reset(seed=3)
            steps = [obs.copy()]
            for action in actions:
                obs, _, done, _ = env.step(np.array([action], dtype=np.float32))
                steps.append(obs.copy())
                assert not done  # ten steps from near-upright never terminate
            trajectories.append(np.stack(steps))
        assert np.array_equal(trajectories[0], trajectories[1])

    def test_interface_shapes_and_reward(self):
        env = cartpole_mod.CartPole(seed=0)
        obs = env.reset(seed=0)
        assert obs.shape == (4,)
        assert obs.dtype == np.float32
        assert cartpole_mod.CartPole.obs_dim == 4
        assert cartpole_mod.CartPole.act_dim == 1
        obs, reward, done, info = env.step(np.zeros(1, dtype=np.float32))
        assert obs.shape == (4,)
        assert reward == pytest.approx(1.0)
        assert done is False
        assert info["steps"] == 1

    def test_effort_penalty_and_action_clipping(self):
        env = cartpole_mod.CartPole(seed=0)
        env.reset(seed=0)
        _, reward, _, _ = env.step(np.array([1.0]))
        assert reward == pytest.approx(1.0 - 0.05)
        env_a = cartpole_mod.CartPole(seed=0)
        env_a.reset(seed=0)
        env_b = cartpole_mod.CartPole(seed=0)
        env_b.reset(seed=0)
        obs_a, _, _, _ = env_a.step(np.array([5.0]))
        obs_b, _, _, _ = env_b.step(np.array([1.0]))
        assert np.array_equal(obs_a, obs_b)


class TestPolicy:
    def test_parameter_count_and_forward_shape(self):
        hidden = [8]
        n = train.num_params(hidden)
        assert n == 4 * 8 + 8 + 8 * 1 + 1
        theta = train.init_theta(hidden, seed=0)
        assert theta.shape == (n,)
        assert theta.dtype == np.float32
        action = train.policy_forward(theta, hidden, np.zeros(4, dtype=np.float32))
        assert action.shape == (1,)
        assert -1.0 <= float(action[0]) <= 1.0

    def test_init_is_deterministic_per_seed(self):
        assert np.array_equal(train.init_theta([8], 1), train.init_theta([8], 1))
        assert not np.array_equal(train.init_theta([8], 1), train.init_theta([8], 2))


class TestConfig:
    def test_defaults_match_the_plan(self):
        cfg = load_config(train.DEFAULTS, env={})
        assert cfg.es.pop == 32
        assert cfg.es.sigma == pytest.approx(0.1)
        assert cfg.es.lr == pytest.approx(0.02)
        assert cfg.run.max_iterations == 200
        assert cfg.run.checkpoint_every == 10
        assert cfg.policy.hidden == [32]

    def test_config_toml_mirrors_the_code_defaults(self):
        from_file = load_config(train.DEFAULTS, path=str(TEMPLATE_DIR / "config.toml"), env={})
        from_code = load_config(train.DEFAULTS, env={})
        assert from_file.as_dict() == from_code.as_dict()

    def test_env_overrides_reach_the_loop_inputs(self):
        cfg = make_cfg({"WT_ES__POP": 8, "WT_POLICY__HIDDEN": "[4, 4]"})
        assert cfg.es.pop == 8
        assert cfg.policy.hidden == [4, 4]


class TestTrainLoop:
    def test_twenty_generations_improve_mean_return_over_generation_zero(self, tmp_path):
        cfg = make_cfg(
            {
                "WT_ES__POP": 16,
                "WT_ES__SIGMA": 0.1,
                "WT_ES__LR": 0.05,
                "WT_POLICY__HIDDEN": "[16]",
                "WT_RUN__MAX_ITERATIONS": 20,
                "WT_RUN__CHECKPOINT_EVERY": 10,
            }
        )
        run = Run(tmp_path, "improve")
        result = train.train_loop(cfg, run, workers=1)
        lines = metric_lines(run)
        assert len(lines) == 20
        assert lines[-1]["mean_return"] > lines[0]["mean_return"]
        assert result["iterations"] == 20
        assert result["final_mean_return"] == pytest.approx(lines[-1]["mean_return"])
        assert result["best_mean_return"] >= result["final_mean_return"] - 1e-9

    def test_kill_and_resume_restores_iteration_and_adam_step_count(self, tmp_path, capsys):
        first = {**FAST, "WT_RUN__MAX_ITERATIONS": 10, "WT_RUN__CHECKPOINT_EVERY": 5}
        run = Run(tmp_path, "resume")
        train.train_loop(make_cfg(first), run, workers=1)
        # A brand-new Run on the same directory stands in for a container restart.
        run_again = Run(tmp_path, "resume")
        assert run_again.iteration == 10
        arrays, meta, iteration = run_again.load_latest()
        assert iteration == 10
        assert int(arrays["adam_t"]) == 10
        capsys.readouterr()
        second = {**first, "WT_RUN__MAX_ITERATIONS": 20}
        result = train.train_loop(make_cfg(second), run_again, workers=1)
        assert "resumed iteration=10 adam_t=10" in capsys.readouterr().out
        assert result["iterations"] == 20
        arrays, _, iteration = Run(tmp_path, "resume").load_latest()
        assert iteration == 20
        assert int(arrays["adam_t"]) == 20

    def test_resume_matches_uninterrupted_training_exactly(self, tmp_path):
        base = {**FAST, "WT_RUN__MAX_ITERATIONS": 20, "WT_RUN__CHECKPOINT_EVERY": 10}
        straight = Run(tmp_path / "a", "run")
        train.train_loop(make_cfg(base), straight, workers=1)
        split = Run(tmp_path / "b", "run")
        train.train_loop(make_cfg({**base, "WT_RUN__MAX_ITERATIONS": 10}), split, workers=1)
        train.train_loop(make_cfg(base), Run(tmp_path / "b", "run"), workers=1)
        theta_straight = load_policy(straight)[0]["theta"]
        theta_split = load_policy(Run(tmp_path / "b", "run"))[0]["theta"]
        assert np.array_equal(theta_straight, theta_split)

    def test_resume_with_a_different_architecture_raises(self, tmp_path):
        short = {**FAST, "WT_RUN__MAX_ITERATIONS": 2, "WT_RUN__CHECKPOINT_EVERY": 2}
        train.train_loop(make_cfg(short), Run(tmp_path, "arch"), workers=1)
        with pytest.raises(ValueError, match="architecture"):
            train.train_loop(
                make_cfg({**short, "WT_POLICY__HIDDEN": "[4]"}),
                Run(tmp_path, "arch"),
                workers=1,
            )

    def test_completed_run_writes_a_verifiable_artifact_manifest(self, tmp_path):
        run = Run(tmp_path, "artifact")
        train.train_loop(make_cfg(FAST), run, workers=1)
        verify_manifest(run.dir)
        arrays, meta = load_policy(run)
        assert arrays["theta"].shape == (train.num_params([8]),)
        assert meta["architecture"] == [8]
        manifest = json.loads((run.dir / "manifest.json").read_text())
        assert manifest["framework"] == "numpy"
        assert manifest["inputs"] == {"observation": [4]}
        assert manifest["outputs"] == {"action": [1]}

    def test_metrics_are_honest_about_population(self, tmp_path):
        run = Run(tmp_path, "metrics")
        train.train_loop(make_cfg(FAST), run, workers=1)
        for line in metric_lines(run):
            assert line["population"] == 4
            assert line["n_contributed"] == 4
            assert np.isfinite(line["mean_return"])

    def test_resume_after_completion_trains_no_further(self, tmp_path):
        first = train.train_loop(make_cfg(FAST), Run(tmp_path, "done"), workers=1)
        again = train.train_loop(make_cfg(FAST), Run(tmp_path, "done"), workers=1)
        assert again["iterations"] == 4
        assert again["final_mean_return"] == pytest.approx(first["final_mean_return"])
        assert len(metric_lines(Run(tmp_path, "done"))) == 4

    def test_process_pool_evaluation_matches_serial(self, tmp_path):
        base = {**FAST, "WT_RUN__MAX_ITERATIONS": 2, "WT_RUN__CHECKPOINT_EVERY": 2}
        serial = Run(tmp_path / "serial", "run")
        train.train_loop(make_cfg(base), serial, workers=1)
        pooled = Run(tmp_path / "pool", "run")
        train.train_loop(make_cfg(base), pooled, workers=2)
        assert np.array_equal(load_policy(serial)[0]["theta"], load_policy(pooled)[0]["theta"])


def test_empty_environment_values_are_treated_as_unset(monkeypatch, tmp_path):
    """Enumerated ${VAR} passthrough can deliver unset variables as "".

    Found while documenting: with WT_WORKERS="" the previous main crashed on
    int(""). The contract is that empty means unset and defaults apply.
    """

    monkeypatch.setenv("WT_WORKERS", "")
    monkeypatch.setenv("WT_ES__POP", "")
    monkeypatch.setenv("WT_ES__SIGMA", "")
    monkeypatch.setenv("WT_POLICY__HIDDEN", "[8]")
    monkeypatch.setenv("WT_RUN_ID", "empty-env")
    monkeypatch.setenv("WT_CKPT_DIR", str(tmp_path))
    monkeypatch.setenv("WT_RUN__MAX_ITERATIONS", "1")
    train.main()
    assert (tmp_path / "empty-env").is_dir()
