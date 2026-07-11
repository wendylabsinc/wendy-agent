import numpy as np
from g1fleet.es import es_gradient, adam_step

def test_es_ascends_toy_quadratic():
    # maximize f(x) = -||x - target||^2 ; ES gradient should move theta toward target
    rng = np.random.default_rng(0)
    n = 8; target = rng.standard_normal(n).astype(np.float32)
    theta = np.zeros(n, np.float32); sigma = 0.1; state = None
    def f(x): return -float(np.square(x - target).sum())
    for gen in range(60):
        pop, seeds = 40, list(range(gen * 40, gen * 40 + 40))
        rp, rm = [], []
        for s in seeds:
            eps = np.random.default_rng(s).standard_normal(n).astype(np.float32)
            rp.append(f(theta + sigma * eps)); rm.append(f(theta - sigma * eps))
        g = es_gradient(np.array(rp), np.array(rm), seeds, n, sigma)
        theta, state = adam_step(theta, g, state, lr=0.05)
    assert np.linalg.norm(theta - target) < np.linalg.norm(target) * 0.5
