"""Reinforcement learning math: Generalized Advantage Estimation (GAE).

Pure NumPy; framework-neutral. Callers using PyTorch or JAX convert to and
from arrays at the boundary.
"""

import numpy as np


def gae(
    rewards: np.ndarray,
    values: np.ndarray,
    dones: np.ndarray,
    gamma: float = 0.99,
    lam: float = 0.95,
) -> tuple[np.ndarray, np.ndarray]:
    """Compute GAE advantages and value targets for one flat batch.

    ``rewards``, ``values``, and ``dones`` have the same length; ``dones[t]``
    is truthy when step ``t`` ends an episode. The recursion is

        delta_t = rewards[t] + gamma * values[t + 1] * (1 - dones[t]) - values[t]
        advantage_t = delta_t + gamma * lam * (1 - dones[t]) * advantage_{t + 1}

    with a bootstrap value of 0 after a done step and past the end of the
    batch; callers who want a non-zero tail bootstrap append that step
    themselves. Returns ``(advantages, returns)`` as float64 arrays, where
    ``returns = advantages + values``.
    """
    rewards = np.asarray(rewards, dtype=np.float64)
    values = np.asarray(values, dtype=np.float64)
    dones = np.asarray(dones, dtype=np.float64)
    if not (rewards.shape == values.shape == dones.shape):
        raise ValueError(
            f"rewards, values, and dones must have the same shape: got "
            f"{rewards.shape}, {values.shape}, {dones.shape}"
        )
    n = rewards.size
    advantages = np.zeros(n, dtype=np.float64)
    carry = 0.0
    for t in range(n - 1, -1, -1):
        not_done = 1.0 - dones[t]
        next_value = values[t + 1] if t + 1 < n else 0.0
        delta = rewards[t] + gamma * next_value * not_done - values[t]
        carry = delta + gamma * lam * not_done * carry
        advantages[t] = carry
    return advantages, advantages + values
