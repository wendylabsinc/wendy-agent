"""Continuous-action cart-pole, pure NumPy, deterministic under seed.

The built-in environment for the training templates. It exists so every
template has a real, fast control problem with no simulator dependency; it is
not a benchmark. Dynamics follow the classic cart-pole equations (Barto, Sutton
and Anderson, 1983) with a continuous force input instead of the bang-bang
action of the original.

Interface (matched by all templates):
    env = CartPole(seed=0)
    obs = env.reset(seed=0)           # float32, shape (4,)
    obs, reward, done, info = env.step(action)   # action: array-like, shape (1,)

Observation: [cart position, cart velocity, pole angle, pole angular velocity].
Action: force in [-1, 1], scaled internally to +/- 10 Newton; values outside
the range are clipped. Reward: 1.0 per step survived, minus 0.05 * action^2 as
an effort penalty. Episode ends when |position| > 2.4 m, |angle| > 12 degrees,
or after 500 steps.
"""
from __future__ import annotations

import math

import numpy as np

GRAVITY_M_S2 = 9.8
CART_MASS_KG = 1.0
POLE_MASS_KG = 0.1
POLE_HALF_LENGTH_M = 0.5
FORCE_SCALE_N = 10.0
TIME_STEP_S = 0.02
POSITION_LIMIT_M = 2.4
ANGLE_LIMIT_RAD = 12.0 * math.pi / 180.0
MAX_EPISODE_STEPS = 500


class CartPole:
    obs_dim = 4
    act_dim = 1

    def __init__(self, seed: int = 0):
        self._rng = np.random.default_rng(seed)
        self._state = np.zeros(4, dtype=np.float64)
        self._steps = 0

    def reset(self, seed: int | None = None) -> np.ndarray:
        if seed is not None:
            self._rng = np.random.default_rng(seed)
        self._state = self._rng.uniform(-0.05, 0.05, size=4)
        self._steps = 0
        return self._state.astype(np.float32)

    def step(self, action) -> tuple[np.ndarray, float, bool, dict]:
        force = float(np.clip(np.asarray(action, dtype=np.float64).reshape(-1)[0], -1.0, 1.0))
        x, x_dot, theta, theta_dot = self._state

        total_mass = CART_MASS_KG + POLE_MASS_KG
        pole_mass_length = POLE_MASS_KG * POLE_HALF_LENGTH_M
        f = force * FORCE_SCALE_N
        cos_t = math.cos(theta)
        sin_t = math.sin(theta)

        temp = (f + pole_mass_length * theta_dot**2 * sin_t) / total_mass
        theta_acc = (GRAVITY_M_S2 * sin_t - cos_t * temp) / (
            POLE_HALF_LENGTH_M * (4.0 / 3.0 - POLE_MASS_KG * cos_t**2 / total_mass)
        )
        x_acc = temp - pole_mass_length * theta_acc * cos_t / total_mass

        x = x + TIME_STEP_S * x_dot
        x_dot = x_dot + TIME_STEP_S * x_acc
        theta = theta + TIME_STEP_S * theta_dot
        theta_dot = theta_dot + TIME_STEP_S * theta_acc
        self._state = np.array([x, x_dot, theta, theta_dot], dtype=np.float64)
        self._steps += 1

        failed = abs(x) > POSITION_LIMIT_M or abs(theta) > ANGLE_LIMIT_RAD
        done = failed or self._steps >= MAX_EPISODE_STEPS
        reward = 1.0 - 0.05 * force * force if not failed else 0.0
        return self._state.astype(np.float32), float(reward), bool(done), {"steps": self._steps}
