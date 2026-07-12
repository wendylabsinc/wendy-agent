"""GPU rollout backend using NVIDIA Warp + mujoco_warp (SIM_BACKEND=warp).

For Evolution Strategies the batch dimension IS the population: each of `nworld`
parallel G1 worlds runs a *different* perturbed policy, all stepped together on
the GPU. The MuJoCo physics step is captured once into a CUDA graph and replayed
every physics tick — without that capture mujoco_warp is launch-overhead-bound
(~25k env-steps/s); with it we measured ~734k env-steps/s on a Spark (see
Examples/G1GpuProbe).

Only `evaluate_returns` is implemented (all ES needs). The policy forward pass is
a per-world batched MLP in torch-cuda, sharing the device with Warp. Observation
and reward exactly mirror g1fleet.g1env so returns are comparable to the CPU
backend.

The batched MLP runs in CuPy (numpy-like GPU arrays) rather than torch — torch's
CUDA wheel adds ~7 GB to the image, which made the mesh push over the tunnel
unreliable; CuPy reuses the CUDA runtime already in the base image. warp +
mujoco_warp + cupy are imported lazily; the CPU path never touches them.
"""
from __future__ import annotations
import numpy as np

from . import g1env as G
from .policy import _layer_shapes


class WarpBackend:
    def __init__(self, obs_dim: int, act_dim: int, hidden=(256, 256), **_ignored):
        import cupy as cp
        import warp as wp
        import mujoco
        import mujoco_warp as mjwarp

        self.cp = cp
        self.wp = wp
        self.mjwarp = mjwarp
        self.obs_dim = obs_dim
        self.act_dim = act_dim
        self.hidden = tuple(hidden)
        self._shapes = _layer_shapes(obs_dim, act_dim, self.hidden)

        wp.init()
        if not wp.is_cuda_available():
            raise RuntimeError("SIM_BACKEND=warp requires a CUDA device; none visible "
                               "(is the `gpu` entitlement set and CDI configured?)")

        # Build the MjModel once; reset to the home keyframe so every world starts
        # from the same stance g1env uses.
        xml = G._model_path()
        self.m = mujoco.MjModel.from_xml_path(xml)
        self.mj_d = mujoco.MjData(self.m)
        if self.m.nkey > 0:
            mujoco.mj_resetDataKeyframe(self.m, self.mj_d, 0)
        self.nu = int(self.m.nu)
        self.nq = int(self.m.nq)
        self.nv = int(self.m.nv)

        # Home stance (ctrl at keyframe) and actuator limits, as GPU arrays.
        home = self.mj_d.ctrl.copy() if self.nu else np.zeros(0, np.float32)
        self.home = cp.asarray(home, dtype=cp.float32)
        self.lo = cp.asarray(self.m.actuator_ctrlrange[:, 0], dtype=cp.float32)
        self.hi = cp.asarray(self.m.actuator_ctrlrange[:, 1], dtype=cp.float32)
        self.limited = cp.asarray(self.m.actuator_ctrllimited.astype(bool))

        self.mw_m = mjwarp.put_model(self.m)

    # -- batched per-world MLP -------------------------------------------------
    def _unpack(self, params_t):
        """params_t: (P, num_params) -> lists of per-world W (P,nin,nout), b (P,1,nout)."""
        Ws, bs, i = [], [], 0
        P = params_t.shape[0]
        for nin, nout in self._shapes:
            n = nin * nout
            Ws.append(params_t[:, i:i + n].reshape(P, nin, nout)); i += n
            bs.append(params_t[:, i:i + nout].reshape(P, 1, nout)); i += nout
        return Ws, bs

    def _forward(self, obs, Ws, bs):
        """obs: (P, obs_dim) -> action (P, act_dim), tanh-bounded. All layers tanh
        (matches MLPPolicy.act)."""
        cp = self.cp
        x = obs[:, None, :]  # (P,1,in)
        for k in range(len(Ws)):
            x = cp.tanh(cp.matmul(x, Ws[k]) + bs[k])  # (P,1,out)
        return x[:, 0, :]

    # -- rollout ---------------------------------------------------------------
    def evaluate_returns(self, param_vectors, seeds=None) -> np.ndarray:
        cp, wp, mjwarp = self.cp, self.wp, self.mjwarp
        P = len(param_vectors)
        params = np.stack([np.asarray(v, np.float32) for v in param_vectors])
        params_t = cp.asarray(params, dtype=cp.float32)
        Ws, bs = self._unpack(params_t)

        mw_d = mjwarp.put_data(self.m, self.mj_d, nworld=P)
        # Zero-copy CuPy views over the Warp arrays (shared GPU memory). Writing
        # into `ctrl` in place is what lets the captured graph read fresh controls.
        qpos = cp.asarray(mw_d.qpos)   # (P, nq)
        qvel = cp.asarray(mw_d.qvel)   # (P, nv)
        ctrl = cp.asarray(mw_d.ctrl)   # (P, nu)

        home_b = cp.broadcast_to(self.home, (P, self.nu))

        # Warm up (compile all solver kernels) then capture the physics step.
        mjwarp.step(self.mw_m, mw_d)
        wp.synchronize()
        with wp.ScopedCapture() as capture:
            mjwarp.step(self.mw_m, mw_d)
        step_graph = capture.graph

        total = cp.zeros(P, dtype=cp.float32)
        alive = cp.ones(P, dtype=cp.float32)

        for _ in range(G.EPISODE_STEPS):
            obs = cp.concatenate([qpos, qvel, home_b], axis=1)   # (P, obs_dim)
            action = self._forward(obs, Ws, bs)                   # (P, nu)
            target = home_b + G.ACTION_SCALE * action
            target = cp.where(self.limited, cp.clip(target, self.lo, self.hi), target)
            ctrl[...] = target                                    # in-place -> graph reads it
            for _ in range(G.CTRL_DECIMATION):
                wp.capture_launch(step_graph)
            wp.synchronize()

            h = qpos[:, 2]
            v = qvel[:, 0]
            upright = cp.clip(1.0 - cp.abs(h - G.STAND_HEIGHT), 0.0, None)
            vel_track = -cp.abs(v - G.TARGET_VEL)
            ctrl_cost = cp.sum(action * action, axis=1)
            fell = h < G.FALL_HEIGHT
            r = (G.W_VEL * vel_track + G.W_UP * upright + G.ALIVE - G.W_CTRL * ctrl_cost)
            r = r - fell.astype(cp.float32)  # fall penalty, matches g1env reward -= 1.0
            total = total + r * alive
            alive = alive * (~fell).astype(cp.float32)

        return cp.asnumpy(total).astype(np.float32)

    def collect_trajectory(self, *a, **k):
        raise NotImplementedError("WarpBackend supports ES (evaluate_returns) only; "
                                  "use SIM_BACKEND=cpu for PPO trajectory collection")
