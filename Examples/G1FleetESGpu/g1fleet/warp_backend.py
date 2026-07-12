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

This module imports torch + warp + mujoco_warp lazily; the CPU path never touches
them.
"""
from __future__ import annotations
import numpy as np

from . import g1env as G
from .policy import _layer_shapes


class WarpBackend:
    def __init__(self, obs_dim: int, act_dim: int, hidden=(256, 256), **_ignored):
        import torch
        import warp as wp
        import mujoco
        import mujoco_warp as mjwarp

        self.torch = torch
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
        self.device = "cuda"

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

        # Home stance (ctrl at keyframe) and actuator limits, as GPU tensors.
        home = self.mj_d.ctrl.copy() if self.nu else np.zeros(0, np.float32)
        self.home = torch.as_tensor(home, dtype=torch.float32, device=self.device)
        lo = self.m.actuator_ctrlrange[:, 0].copy()
        hi = self.m.actuator_ctrlrange[:, 1].copy()
        limited = self.m.actuator_ctrllimited.astype(bool)
        self.lo = torch.as_tensor(lo, dtype=torch.float32, device=self.device)
        self.hi = torch.as_tensor(hi, dtype=torch.float32, device=self.device)
        self.limited = torch.as_tensor(limited, device=self.device)

        self.mw_m = mjwarp.put_model(self.m)

    # -- batched per-world MLP -------------------------------------------------
    def _unpack(self, params_t):
        """params_t: (P, num_params) -> lists of per-world W (P,nin,nout), b (P,nout)."""
        Ws, bs, i = [], [], 0
        P = params_t.shape[0]
        for nin, nout in self._shapes:
            n = nin * nout
            Ws.append(params_t[:, i:i + n].reshape(P, nin, nout)); i += n
            bs.append(params_t[:, i:i + nout]); i += nout
        return Ws, bs

    def _forward(self, obs, Ws, bs):
        """obs: (P, obs_dim) -> action (P, act_dim), tanh-bounded. All layers tanh
        (matches MLPPolicy.act)."""
        torch = self.torch
        x = obs.unsqueeze(1)  # (P,1,in)
        for k in range(len(Ws)):
            x = torch.baddbmm(bs[k].unsqueeze(1), x, Ws[k])  # (P,1,out)
            x = torch.tanh(x)
        return x.squeeze(1)

    # -- rollout ---------------------------------------------------------------
    def evaluate_returns(self, param_vectors, seeds=None) -> np.ndarray:
        torch, wp, mjwarp = self.torch, self.wp, self.mjwarp
        P = len(param_vectors)
        params = np.stack([np.asarray(v, np.float32) for v in param_vectors])
        params_t = torch.as_tensor(params, dtype=torch.float32, device=self.device)
        Ws, bs = self._unpack(params_t)

        mw_d = mjwarp.put_data(self.m, self.mj_d, nworld=P)
        qpos = wp.to_torch(mw_d.qpos)   # (P, nq) GPU view
        qvel = wp.to_torch(mw_d.qvel)   # (P, nv)
        ctrl = wp.to_torch(mw_d.ctrl)   # (P, nu) — write in place so the graph sees it

        home_b = self.home.unsqueeze(0).expand(P, self.nu)

        # Warm up (compile all solver kernels) then capture the physics step.
        mjwarp.step(self.mw_m, mw_d)
        wp.synchronize()
        with wp.ScopedCapture() as capture:
            mjwarp.step(self.mw_m, mw_d)
        step_graph = capture.graph

        total = torch.zeros(P, device=self.device)
        alive = torch.ones(P, device=self.device)

        for _ in range(G.EPISODE_STEPS):
            obs = torch.cat([qpos, qvel, home_b], dim=1)      # (P, obs_dim)
            action = self._forward(obs, Ws, bs)                # (P, nu)
            target = home_b + G.ACTION_SCALE * action
            target = torch.where(self.limited, torch.clamp(target, self.lo, self.hi), target)
            ctrl.copy_(target)                                 # in-place -> graph reads it
            for _ in range(G.CTRL_DECIMATION):
                wp.capture_launch(step_graph)
            wp.synchronize()

            h = qpos[:, 2]
            v = qvel[:, 0]
            upright = torch.clamp(1.0 - torch.abs(h - G.STAND_HEIGHT), min=0.0)
            vel_track = -torch.abs(v - G.TARGET_VEL)
            ctrl_cost = torch.sum(action * action, dim=1)
            fell = h < G.FALL_HEIGHT
            r = (G.W_VEL * vel_track + G.W_UP * upright + G.ALIVE - G.W_CTRL * ctrl_cost)
            r = r - fell.float()  # fall penalty, matches g1env's reward -= 1.0
            total = total + r * alive
            alive = alive * (~fell).float()

        return total.detach().cpu().numpy().astype(np.float32)

    def collect_trajectory(self, *a, **k):
        raise NotImplementedError("WarpBackend supports ES (evaluate_returns) only; "
                                  "use SIM_BACKEND=cpu for PPO trajectory collection")
