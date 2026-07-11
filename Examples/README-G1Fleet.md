# G1 Fleet — Distributed Reinforcement Learning over the WendyOS Mesh

Two example apps that train a **Unitree G1** locomotion policy with genuinely
**distributed compute**: the work is split across a fleet of WendyOS devices that
discover and talk to each other over the **WendyOS mesh**. Each device runs the
same container; the mesh wires them into one training system.

- **`G1FleetES/`** — Evolution Strategies. Mesh-light and embarrassingly
  parallel: the coordinator broadcasts one parameter vector, each device
  evaluates a slice of mirrored perturbations and returns *scalars*. Best
  "add a device → train faster" demonstration.
- **`G1FleetPPO/`** — actor–learner PPO. The learner holds the policy and serves
  weights; actors pull weights, roll out episodes, and POST trajectories back for
  gradient updates.

Both share a vendored core package **`g1fleet/`** (env, policy, rollout backend,
mesh wiring, wire codec, ES + PPO drivers). The canonical source lives in
`Examples/g1fleet/`; `scripts/sync_g1fleet.sh` copies it into each app's build
context. Unit tests live in `Examples/g1fleet/tests/` (run with `pytest`).

## The task

A reduced, edge-CPU-tractable G1 objective: **velocity tracking + stay upright**.
Observation = joint positions/velocities + home stance; action = PD targets
around the home stance (clamped to actuator ranges, like
`HelloPython/mujoco_g1.py`); reward rewards forward velocity near a target and
staying near standing height, penalizes control effort and falling. The
deliverable is a *genuinely distributed training loop whose mean return climbs* —
not a fully solved gait (see "Scope").

## How the mesh is used

One entitlement block per service (see each `wendy.json`):

```json
{ "type": "network", "mode": "mesh", "serviceCIDR": "10.99.0.0/16",
  "ports": [{ "host": 8080, "container": 8080 }] }
```

- `isolation: "isolated"` + `mode: "mesh"` gives each container its own network
  namespace and grants egress to the mesh service CIDR, so it can dial peers.
- Peers are addressed as `device-<assetId>.cloud.wendy.dev:8080`. On a shared LAN
  the mesh peers **LAN-direct**; otherwise it relays via the cloud broker.
- `ports` publishes the container's 8080 so peers can reach this device's server.

> **Mesh mode has no internet egress** — only the mesh CIDR. That is why the G1
> model is **vendored into the image at build time** (`/opt/g1_model`, loaded via
> `G1_MODEL_DIR`); nothing is fetched at runtime.

## Deploy

Set per-device env at deploy time (expanded from your shell into each
`wendy.json` `env` block). Asset ids for this fleet: **Spark1=284, Spark2=283,
Spark3=211**.

### ES

```bash
cd Examples/G1FleetES
../../scripts/sync_g1fleet.sh   # refresh the vendored core

ROLE=coordinator MESH_SELF=284 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu ES_POP=60 wendy cloud run --device 284 --detach
ROLE=worker      MESH_SELF=283 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu ES_POP=60 wendy cloud run --device 283 --detach
ROLE=worker      MESH_SELF=211 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu ES_POP=60 wendy cloud run --device 211 --detach
```

The coordinator (284) double-duties: it serves `/params`+`/returns` **and** runs a
local worker, so every device contributes rollouts.

### PPO

```bash
cd Examples/G1FleetPPO
../../scripts/sync_g1fleet.sh

ROLE=learner MESH_SELF=284 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 284 --detach
ROLE=actor   MESH_SELF=283 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 283 --detach
ROLE=actor   MESH_SELF=211 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 211 --detach
```

> **Deploy note:** the container-create step over the cloud tunnel can
> intermittently fail with `Secure connection dropped during the TLS handshake`.
> The image push has already completed at that point — just re-run the same
> `wendy cloud run` and it proceeds to create the container.

## Watch it train

```bash
wendy cloud device logs --device 284      # coordinator (ES) / learner (PPO)
```

ES coordinator logs (verified on the Spark fleet):

```
[g1fleet-es] gen=12 mean_return=-1.5036 best=3.3987 n_seeds=60/60
[g1fleet-es] gen=15 mean_return=-0.6777 best=3.8136 n_seeds=60/60
[g1fleet-es] gen=16 mean_return=-0.8601 best=4.8930 n_seeds=60/60
```

`n_seeds=60/60` means all three devices contributed their full slice that
generation; `mean_return`/`best` climbing means the policy is improving. The
per-device dashboard **Mesh** tab shows the live peer connections, LAN-direct vs
relay split, and per-peer bytes/latency.

## Configuration (env vars)

| Var | Meaning | Default |
|---|---|---|
| `ROLE` | `coordinator`/`worker` (ES) or `learner`/`actor` (PPO) | `worker` |
| `MESH_SELF` | this device's asset id (skips dialing itself) | — |
| `LEARNER_ID` | coordinator/learner asset id | — |
| `MESH_PEERS` | comma-separated fleet asset ids | — |
| `SIM_BACKEND` | `cpu` (guaranteed) or `mjx` (GPU stretch, not on critical path) | `cpu` |
| `ES_POP` | ES population size (mirrored) | 60 |
| `POLICY_HIDDEN` | MLP hidden layers, e.g. `256,256` | `256,256` |
| `CKPT_DIR` | checkpoint dir (persist volume) | `/data/checkpoints` |

PPO also reads `TRAIN_BATCH`, `PPO_ROLLOUT_STEPS`, `PPO_EPOCHS`, `PPO_MINIBATCH`,
`PPO_CLIP`, `PPO_LR`, `MAX_STALENESS`.

## Simulation backend

`SIM_BACKEND=cpu` runs the MuJoCo C engine across the device's CPU cores — the
guaranteed, verified path. `SIM_BACKEND=mjx` (JAX/GPU-batched) is a **stretch
target**: on Blackwell (`sm_121`) + CUDA 13 the JAX toolchain is bleeding-edge, so
it is intentionally off the critical path and currently raises if selected.

## Scope

The deliverable is a working distributed loop with a climbing reward curve, not a
research-grade solved gait. A fully-trained walking policy would want the GPU/MJX
path, more devices, and far longer training. See
`specs/2026-07-11-g1-fleet-distributed-rl-design.md` and `-plan.md` for the full
design, decisions, and task breakdown.
