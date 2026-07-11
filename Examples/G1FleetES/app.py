#!/usr/bin/env python3
"""G1FleetES entrypoint. ROLE=coordinator|worker dispatch over the mesh."""
import os
from g1fleet.mesh import MeshConfig
from g1fleet.g1env import G1Env
from g1fleet import es

def main():
    cfg = MeshConfig.from_env()
    env = G1Env()  # build once to learn dims (cheap)
    obs_dim, act_dim = env.obs_dim, env.act_dim
    del env
    print(f"[g1fleet-es] role={cfg.role} self={cfg.self_id} peers={cfg.peers} backend={cfg.backend}", flush=True)
    if cfg.role == "coordinator":
        es.run_coordinator(cfg, obs_dim, act_dim)
    else:
        es.run_worker(cfg, obs_dim, act_dim)

if __name__ == "__main__":
    main()
