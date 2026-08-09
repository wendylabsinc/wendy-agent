"""Tests for Generalized Advantage Estimation."""

import numpy as np

from wendytrain.rl import gae


def test_three_step_hand_computed_example():
    # gamma = 0.5, lam = 0.5, terminal bootstrap value 0 after the last step.
    rewards = np.array([1.0, 1.0, 1.0])
    values = np.array([0.5, 0.4, 0.3])
    dones = np.array([0.0, 0.0, 1.0])
    advantages, returns = gae(rewards, values, dones, gamma=0.5, lam=0.5)
    # delta_2 = 1 - 0.3 = 0.7 (done, so no bootstrap); A_2 = 0.7
    # delta_1 = 1 + 0.5 * 0.3 - 0.4 = 0.75; A_1 = 0.75 + 0.25 * 0.7 = 0.925
    # delta_0 = 1 + 0.5 * 0.4 - 0.5 = 0.7; A_0 = 0.7 + 0.25 * 0.925 = 0.93125
    assert np.allclose(advantages, [0.93125, 0.925, 0.7], atol=1e-6)
    assert np.allclose(returns, advantages + values, atol=1e-6)


def test_done_cuts_the_recursion():
    rewards = np.array([1.0, 1.0, 1.0])
    values = np.array([0.5, 0.4, 0.3])
    dones = np.array([0.0, 1.0, 0.0])
    advantages, _ = gae(rewards, values, dones, gamma=0.5, lam=0.5)
    # Step 1 ends an episode: delta_1 = 1 - 0.4 = 0.6 with no bootstrap and no
    # carry from A_2; without the cut it would be 0.925.
    assert np.allclose(advantages[1], 0.6, atol=1e-6)
    # Step 2 starts a new episode and bootstraps 0 past the end of the batch.
    assert np.allclose(advantages[2], 0.7, atol=1e-6)
    # Step 0 still chains into step 1.
    assert np.allclose(advantages[0], 0.7 + 0.25 * 0.6, atol=1e-6)


def test_defaults_match_standard_gamma_and_lambda():
    rewards = np.array([1.0, 0.0])
    values = np.array([0.0, 0.0])
    dones = np.array([0.0, 1.0])
    advantages, returns = gae(rewards, values, dones)
    # delta_1 = 0; A_1 = 0; delta_0 = 1; A_0 = 1 + 0.99 * 0.95 * 0 = 1.
    assert np.allclose(advantages, [1.0, 0.0], atol=1e-6)
    assert np.allclose(returns, [1.0, 0.0], atol=1e-6)


def test_shapes_match_input():
    n = 17
    rng = np.random.default_rng(0)
    advantages, returns = gae(
        rng.standard_normal(n), rng.standard_normal(n), np.zeros(n)
    )
    assert advantages.shape == (n,)
    assert returns.shape == (n,)
