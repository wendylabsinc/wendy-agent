"""Tests for Evolution Strategies math with combined rank normalization."""

import numpy as np

from wendytrain.es import gradient, perturbation, rank_normalize_combined


def test_perturbation_is_deterministic_float32():
    a = perturbation(7, 16)
    b = perturbation(7, 16)
    assert a.dtype == np.float32
    assert a.shape == (16,)
    assert np.array_equal(a, b)
    assert np.array_equal(a, np.random.default_rng(7).standard_normal(16).astype(np.float32))


def test_different_seeds_differ():
    assert not np.array_equal(perturbation(1, 8), perturbation(2, 8))


def test_rank_normalization_is_over_the_combined_set():
    rp = np.array([1.0, 2.0], np.float32)
    rm = np.array([100.0, 200.0], np.float32)
    nrp, nrm = rank_normalize_combined(rp, rm)
    # Combined ranks are [0,1,2,3] -> [-0.5,-1/6,1/6,0.5]; per-set
    # normalization would wrongly give both sets the same values.
    assert np.allclose(nrp, [-0.5, -1 / 6], atol=1e-6)
    assert np.allclose(nrm, [1 / 6, 0.5], atol=1e-6)


def test_rank_normalization_output_bounds_and_shapes():
    rng = np.random.default_rng(3)
    rp = rng.standard_normal(20).astype(np.float32)
    rm = rng.standard_normal(20).astype(np.float32)
    nrp, nrm = rank_normalize_combined(rp, rm)
    assert nrp.shape == rp.shape and nrm.shape == rm.shape
    combined = np.concatenate([nrp, nrm])
    assert combined.min() == -0.5
    assert combined.max() == 0.5


def test_es_gradient_climbs_a_quadratic():
    # theta near 0, f(x) = -|x|^2, ES gradient must point toward 0.
    rng = np.random.default_rng(0)
    theta = rng.standard_normal(8).astype(np.float32)
    seeds = list(range(64))
    sigma = 0.1
    rp = [-(np.linalg.norm(theta + sigma * perturbation(s, 8)) ** 2) for s in seeds]
    rm = [-(np.linalg.norm(theta - sigma * perturbation(s, 8)) ** 2) for s in seeds]
    g = gradient(np.array(rp), np.array(rm), seeds, 8, sigma)
    assert np.dot(g, -theta) > 0  # ascent direction reduces |theta|


def test_gradient_shape_and_dtype():
    seeds = [3, 5, 9]
    g = gradient(np.array([1.0, 2.0, 3.0]), np.array([0.5, 1.5, 2.5]), seeds, 12, 0.1)
    assert g.shape == (12,)
    assert g.dtype == np.float32
