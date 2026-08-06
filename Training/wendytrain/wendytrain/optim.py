"""Adam optimizer as a pure function over NumPy arrays.

The optimizer state is a plain dictionary of arrays so it checkpoints through
``wendytrain.wire`` unchanged. Convention: ``m`` and ``v`` are the first and
second moment estimates with ``theta``'s shape and dtype, and ``t`` is the
step count stored as a zero-dimensional ``int64`` array (the wire format
carries arrays, not integers). Restoring a checkpointed state and continuing
produces the same trajectory as never stopping; losing this state was the
headline resume defect this library fixes.
"""

import numpy as np


def adam_step(
    theta: np.ndarray,
    grad: np.ndarray,
    state: dict | None,
    lr: float = 1e-2,
    b1: float = 0.9,
    b2: float = 0.999,
    eps: float = 1e-8,
    maximize: bool = False,
) -> tuple[np.ndarray, dict]:
    """Apply one Adam update and return ``(new_theta, new_state)``.

    Pass ``state=None`` on the first call; every later call passes the state
    returned by the previous one (or one restored from a checkpoint). Inputs
    are never mutated; the returned state is a fresh dictionary. With
    ``maximize=True`` the step ascends the gradient instead of descending it,
    which is the natural mode for Evolution Strategies return estimates.
    """
    theta = np.asarray(theta)
    grad = np.asarray(grad, dtype=theta.dtype)
    if state is None:
        m = np.zeros_like(theta)
        v = np.zeros_like(theta)
        t = 0
    else:
        m = np.asarray(state["m"])
        v = np.asarray(state["v"])
        t = int(state["t"])
    if maximize:
        grad = -grad
    t += 1
    m = b1 * m + (1.0 - b1) * grad
    v = b2 * v + (1.0 - b2) * np.square(grad)
    m_hat = m / (1.0 - b1**t)
    v_hat = v / (1.0 - b2**t)
    new_theta = theta - lr * m_hat / (np.sqrt(v_hat) + eps)
    return new_theta.astype(theta.dtype), {
        "m": m.astype(theta.dtype),
        "v": v.astype(theta.dtype),
        "t": np.array(t, dtype=np.int64),
    }
