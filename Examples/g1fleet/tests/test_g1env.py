import numpy as np
from g1fleet.g1env import G1Env

def test_reset_returns_finite_obs():
    env = G1Env(seed=0)
    obs = env.reset()
    assert obs.shape == (env.obs_dim,) and np.all(np.isfinite(obs))

def test_step_contract_and_finiteness():
    env = G1Env(seed=0); env.reset()
    a = np.zeros(env.act_dim, dtype=np.float32)
    obs, rew, done, info = env.step(a)
    assert obs.shape == (env.obs_dim,)
    assert np.isfinite(rew) and isinstance(done, bool)

def test_determinism_same_seed_same_trajectory():
    def roll():
        env = G1Env(seed=7); env.reset(seed=7)
        rs = []
        a = np.zeros(env.act_dim, dtype=np.float32)
        for _ in range(20):
            _, r, d, _ = env.step(a); rs.append(r)
            if d: break
        return rs
    assert roll() == roll()

def test_episode_terminates_by_horizon_or_fall():
    env = G1Env(seed=0); env.reset()
    a = np.zeros(env.act_dim, dtype=np.float32)
    done = False
    for _ in range(env.EPISODE_STEPS + 5):
        _, _, done, _ = env.step(a)
        if done: break
    assert done


def test_cpu_backend_evaluate_returns_shape():
    from g1fleet.g1env import G1Env
    from g1fleet.policy import MLPPolicy
    from g1fleet.rollout import CPUBackend
    e = G1Env(); p = MLPPolicy(e.obs_dim, e.act_dim, hidden=(16, 16))
    be = CPUBackend(e.obs_dim, e.act_dim, workers=2)
    import numpy as np
    v = p.get_flat()
    out = be.evaluate_returns([v, v * 0.0], seeds=[1, 2])
    assert out.shape == (2,) and np.all(np.isfinite(out))

def test_cpu_backend_collect_trajectory_lengths():
    from g1fleet.g1env import G1Env
    from g1fleet.policy import MLPPolicy
    from g1fleet.rollout import CPUBackend
    e = G1Env(); p = MLPPolicy(e.obs_dim, e.act_dim, hidden=(16, 16))
    be = CPUBackend(e.obs_dim, e.act_dim, workers=1)
    tr = be.collect_trajectory(p.get_flat(), n_steps=32, seed=0)
    assert tr.obs.shape[0] == 32 and tr.rewards.shape[0] == 32
