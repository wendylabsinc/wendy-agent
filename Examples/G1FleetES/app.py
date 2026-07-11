#!/usr/bin/env python3
"""G1FleetES entrypoint. ROLE=coordinator|worker dispatch over the mesh.

The coordinator double-duties: it serves /params + /returns AND runs a local
worker loop dialing 127.0.0.1, so every device in the fleet contributes rollouts.
Without this, the coordinator's own slice of the ES population would never be
evaluated and every generation would limp to the dead-peer timeout.
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
    env = G1Env()  # build once to learn dims (cheap)
    obs_dim, act_dim = env.obs_dim, env.act_dim
    del env
    print(f"[g1fleet-es] role={cfg.role} self={cfg.self_id} peers={cfg.peers} backend={cfg.backend}", flush=True)

    if cfg.role == "coordinator":
        # Serve the coordinator in the background...
        threading.Thread(
            target=es.run_coordinator, args=(cfg, obs_dim, act_dim), daemon=True
        ).start()
        time.sleep(1.0)  # let the HTTP server bind before the local worker dials
        # ...and also work: dial ourselves over loopback so this device's slice
        # of the ES population gets evaluated like any other worker.
        local = replace(cfg, role="worker", peers=[f"127.0.0.1:{cfg.port}"])
        print(f"[g1fleet-es] coordinator also running local worker -> {local.peers[0]}", flush=True)
        es.run_worker(local, obs_dim, act_dim)
    else:
        es.run_worker(cfg, obs_dim, act_dim)


if __name__ == "__main__":
    main()
