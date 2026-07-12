#!/usr/bin/env python3
"""G1FleetESGpu entrypoint — identical control flow to G1FleetES, but the worker
backend is GPU (SIM_BACKEND=warp -> mujoco_warp + CUDA graphs). The coordinator
double-duties as a local worker so its GPU contributes rollouts too.
"""
import os
import threading
import time
from dataclasses import replace

from g1fleet.mesh import MeshConfig
from g1fleet.g1env import G1Env
from g1fleet import es


def main():
    cfg = MeshConfig.from_env()
    env = G1Env()  # build once to learn dims (cheap; CPU model load)
    obs_dim, act_dim = env.obs_dim, env.act_dim
    del env
    print(f"[g1fleet-es] role={cfg.role} self={cfg.self_id} peers={cfg.peers} backend={cfg.backend}", flush=True)

    if cfg.role == "coordinator":
        threading.Thread(
            target=es.run_coordinator, args=(cfg, obs_dim, act_dim), daemon=True
        ).start()
        time.sleep(1.0)
        local = replace(cfg, role="worker", peers=[f"127.0.0.1:{cfg.port}"])
        print(f"[g1fleet-es] coordinator also running local worker -> {local.peers[0]}", flush=True)
        es.run_worker(local, obs_dim, act_dim)
    else:
        es.run_worker(cfg, obs_dim, act_dim)


if __name__ == "__main__":
    main()
