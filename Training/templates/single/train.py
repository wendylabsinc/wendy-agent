"""Single-device Evolution Strategies (ES) trainer for the built-in cart-pole.

The complete run lifecycle on one device: layered configuration, a durable run
that resumes from its latest checkpoint including optimizer state, honest
per-generation metrics, and an artifact manifest on completion. The policy is
a small tanh Multi-Layer Perceptron (MLP) held as one flat float32 parameter
vector; training is mirrored-sampling ES with combined rank normalization from
``wendytrain.es`` and Adam in maximize mode from ``wendytrain.optim``.

The sweep template imports ``train_loop(cfg, run) -> dict`` from this module;
keep that signature stable.

Environment contract (all optional):
    WT_RUN_ID      stable run identifier, names the checkpoint directory
    WT_CKPT_DIR    checkpoint root, default /data/checkpoints
    WT_CONFIG      path to a TOML configuration file baked into the image
    WT_WORKERS     evaluation processes, default the CPU count
    WT_<S>__<K>    configuration overrides, for example WT_ES__POP=64
"""

from __future__ import annotations

import json
import os
import sys
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

import numpy as np

from wendytrain import Run, es, load_config, optim, wire
from wendytrain.manifest import write_manifest

# The container stages every file flat in /app; in the repository checkout the
# module must find cartpole.py next to itself even when imported from a file
# path, so the template directory joins sys.path.
_HERE = Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.append(str(_HERE))

from cartpole import CartPole

DEFAULTS = {
    "es": {"pop": 32, "sigma": 0.1, "lr": 0.02},
    "run": {"max_iterations": 200, "checkpoint_every": 10, "seed": 0},
    "policy": {"hidden": [32]},
}

OBS_DIM = CartPole.obs_dim
ACT_DIM = CartPole.act_dim

# Spreads sweep members' perturbation seeds apart so runs that differ only in
# run.seed never share a perturbation sequence.
_SEED_STRIDE = 1_000_003


def layer_dims(hidden: list[int]) -> list[int]:
    """Full layer widths of the policy network, input to output."""
    return [OBS_DIM, *hidden, ACT_DIM]


def num_params(hidden: list[int]) -> int:
    """Number of scalar parameters in the flat policy vector."""
    dims = layer_dims(hidden)
    return sum(fan_in * fan_out + fan_out for fan_in, fan_out in zip(dims, dims[1:]))


def init_theta(hidden: list[int], seed: int) -> np.ndarray:
    """Deterministic float32 initialization, scaled by 1/sqrt(fan_in)."""
    rng = np.random.default_rng(seed)
    dims = layer_dims(hidden)
    parts = []
    for fan_in, fan_out in zip(dims, dims[1:]):
        weight = rng.standard_normal((fan_in, fan_out)) / np.sqrt(fan_in)
        parts.append(weight.astype(np.float32).ravel())
        parts.append(np.zeros(fan_out, dtype=np.float32))
    return np.concatenate(parts)


def policy_forward(theta: np.ndarray, hidden: list[int], obs: np.ndarray) -> np.ndarray:
    """Run the tanh MLP; the output tanh keeps the action inside [-1, 1]."""
    dims = layer_dims(hidden)
    x = np.asarray(obs, dtype=np.float32)
    offset = 0
    for fan_in, fan_out in zip(dims, dims[1:]):
        weight = theta[offset : offset + fan_in * fan_out].reshape(fan_in, fan_out)
        offset += fan_in * fan_out
        bias = theta[offset : offset + fan_out]
        offset += fan_out
        x = np.tanh(x @ weight + bias)
    return x


def episode_return(theta: np.ndarray, hidden: list[int], env_seed: int) -> float:
    """Total reward of one episode under the policy, deterministic per seed."""
    env = CartPole(env_seed)
    obs = env.reset(seed=env_seed)
    total = 0.0
    done = False
    while not done:
        obs, reward, done, _ = env.step(policy_forward(theta, hidden, obs))
        total += reward
    return total


def evaluate_pair(task: tuple[np.ndarray, float, int, list[int]]) -> tuple[float, float]:
    """Evaluate one mirrored perturbation pair; the environment seed equals the
    perturbation seed on both sides (common random numbers)."""
    theta, sigma, seed, hidden = task
    eps = es.perturbation(seed, theta.size)
    return (
        episode_return(theta + sigma * eps, hidden, seed),
        episode_return(theta - sigma * eps, hidden, seed),
    )


def _save_checkpoint(
    run: Run,
    theta: np.ndarray,
    adam_state: dict,
    hidden: list[int],
    iteration: int,
    mean_return: float,
    best_mean: float,
) -> None:
    run.save_checkpoint(
        {
            "theta": theta,
            "adam_m": adam_state["m"],
            "adam_v": adam_state["v"],
            "adam_t": adam_state["t"],
        },
        {
            "architecture": hidden,
            "obs_dim": OBS_DIM,
            "act_dim": ACT_DIM,
            "mean_return": mean_return,
            "best_mean_return": best_mean,
        },
        iteration,
    )


