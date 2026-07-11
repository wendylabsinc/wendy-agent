#!/usr/bin/env python3
"""G1FleetPPO entrypoint. ROLE=learner|actor dispatch over the mesh."""
import os
from g1fleet.mesh import MeshConfig
from g1fleet.g1env import G1Env
from g1fleet import ppo

def main():
    cfg = MeshConfig.from_env()
    env = G1Env()  # build once to learn dims (cheap)
    obs_dim, act_dim = env.obs_dim, env.act_dim
    del env
    print(f"[g1fleet-ppo] role={cfg.role} self={cfg.self_id} peers={cfg.peers} backend={cfg.backend}", flush=True)
    if cfg.role == "learner":
        ppo.run_learner(cfg, obs_dim, act_dim)
    else:
        ppo.run_actor(cfg, obs_dim, act_dim)

if __name__ == "__main__":
    main()
