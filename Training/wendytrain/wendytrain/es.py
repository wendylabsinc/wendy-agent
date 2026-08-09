"""Evolution Strategies (ES) math: mirrored sampling with combined rank normalization.

Pure NumPy functions. Workers regenerate perturbations from integer seeds, so
only seeds and scalar returns cross the wire. Rank normalization runs over the
combined plus and minus return sets; normalizing each set separately (the
defect found in pull request #1423) erases the very differences that carry the
gradient signal.
"""

import numpy as np


def perturbation(seed: int, n: int) -> np.ndarray:
    """The deterministic float32 Gaussian perturbation for ``seed``.

    Both sides of a mirrored pair use the same seed: the plus side evaluates
    ``theta + sigma * perturbation(seed, n)`` and the minus side subtracts it.
    """
    return np.random.default_rng(seed).standard_normal(n).astype(np.float32)


def rank_normalize_combined(
    returns_plus: np.ndarray, returns_minus: np.ndarray
) -> tuple[np.ndarray, np.ndarray]:
    """Rank-normalize the two return sets over their concatenation.

    All ``2 * n`` returns are ranked together and mapped linearly onto
    ``[-0.5, 0.5]`` (lowest return to -0.5, highest to 0.5), then split back
    into the plus and minus halves. Ranking over the combined set preserves
    the ordering between a perturbation's two sides, which is exactly the
    signal the mirrored difference needs.
    """
    returns_plus = np.asarray(returns_plus, dtype=np.float64)
    returns_minus = np.asarray(returns_minus, dtype=np.float64)
    combined = np.concatenate([returns_plus, returns_minus])
    total = combined.size
    if total < 2:
        raise ValueError("rank normalization needs at least two returns")
    ranks = np.empty(total, dtype=np.float64)
    ranks[np.argsort(combined, kind="stable")] = np.arange(total, dtype=np.float64)
    normalized = ranks / (total - 1) - 0.5
    n = returns_plus.size
    return normalized[:n], normalized[n:]


def gradient(
    returns_plus: np.ndarray,
    returns_minus: np.ndarray,
    seeds: list[int],
    num_params: int,
    sigma: float,
) -> np.ndarray:
    """Estimate the ES gradient of expected return (an ascent direction).

    Uses combined rank normalization and mirrored differences:

        g = (1 / (n * sigma)) * sum_i 0.5 * (nr_plus_i - nr_minus_i) * eps_i

    where ``eps_i = perturbation(seeds[i], num_params)`` and ``n`` is the
    number of mirrored pairs. Feed the result to an optimizer in maximize
    mode, for example ``optim.adam_step(theta, g, state, maximize=True)``.
    """
    returns_plus = np.asarray(returns_plus)
    returns_minus = np.asarray(returns_minus)
    n = len(seeds)
    if not (returns_plus.size == returns_minus.size == n):
        raise ValueError(
            f"gradient needs one plus and one minus return per seed: got "
            f"{returns_plus.size} plus, {returns_minus.size} minus, {n} seeds"
        )
    if sigma <= 0:
        raise ValueError(f"sigma must be positive, got {sigma}")
    nr_plus, nr_minus = rank_normalize_combined(returns_plus, returns_minus)
    weights = 0.5 * (nr_plus - nr_minus)
    total = np.zeros(num_params, dtype=np.float64)
    for weight, seed in zip(weights, seeds):
        total += weight * perturbation(seed, num_params)
    return (total / (n * sigma)).astype(np.float32)
