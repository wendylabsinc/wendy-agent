import numpy as np
from g1fleet.ppo import compute_gae

def test_gae_matches_discounted_return_when_lambda_one_no_bootstrap():
    r = np.array([1.0, 1.0, 1.0], np.float32)
    v = np.zeros(3, np.float32); d = np.array([0, 0, 1], np.float32)
    adv, ret = compute_gae(r, v, d, gamma=1.0, lam=1.0)
    # returns = reverse cumulative sum when values=0, gamma=1
    assert np.allclose(ret, [3.0, 2.0, 1.0], atol=1e-5)
    assert np.allclose(adv, ret, atol=1e-5)  # adv = ret - v, v=0
