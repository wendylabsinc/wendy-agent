# Distributed G1 Locomotion RL on a WendyOS Mesh Fleet

**Date:** 2026-07-11
**Status:** Approved design, pending implementation plan
**Fleet:** Spark1 (asset 284), Spark2 (283), Spark3 (211) — all in Wendy Cloud

## Goal

Train a Unitree G1 locomotion policy with **genuinely distributed compute**: the
work is split across three physical WendyOS devices that discover and talk to
each other over the **WendyOS mesh**. Ship it as WendyOS containers and deploy
with `wendy run`.

Two deployable apps demonstrate two distributed-RL architectures over the same
mesh substrate and the same G1 task:

- **`G1FleetES`** — Evolution Strategies (mesh-light, embarrassingly parallel).
- **`G1FleetPPO`** — actor–learner PPO (weight broadcast + experience upload).

**Success bar (pass/fail):** a genuinely distributed training loop over the real
mesh where the **mean episode return visibly climbs** across generations/updates,
and adding a device increases throughput. A fully solved walking gait is a
**stretch goal**, not the bar.

## Fleet hardware (measured 2026-07-11)

All three Sparks report identically:

| Property | Value |
|---|---|
| CPU | ARM64, 20 cores |
| Memory | 128 GB unified |
| GPU | NVIDIA Blackwell, `sm_121`, CUDA 13.0.3 |
| OS | Ubuntu 24.04 |
| LAN | 192.168.0.24 / .132 / .46 — **same subnet** |

Consequences:

- Same subnet ⇒ mesh peers **LAN-direct** (no broker relay needed). Confirm in
  each device's dashboard **Mesh** tab.
- 20 CPU cores × 3 = 60 parallel CPU envs available fleet-wide — enough for the
  reward curve to climb on the CPU backend alone.
- GPU is Blackwell + CUDA 13 (bleeding edge). **JAX/MJX on `sm_121` is a known
  containerization risk.** The design does not let E2E verification depend on it.

## Key decisions (from brainstorming)

1. **Pluggable simulation backend.** Ship on the guaranteed **CPU** path
   (MuJoCo C engine). **MJX/GPU** is an optional accelerator switched on by env
   var, pursued only as a stretch after the CPU loop is verified E2E.
2. **Reduced G1 task:** velocity-tracking + stay-upright (not full gait from
   scratch), which is tractable on CPU and produces a visibly climbing curve.
3. **Two apps sharing one vendored core package** (`g1fleet/`).

## Architecture

### Shared core package: `g1fleet/`

Vendored into each app's build context (copied, not pip-installed, to keep
images self-contained and offline-buildable). Modules:

- **`g1env.py`** — the G1 locomotion environment.
  - Model: `unitree_g1` from MuJoCo Menagerie (same load path as the existing
    `Examples/HelloPython/mujoco_g1.py`), reset to keyframe 0 (home stance).
  - **Observation** (per step): joint positions, joint velocities, base
    orientation (quaternion or projected gravity), base linear/angular velocity,
    and previous action. Flat `float32` vector.
  - **Action:** PD targets around the home actuator stance; policy outputs a
    delta clamped to actuator control ranges (mirrors the clamping in
    `mujoco_g1.py`).
  - **Reward:** `w_v·(forward velocity tracking) + w_up·(upright) + alive_bonus
    − w_ctrl·‖action‖² − w_fall·fall`. Exact weights are tunable constants at the
    top of the module.
  - **Termination:** base height below a fall threshold, or fixed horizon
    (`EPISODE_STEPS`).
  - Control decimation: policy acts every K physics steps (K a constant).
  - Deterministic given `(params, seed)` so ES mirrored sampling is reproducible.
- **`policy.py`** — small fixed MLP (e.g. `[obs → 256 → 256 → act]`, tanh).
  - NumPy forward pass (ES workers need no autograd).
  - Torch module mirror with identical shapes for the PPO learner.
  - Flat parameter vector `get_flat()/set_flat()` for wire transport and ES math.
- **`rollout.py`** — one backend interface, two implementations:
  - `Backend.evaluate_returns(param_vectors, seeds) -> returns` (ES: scalar
    return per param vector).
  - `Backend.collect_trajectories(param_vector, n_steps, seed) -> Trajectory`
    (PPO: obs/action/logprob/reward/value/done arrays).
  - `CPUBackend` — MuJoCo C engine; fans envs across cores with a process pool.
  - `MJXBackend` — JAX-vectorized batched sim on GPU (stretch; guarded import).
  - Selected by `SIM_BACKEND=cpu|mjx` (default `cpu`).
- **`mesh.py`** — peer/role wiring from env vars, reusing HelloMesh's
  `parse_peers` normalization (bare id → `device-<id>.cloud.wendy.dev:<port>`).
  Exposes `self_id`, `learner_id`, `peers`, `role`, and small HTTP client helpers
  with retry/backoff for transient mesh-path warmup.