def _export_artifact(run: Run, theta: np.ndarray, hidden: list[int], iteration: int) -> None:
    """Write the deployable policy and its manifest into the run directory."""
    run.dir.mkdir(parents=True, exist_ok=True)
    blob = wire.encode(
        {"theta": theta},
        {"architecture": hidden, "obs_dim": OBS_DIM, "act_dim": ACT_DIM, "iteration": iteration},
    )
    (run.dir / "policy.wtw").write_bytes(blob)
    write_manifest(
        run.dir,
        files=["policy.wtw"],
        inputs={"observation": [OBS_DIM]},
        outputs={"action": [ACT_DIM]},
        layout=(
            "observation[0] cart position (m), [1] cart velocity (m/s), "
            "[2] pole angle (rad), [3] pole angular velocity (rad/s); "
            "action[0] force in [-1, 1]"
        ),
        framework="numpy",
        extra={"run_id": run.run_id, "iterations": iteration},
    )


def train_loop(cfg, run: Run, workers: int | None = None) -> dict:
    """Train to ``cfg.run.max_iterations`` generations, resuming if possible.

    ``iteration`` counts completed generations, so a checkpoint at iteration
    ``n`` means generations ``0..n-1`` are done and the Adam step count is
    ``n``. Returns a result dictionary with ``run_id``, ``iterations``,
    ``final_mean_return``, and ``best_mean_return``.
    """
    hidden = [int(h) for h in cfg.policy.hidden]
    pop = int(cfg.es.pop)
    sigma = float(cfg.es.sigma)
    lr = float(cfg.es.lr)
    max_iterations = int(cfg.run.max_iterations)
    checkpoint_every = int(cfg.run.checkpoint_every)
    seed0 = int(cfg.run.seed)
    n = num_params(hidden)

    loaded = run.load_latest()
    if loaded is not None:
        arrays, meta, start = loaded
        saved = [int(h) for h in meta.get("architecture", [])]
        if saved != hidden:
            raise ValueError(
                f"checkpoint architecture {saved} does not match configured "
                f"policy.hidden {hidden}; restore the configuration or start a new WT_RUN_ID"
            )
        theta = arrays["theta"]
        adam_state = {"m": arrays["adam_m"], "v": arrays["adam_v"], "t": arrays["adam_t"]}
        last_mean = meta.get("mean_return")
        best_mean = meta.get("best_mean_return", last_mean)
        print(f"[single] resumed iteration={start} adam_t={int(arrays['adam_t'])}", flush=True)
    else:
        start = 0
        theta = init_theta(hidden, seed0)
        adam_state = None
        last_mean = None
        best_mean = None
        print(f"[single] fresh run, {n} parameters, population {pop}", flush=True)

    if workers is None:
        workers = os.cpu_count() or 1
    pool = ProcessPoolExecutor(max_workers=workers) if workers > 1 else None
    try:
        for gen in range(start, max_iterations):
            seeds = [seed0 * _SEED_STRIDE + gen * pop + i for i in range(pop)]
            tasks = [(theta, sigma, seed, hidden) for seed in seeds]
            if pool is not None:
                results = list(pool.map(evaluate_pair, tasks))
            else:
                results = [evaluate_pair(task) for task in tasks]
            returns_plus = np.array([r[0] for r in results], dtype=np.float64)
            returns_minus = np.array([r[1] for r in results], dtype=np.float64)
            grad = es.gradient(returns_plus, returns_minus, seeds, n, sigma)
            theta, adam_state = optim.adam_step(theta, grad, adam_state, lr=lr, maximize=True)

            completed = gen + 1
            everything = np.concatenate([returns_plus, returns_minus])
            mean_return = float(np.mean(everything))
            best_return = float(np.max(everything))
            best_mean = mean_return if best_mean is None else max(best_mean, mean_return)
            last_mean = mean_return
            run.log_metrics(
                {
                    "iteration": completed,
                    "generation": gen,
                    "mean_return": mean_return,
                    "best_return": best_return,
                    "population": pop,
                    "n_contributed": pop,
                }
            )
            if completed % checkpoint_every == 0 or completed == max_iterations:
                _save_checkpoint(run, theta, adam_state, hidden, completed, mean_return, best_mean)
    finally:
        if pool is not None:
            pool.shutdown()

    iterations = max(start, max_iterations)
    _export_artifact(run, theta, hidden, iterations)
    return {
        "run_id": run.run_id,
        "iterations": iterations,
        "final_mean_return": last_mean,
        "best_mean_return": best_mean,
    }


def main() -> None:
    # Enumerated ${VAR} passthrough in wendy.json can deliver unset variables
    # as empty strings; treat empty as unset so every default applies.
    env = {k: v for k, v in os.environ.items() if v != ""}
    cfg = load_config(DEFAULTS, env=env)
    run = Run.from_env(env)
    workers = int(env.get("WT_WORKERS", "0")) or None
    result = train_loop(cfg, run, workers=workers)
    print("[single] finished: " + json.dumps(result), flush=True)


if __name__ == "__main__":
    main()
