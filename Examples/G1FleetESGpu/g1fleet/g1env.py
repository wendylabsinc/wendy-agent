"""Unitree G1 velocity-tracking + stay-upright task on the MuJoCo C engine (CPU).

Model comes from robot_descriptions (MuJoCo Menagerie unitree_g1), baked into the
image at build time. Action = PD deltas around the home actuator stance, clamped
to actuator control ranges (same clamping idea as Examples/HelloPython/mujoco_g1.py).
"""
from __future__ import annotations
import os
import numpy as np
import mujoco

EPISODE_STEPS = 400          # policy steps per episode
CTRL_DECIMATION = 5          # physics steps per policy step
TARGET_VEL = 0.5             # m/s forward target
W_VEL, W_UP, ALIVE, W_CTRL = 1.5, 0.5, 0.2, 0.001
FALL_HEIGHT = 0.4736         # base z below this => fall (0.6 * measured h0=0.7894)
ACTION_SCALE = 0.5           # rad delta scale on top of home stance
STAND_HEIGHT = 0.7894        # measured nominal standing base height (calibrated via Task 5 Step 4)


def _model_path() -> str:
    """Resolve the G1 MJCF. Prefers a model dir vendored into the image at build
    time (G1_MODEL_DIR) so runtime never touches the network — mesh-mode
    containers have no internet egress, so a runtime robot_descriptions git
    fetch would crash. Falls back to robot_descriptions for local dev."""
    d = os.environ.get("G1_MODEL_DIR", "").strip()
    if d and os.path.isdir(d):
        name_file = os.path.join(d, ".mjcf_name")
        name = "g1.xml"
        if os.path.isfile(name_file):
            with open(name_file) as f:
                name = f.read().strip() or name
        candidate = os.path.join(d, name)
        if os.path.isfile(candidate):
            return candidate
    from robot_descriptions import g1_mj_description
    return g1_mj_description.MJCF_PATH


class G1Env:
    def __init__(self, seed: int = 0):
        self.model = mujoco.MjModel.from_xml_path(_model_path())
        self.data = mujoco.MjData(self.model)
        self._rng = np.random.default_rng(seed)
        self._steps = 0
        self.nu = self.model.nu
        if self.model.nkey > 0:
            mujoco.mj_resetDataKeyframe(self.model, self.data, 0)
        self._home = self.data.ctrl.copy() if self.nu else np.zeros(0)
        self._lo = self.model.actuator_ctrlrange[:, 0].copy()
        self._hi = self.model.actuator_ctrlrange[:, 1].copy()
        self._limited = self.model.actuator_ctrllimited.astype(bool)
        self.act_dim = int(self.nu)
        self.obs_dim = int(self._observe().size)

    def _observe(self) -> np.ndarray:
        d = self.data
        return np.concatenate([
            d.qpos.ravel(), d.qvel.ravel(),
            self._home,  # context: home stance so obs is action-relative
        ]).astype(np.float32)

    def reset(self, seed: int | None = None) -> np.ndarray:
        if seed is not None:
            self._rng = np.random.default_rng(seed)
        mujoco.mj_resetData(self.model, self.data)
        if self.model.nkey > 0:
            mujoco.mj_resetDataKeyframe(self.model, self.data, 0)
        self._steps = 0
        return self._observe()

    def _base_height(self) -> float:
        return float(self.data.qpos[2]) if self.data.qpos.size > 2 else 0.0

    def _forward_vel(self) -> float:
        return float(self.data.qvel[0]) if self.data.qvel.size > 0 else 0.0

    def step(self, action: np.ndarray):
        a = np.asarray(action, dtype=np.float32).ravel()[: self.nu]
        target = self._home + ACTION_SCALE * a
        if self.nu:
            target = np.where(self._limited, np.clip(target, self._lo, self._hi), target)
            self.data.ctrl[:] = target
        for _ in range(CTRL_DECIMATION):
            mujoco.mj_step(self.model, self.data)
        self._steps += 1
        h = self._base_height(); v = self._forward_vel()
        upright = max(0.0, 1.0 - abs(h - STAND_HEIGHT))
        vel_track = -abs(v - TARGET_VEL)
        ctrl_cost = float(np.square(a).sum())
        fell = h < FALL_HEIGHT
        reward = W_VEL * vel_track + W_UP * upright + ALIVE - W_CTRL * ctrl_cost
        if fell:
            reward -= 1.0
        done = bool(fell or self._steps >= EPISODE_STEPS)
        return self._observe(), reward, done, {"h": h, "v": v}

    # expose constants as attributes for tests
    EPISODE_STEPS = EPISODE_STEPS
