import numpy as np
from g1fleet.policy import MLPPolicy, TorchMLP

def test_flat_roundtrip_is_identity():
    p = MLPPolicy(obs_dim=10, act_dim=4, hidden=(8, 8))
    v = p.get_flat()
    assert v.dtype == np.float32 and v.ndim == 1 and v.size == p.num_params()
    v2 = v.copy(); v2[:] = np.arange(v.size, dtype=np.float32)
    p.set_flat(v2)
    assert np.array_equal(p.get_flat(), v2)

def test_act_shape_and_bounds():
    p = MLPPolicy(obs_dim=10, act_dim=4, hidden=(8, 8))
    a = p.act(np.zeros(10, dtype=np.float32))
    assert a.shape == (4,) and np.all(np.abs(a) <= 1.0 + 1e-6)

def test_numpy_and_torch_agree():
    p = MLPPolicy(obs_dim=6, act_dim=3, hidden=(8, 8))
    t = TorchMLP(obs_dim=6, act_dim=3, hidden=(8, 8))
    t.set_flat(p.get_flat())
    obs = np.random.default_rng(0).standard_normal(6).astype(np.float32)
    import torch
    with torch.no_grad():
        ta = t(torch.from_numpy(obs)[None]).numpy()[0]
    assert np.allclose(p.act(obs), ta, atol=1e-5)
