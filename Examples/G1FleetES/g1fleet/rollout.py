"""Pluggable rollout backends. CPUBackend uses the mujoco C engine across a
process pool (each worker builds its own G1Env — MjModel/MjData are not
shareable across processes)."""
from __future__ import annotations
from dataclasses import dataclass
from concurrent.futures import ProcessPoolExecutor
import numpy as np
from .g1env import G1Env
from .policy import MLPPolicy


@dataclass
class Trajectory:
    obs: np.ndarray; actions: np.ndarray; logprobs: np.ndarray
    rewards: np.ndarray; values: np.ndarray; dones: np.ndarray


def _infer_hidden(obs_dim: int, act_dim: int, total_params: int, fallback):
    """Recover a symmetric 2-hidden-layer (h, h) shape from a flat param
    vector's length. All policies in this codebase use two equal-width
    hidden layers, so the layer sizes are fully determined by obs_dim,
    act_dim and total_params: this lets a rollout worker safely reconstruct
    an MLPPolicy of the *caller's* actual architecture even if the backend
    was constructed with a different default hidden size.

    total = obs*h + h + h*h + h + h*act + act
          = h^2 + h*(obs+act+2) + act
    Solved via the quadratic formula; falls back to `fallback` if no
    positive-integer solution reproduces total_params exactly (e.g. a
    non-symmetric or non-2-layer architecture).
    """
    b = obs_dim + act_dim + 2
    c = act_dim - total_params
    disc = b * b - 4 * c
    if disc >= 0:
        h = int(round((-b + disc ** 0.5) / 2))
        if h > 0:
            n = obs_dim * h + h + h * h + h + h * act_dim + act_dim
            if n == total_params:
                return (h, h)
    return fallback


# --- module-level worker fns (must be picklable for ProcessPoolExecutor) ---
_ENV = {}

def _get_env(obs_dim, act_dim, seed):
    key = (obs_dim, act_dim)
    if key not in _ENV:
        _ENV[key] = G1Env(seed=seed)
    return _ENV[key]

def _episode_return(args):
    flat, seed, obs_dim, act_dim, hidden = args
    env = _get_env(obs_dim, act_dim, seed)
    hidden = _infer_hidden(obs_dim, act_dim, len(flat), hidden)
    pol = MLPPolicy(obs_dim, act_dim, hidden=hidden)
    pol.set_flat(flat)
    obs = env.reset(seed=seed); total = 0.0; done = False
    while not done:
        obs, r, done, _ = env.step(pol.act(obs)); total += r
    return total


class CPUBackend:
    def __init__(self, obs_dim, act_dim, workers: int = 4, hidden=(256, 256)):
        self.obs_dim, self.act_dim, self.workers, self.hidden = obs_dim, act_dim, workers, tuple(hidden)

    def evaluate_returns(self, param_vectors, seeds) -> np.ndarray:
        args = [(np.asarray(v, np.float32), int(s), self.obs_dim, self.act_dim, self.hidden)
                for v, s in zip(param_vectors, seeds)]
        if self.workers <= 1:
            return np.array([_episode_return(a) for a in args], dtype=np.float32)
        with ProcessPoolExecutor(max_workers=self.workers) as ex:
            return np.array(list(ex.map(_episode_return, args)), dtype=np.float32)

    def collect_trajectory(self, param_vector, n_steps, seed, value_fn=None) -> Trajectory:
        param_vector = np.asarray(param_vector, np.float32)
        hidden = _infer_hidden(self.obs_dim, self.act_dim, param_vector.size, self.hidden)
        env = G1Env(seed=seed); pol = MLPPolicy(self.obs_dim, self.act_dim, hidden=hidden)
        pol.set_flat(param_vector)
        obs = env.reset(seed=seed)
        O, A, R, D, V = [], [], [], [], []
        for _ in range(n_steps):
            a = pol.act(obs)
            O.append(obs); A.append(a)
            V.append(float(value_fn(obs)) if value_fn else 0.0)
            obs, r, done, _ = env.step(a)
            R.append(r); D.append(done)
            if done:
                obs = env.reset()
        z = lambda L: np.asarray(L, np.float32)
        return Trajectory(z(O), z(A), np.zeros(n_steps, np.float32), z(R), z(V), z(D))


def make_backend(kind, obs_dim, act_dim, workers, hidden=(256, 256)):
    if kind == "cpu":
        return CPUBackend(obs_dim, act_dim, workers=workers, hidden=hidden)
    if kind == "mjx":
        raise RuntimeError("mjx backend is a stretch target and not implemented on this path; "
                           "set SIM_BACKEND=cpu")
    raise ValueError(f"unknown SIM_BACKEND={kind!r}")