- **`netcodec.py`** — length-prefixed, gzip-compressed NumPy transport:
  `encode_params`, `decode_params`, `encode_trajectory`, `decode_trajectory`.
  Used as HTTP request/response bodies. Includes a shape/dtype header so the
  receiver validates before allocating.

### App A — `G1FleetES` (Evolution Strategies)

Roles: **Spark1 (284) = coordinator**; all three devices = workers (Spark1
double-duties). Chosen because ES has the smallest mesh footprint and is
trivially correct — the strongest "add a device = train faster" demonstration.

Per generation `g`:
1. Coordinator holds θ (flat params). Serves `GET /params` → `{generation, theta,
   sigma, base_seed}`.
2. Each worker pulls θ and computes its **assigned slice** of the population.
   Perturbation `i` uses a deterministic seed `base_seed + i`; **mirrored
   sampling** evaluates both `θ + σ·εᵢ` and `θ − σ·εᵢ`. Slice assignment is by
   worker index among sorted peers so slices are disjoint and cover `[0, N)`.
3. Worker evaluates returns via `CPUBackend.evaluate_returns` and POSTs
   `POST /returns` → `{generation, indices, returns_plus, returns_minus}`.
4. Coordinator waits until it has all `N` (with a per-generation timeout; missing
   workers' slices are skipped and logged, loop still advances), then computes
   the ES gradient estimate
   `g = 1/(N·σ) · Σ_i (F⁺ᵢ − F⁻ᵢ)/2 · εᵢ`
   with rank-normalized fitness, applies an **Adam** step to θ, increments
   `generation`, logs mean return, and checkpoints θ to the persistent volume.

Bandwidth: θ down (tens of KB for a small MLP), scalars up. Ideal over mesh.

`GET /status` returns `{generation, mean_return, best_return}` for quick polling.

### App B — `G1FleetPPO` (actor–learner)

Roles: **Spark1 (284) = learner** (runs gradient updates; GPU-preferred),
**Spark2/3 = actors**. Spark1 may also act if idle. Classic decoupled actor–learner.

- Learner:
  - Serves `GET /weights` → `{version, theta}` (version-stamped policy+value
    params).
  - Accepts `POST /rollout` → trajectory batch tagged with the `weights_version`
    it was collected under.
  - Aggregates uploaded experience into a training batch, runs PPO
    (GAE-λ advantages, clipped surrogate, value loss, entropy bonus) for a few
    epochs, bumps `version`, logs reward/KL/policy-loss/value-loss, checkpoints.
  - **Staleness policy:** batches whose `weights_version` lags the current
    version by more than `MAX_STALENESS` are dropped (logged); mild staleness is
    tolerated by PPO's ratio clipping. `MAX_STALENESS` is a constant.
- Actors:
  - Loop: `GET /weights`; collect `ROLLOUT_STEPS` of G1 experience via
    `CPUBackend.collect_trajectories`; `POST /rollout`; repeat.
  - On connection failure, back off and retry (mesh path may still be warming).

Bandwidth: trajectories up (larger than ES, fine over LAN-direct mesh),
weights down.

`GET /status` returns `{version, last_mean_return, updates}`.

## Packaging (both apps)

Directory layout (new, under `Examples/`):

```
Examples/G1FleetES/
  wendy.json
  Dockerfile
  requirements.txt
  app.py                 # role dispatch: coordinator vs worker
  g1fleet/               # vendored shared core (copied in)
Examples/G1FleetPPO/
  wendy.json
  Dockerfile
  requirements.txt
  app.py                 # role dispatch: learner vs actor
  g1fleet/               # vendored shared core (copied in)
Examples/g1fleet/        # canonical source of the shared core; synced into each app
```

The canonical `Examples/g1fleet/` is the source of truth; a short sync step (or
build-time copy) places it into each app's context. (Plan will pick the simplest
mechanism that keeps images self-contained; a committed copy in each app dir is
acceptable.)

### `wendy.json` (shape, both apps)

```json
{
  "appId": "sh.wendy.examples.g1fleet-es",
  "version": "1.0.0",
  "platform": "linux",
  "isolation": "isolated",
  "services": {
    "trainer": {
      "context": ".",
      "entitlements": [
        {
          "type": "network",
          "mode": "mesh",
          "serviceCIDR": "10.99.0.0/16",
          "ports": [{ "host": 8080, "container": 8080 }]
        },
        { "type": "gpu" },
        { "type": "persist", "name": "checkpoints", "path": "/data/checkpoints" }
      ],
      "env": {
        "ROLE": "${ROLE}",
        "MESH_SELF": "${MESH_SELF}",
        "LEARNER_ID": "${LEARNER_ID}",
        "MESH_PEERS": "${MESH_PEERS}",
        "SIM_BACKEND": "${SIM_BACKEND}"
      }
    }
  }
}
```

