#!/usr/bin/env python3
"""GPU probe for the G1 fleet MJX/Warp path.

Answers the make-or-break questions for the GPU backend, in order:
  1. Does WendyOS's `gpu` entitlement (NVIDIA CDI) expose a CUDA device in the
     container?
  2. Can NVIDIA Warp compile kernels for this GPU's arch (Blackwell sm_121)?
  3. Can mujoco_warp run a *batched* G1 simulation on the GPU, and how fast?

Prints a clear SUCCESS line + throughput on success, or a specific failure and a
non-zero exit on any of the above.
"""
import os
import sys
import time


def main() -> int:
    import warp as wp

    ver = getattr(getattr(wp, "config", None), "version", "?")
    print(f"[gpu-probe] warp version {ver}", flush=True)
    wp.init()

    devices = wp.get_devices()
    cuda_devices = [d for d in devices if getattr(d, "is_cuda", False)]
    print(f"[gpu-probe] all devices: {[str(d) for d in devices]}", flush=True)
    print(f"[gpu-probe] cuda available: {wp.is_cuda_available()} "
          f"cuda devices: {[str(d) for d in cuda_devices]}", flush=True)
    if not cuda_devices:
        print("[gpu-probe] FAIL: no CUDA device visible — GPU passthrough (CDI) "
              "did not expose a GPU to the container", flush=True)
        return 2

    import mujoco
    import mujoco_warp as mjwarp

    model_dir = os.environ.get("G1_MODEL_DIR", "/opt/g1_model")
    with open(os.path.join(model_dir, ".mjcf_name")) as f:
        xml = os.path.join(model_dir, f.read().strip())
    m = mujoco.MjModel.from_xml_path(xml)
    mj_d = mujoco.MjData(m)
    print(f"[gpu-probe] loaded G1 model nu={m.nu} nq={m.nq} nv={m.nv}", flush=True)

    n_steps = int(os.environ.get("N_STEPS", "200"))
    warmup = int(os.environ.get("N_WARMUP", "40"))
    sweep = [int(x) for x in os.environ.get("N_WORLDS_SWEEP", "2048,8192,16384").split(",")]

    mw_m = mjwarp.put_model(m)
    for n_worlds in sweep:
        mw_d = mjwarp.put_data(m, mj_d, nworld=n_worlds)
        # Warm up hard so ALL lazily-compiled solver kernels are built before we
        # time anything — otherwise JIT compilation pollutes the measurement.
        for _ in range(warmup):
            mjwarp.step(mw_m, mw_d)
        wp.synchronize()

        # (a) plain per-step launches
        t0 = time.time()
        for _ in range(n_steps):
            mjwarp.step(mw_m, mw_d)
        wp.synchronize()
        dt = time.time() - t0
        print(f"[gpu-probe] plain:  {n_worlds:>6} worlds x {n_steps} steps "
              f"= {n_worlds * n_steps / dt:>12,.0f} env-steps/s", flush=True)

        # (b) CUDA-graph capture + replay — collapses per-step launch overhead
        try:
            with wp.ScopedCapture() as capture:
                mjwarp.step(mw_m, mw_d)
            graph = capture.graph
            wp.synchronize()
            t0 = time.time()
            for _ in range(n_steps):
                wp.capture_launch(graph)
            wp.synchronize()
            dt = time.time() - t0
            print(f"[gpu-probe] graph:  {n_worlds:>6} worlds x {n_steps} steps "
                  f"= {n_worlds * n_steps / dt:>12,.0f} env-steps/s", flush=True)
        except Exception as exc:  # noqa: BLE001
            print(f"[gpu-probe] graph capture unavailable at {n_worlds}: {exc}", flush=True)
    print("[gpu-probe] SUCCESS", flush=True)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:  # noqa: BLE001 - probe: surface any failure clearly
        import traceback
        traceback.print_exc()
        print(f"[gpu-probe] FAIL: {type(exc).__name__}: {exc}", flush=True)
        sys.exit(1)
