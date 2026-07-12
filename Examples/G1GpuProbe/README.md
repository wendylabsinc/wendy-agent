# G1GpuProbe — GPU viability + throughput probe for the fleet

A minimal, single-shot app that answers whether the **GPU path** for G1 fleet
training is viable on the Spark hardware, and how fast it is. Run it before
building a GPU training backend.

## What it checks (in order)

1. WendyOS's `gpu` entitlement (NVIDIA CDI) actually exposes a CUDA device in
   the container.
2. NVIDIA Warp compiles kernels for the GPU arch (Blackwell `sm_121`).
3. `mujoco_warp` runs a **batched** G1 simulation on the GPU, plain vs
   **CUDA-graph** capture, across a sweep of world counts.

## Recipe (proven on Spark1/2/3, borrowed from `~/git/wendy/mjlab`)

- Base image `nvidia/cuda:12.8.0-runtime-ubuntu24.04` — the CUDA 12.8 runtime is
  backward-compatible with the Sparks' CUDA-13 Blackwell driver (the driver's
  `libcuda` is mounted in by CDI; the image supplies the CUDA runtime).
- `warp-lang>=1.14.0` from **`pypi.nvidia.com`** (resolves to 1.15.0, aarch64,
  CUDA-enabled), `mujoco-warp~=3.10`, `mujoco~=3.10`.
- `MUJOCO_GL=egl`. Single `gpu` entitlement, no network needed (model vendored).

## Measured results (Spark3, 211, 2026-07-12)

```
plain:   2048 worlds = 22,620 env-steps/s      graph:   2048 = 663,772 env-steps/s
plain:   8192 worlds = 26,788 env-steps/s      graph:   8192 = 734,461 env-steps/s
plain:  16384 worlds = 21,129 env-steps/s      graph:  16384 = 734,063 env-steps/s
```

**Key findings:**
- CDI GPU passthrough + Warp `sm_121` kernel compile + batched `mujoco_warp`
  G1 sim all work inside a WendyOS container. ✅
- Plain per-step launches are **launch-overhead-bound** (~25k steps/s, flat
  across batch size — the GPU is starved by kernel-launch overhead, not compute).
- **CUDA-graph capture is mandatory**: capturing `mjwarp.step` once and replaying
  it gives **~734k env-steps/s (~30×)** — roughly 15–30× the CPU backend per
  device, ~2.2M steps/s across the three Sparks.

## Run it

```bash
cd Examples/G1GpuProbe
N_STEPS=300 N_WORLDS_SWEEP=8192,16384 wendy cloud run --device 211 --detach --no-restart
wendy cloud device logs --device 211    # look for [gpu-probe] lines + SUCCESS
```

## Implication for the GPU training backend

A `WarpBackend` in `g1fleet/rollout.py` should:
- run `nworld = <ES population slice>` parallel G1 worlds,
- do the policy forward pass batched on GPU (per-world weights via a batched
  matmul in torch-cuda, sharing the device with Warp),
- extract obs/reward from `mjwarp` data as torch tensors (`wp.to_torch`),
- **wrap `mjwarp.step` in a captured CUDA graph** and replay it each control
  step (this is the 30× that makes it worthwhile),
- run a fixed horizon, accumulate per-world return, and hand the vector of
  returns back to the ES driver unchanged.

Selected by `SIM_BACKEND=warp`. The CPU backend stays the portable default.