- `isolation: "isolated"` + `network mode: "mesh"` is the required combination
  (per HelloMesh: mesh egress needs a per-container netns/bridge).
- `gpu` entitlement (type `gpu`) is present for the MJX stretch path; the CPU
  path ignores it.
- `persist` entitlement (fields `type`, `name`, `path`) mounts a named volume for
  checkpoints at `/data/checkpoints`; the checkpoint path is env-overridable but
  defaults to that mount.
- The port and `serviceCIDR` mirror HelloMesh exactly.

### Dockerfile

- CPU path: ARM64 `python:3.11-slim`, `pip install` mujoco + numpy (+ torch CPU
  for PPO), copy `g1fleet/` and `app.py`. Non-root user, `EXPOSE 8080` (+ 5678
  for debugpy, matching the HelloPython convention). `CMD ["python","app.py"]`.
- MJX path (stretch): NVIDIA CUDA 13 ARM64 base + matching `jax[cuda]` +
  `mujoco-mjx`; selected by a build arg. Not on the critical path.

### Deploy

Asset IDs: Spark1=284, Spark2=283, Spark3=211. The fleet is in Wendy Cloud, so
deploys go through the tunnel with `wendy cloud run --device <id>` (the `--device`
flag accepts the numeric asset id, as confirmed against `wendy cloud device info
--device 284`). One deploy per device with per-device env.

ES:
```bash
cd Examples/G1FleetES
ROLE=coordinator MESH_SELF=284 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 284
ROLE=worker      MESH_SELF=283 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 283
ROLE=worker      MESH_SELF=211 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 211
```

PPO:
```bash
cd Examples/G1FleetPPO
ROLE=learner MESH_SELF=284 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 284
ROLE=actor   MESH_SELF=283 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 283
ROLE=actor   MESH_SELF=211 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu wendy cloud run --device 211
```

## Error handling

- **Mesh warmup:** peers may not answer immediately after start (route/firewall
  still coming up). All cross-device HTTP uses bounded retry with backoff; a
  failed peer is logged and skipped, never fatal.
- **Missing worker (ES):** coordinator advances a generation on timeout using the
  slices it received; logs which slices were missing.
- **Stale weights (PPO):** batches beyond `MAX_STALENESS` dropped and logged.
- **Checkpoint on shutdown:** learner/coordinator writes θ to the volume
  periodically so a restart resumes near where it left off.
- **Backend import failure (MJX):** if `SIM_BACKEND=mjx` fails to import/init,
  log a clear error and exit non-zero (do not silently fall back — the operator
  chose GPU explicitly).

## Testing / verification

1. **Unit (local, CPU):**
   - `g1env` produces finite obs/reward, terminates on fall and horizon,
     deterministic under fixed seed.
   - `policy` flat round-trip (`set_flat(get_flat())` is identity); NumPy and
     Torch forward passes agree numerically.
   - `netcodec` encode→decode round-trips params and trajectories (shape/dtype
     preserved).
   - ES gradient math: on a toy quadratic fitness, θ converges to the optimum.
2. **Local mesh protocol smoke:** run coordinator + one worker (ES) and
   learner + one actor (PPO) as two local processes/containers; assert the
   generation/version advances and mean return is reported.
3. **E2E on the real fleet (CPU) — the pass/fail bar:**
   - Deploy each app to 284/283/211.
   - Confirm LAN-direct peering in each device's dashboard **Mesh** tab.
   - Stream logs (`wendy cloud device logs`) and confirm mean return climbs over
     generations/updates on both apps.
4. **Stretch:** set `SIM_BACKEND=mjx`, resolve JAX-on-Blackwell, compare
   throughput vs CPU.

## Out of scope / YAGNI

- Fully solved G1 walking gait (stretch only).
- Cloud-relay mesh path tuning (fleet is same-LAN; relay untested here).
- Reboot/reconcile auto-rewiring of mesh CNI (known WendyOS limitation; re-run
  `wendy run` after a device restart, per HelloMesh notes).
- A web/graphical dashboard beyond the existing per-device Mesh tab and `/status`.
- Distributed SAC / replay-buffer variant (explicitly deferred in brainstorming).

## References

- `Examples/HelloMesh/` — mesh entitlement + peer-wiring template this builds on.
- `Examples/HelloPython/mujoco_g1.py` — G1 model load + actuator clamping pattern.
- `specs/2026-07-02-mesh-data-plane-design.md` and sibling mesh specs — mesh
  data-plane behavior (LAN-first dial, `device-<id>` addressing).
