"""Tests for the Adam optimizer step."""

import numpy as np

from wendytrain import wire
from wendytrain.optim import adam_step


def test_first_step_moves_against_the_gradient():
    theta = np.zeros(3, dtype=np.float32)
    grad = np.array([1.0, -1.0, 0.5], dtype=np.float32)
    new_theta, state = adam_step(theta, grad, None, lr=0.01)
    assert new_theta[0] < 0 and new_theta[1] > 0 and new_theta[2] < 0
    assert int(state["t"]) == 1


def test_maximize_moves_along_the_gradient():
    theta = np.zeros(3, dtype=np.float32)
    grad = np.array([1.0, -1.0, 0.5], dtype=np.float32)
    new_theta, _ = adam_step(theta, grad, None, lr=0.01, maximize=True)
    assert new_theta[0] > 0 and new_theta[1] < 0 and new_theta[2] > 0


def test_adam_converges_on_a_quadratic_in_200_steps():
    # Minimize f(theta) = |theta|^2 with gradient 2 * theta.
    theta = np.array([1.0, -2.0, 3.0], dtype=np.float32)
    start_norm = float(np.linalg.norm(theta))
    state = None
    for _ in range(200):
        theta, state = adam_step(theta, 2.0 * theta, state, lr=0.05, maximize=False)
    final_norm = float(np.linalg.norm(theta))
    assert final_norm < 0.1
    assert final_norm < 0.05 * start_norm
    assert int(state["t"]) == 200


def test_state_round_trips_through_wire():
    theta = np.ones(4, dtype=np.float32)
    state = None
    for _ in range(3):
        theta, state = adam_step(theta, 2.0 * theta, state, lr=0.01)
    blob = wire.encode(state)
    restored, _ = wire.decode(blob)
    # The convention: m and v are float arrays, t is a zero-dimensional int64 array.
    assert restored["t"].shape == ()
    assert restored["t"].dtype == np.int64
    assert int(restored["t"]) == 3
    theta_a, state_a = adam_step(theta.copy(), 2.0 * theta, state, lr=0.01)
    theta_b, state_b = adam_step(theta.copy(), 2.0 * theta, restored, lr=0.01)
    assert np.allclose(theta_a, theta_b, atol=0)
    assert int(state_a["t"]) == int(state_b["t"]) == 4


def test_step_does_not_mutate_inputs():
    theta = np.ones(4, dtype=np.float32)
    grad = np.full(4, 2.0, dtype=np.float32)
    _, state = adam_step(theta, grad, None)
    theta_before = theta.copy()
    grad_before = grad.copy()
    m_before = state["m"].copy()
    adam_step(theta, grad, state)
    assert np.array_equal(theta, theta_before)
    assert np.array_equal(grad, grad_before)
    assert np.array_equal(state["m"], m_before)
    assert int(state["t"]) == 1
