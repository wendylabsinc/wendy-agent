"""Deterministic G1 Fruit Ninja demo backed by real MuJoCo dynamics.

The motion controller is deliberately scripted. Fruit flight, contacts, and
split-fruit debris are simulated by MuJoCo; no browser-side physics is used.
"""

from __future__ import annotations

import io
import os
import platform
import socket
import threading
import time
from pathlib import Path
from typing import Any

import mujoco
import numpy as np
from PIL import Image


PHYSICS_HZ = 250
CONTROL_HZ = 50
STREAM_HZ = 25
TIMESTEP = 1.0 / PHYSICS_HZ
CONTROL_DECIMATION = PHYSICS_HZ // CONTROL_HZ
CYCLE_SECONDS = 2.35
LAUNCH_TIME = 0.65
INTERCEPT_TIME = 1.35

FRUIT_NAMES = ("watermelon", "mango", "lime")
FRUIT_COLORS = (
    np.array([1.0, 0.22, 0.12, 1.0]),
    np.array([1.0, 0.72, 0.08, 1.0]),
    np.array([0.25, 0.90, 0.35, 1.0]),
)

# Same supported-base, upper-body-only shape as the IsaacLab demonstration.
LEFT_REST = np.array([0.20, 0.20, 0.0, 1.28, 0.0, 0.0, 0.0])
RIGHT_REST = np.array([0.20, -0.20, 0.0, 1.28, 0.0, 0.0, 0.0])
RIGHT_WINDUP = np.array([-1.081, -0.6156, -0.8136, 0.4485, 0.8004, 0.4406, 0.1762])
RIGHT_FOLLOW = np.array([-1.331, -0.0335, 0.9486, 0.1558, -0.2873, 0.3074, 0.2759])


def _smoothstep(value: float) -> float:
    value = float(np.clip(value, 0.0, 1.0))
    return value * value * (3.0 - 2.0 * value)


def _blend(start: np.ndarray, end: np.ndarray, fraction: float) -> np.ndarray:
    return start + (end - start) * _smoothstep(fraction)


class FruitNinjaSimulation:
    """Owns the MuJoCo model, controller, renderer, and latest JPEG frame."""

    def __init__(self, *, enable_renderer: bool = True) -> None:
        root = Path(__file__).resolve().parents[1]
        scene_path = root / "models" / "unitree_g1" / "fruit_ninja_scene.xml"
        self.model = mujoco.MjModel.from_xml_path(str(scene_path))
        self.data = mujoco.MjData(self.model)
        self.shadow = mujoco.MjData(self.model)
        self.render_data = mujoco.MjData(self.model)
        self.model.opt.timestep = TIMESTEP

        self._lock = threading.RLock()
        self._condition = threading.Condition(self._lock)
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._render_thread: threading.Thread | None = None
        self._renderer: mujoco.Renderer | None = None
        self._camera: mujoco.MjvCamera | None = None
        self._scene_option: mujoco.MjvOption | None = None
        self._jpeg: bytes | None = None
        self._frame_seq = 0
        self._wall_started = time.time()
        self._last_render_wall = self._wall_started
        self._measured_stream_fps = 0.0

        self._actuator_joint_ids = self.model.actuator_trnid[:, 0].astype(int)
        self._qpos_addresses = self.model.jnt_qposadr[self._actuator_joint_ids].astype(int)
        self._dof_addresses = self.model.jnt_dofadr[self._actuator_joint_ids].astype(int)
        self._joint_ranges = self.model.jnt_range[self._actuator_joint_ids].copy()
        self._ctrl_ranges = self.model.actuator_ctrlrange.copy()

        self._wrist_id = self._body_id("right_wrist_yaw_link")
        self._blade_body_id = self._body_id("blade_mocap")
        self._blade_geom_id = self._geom_id("blade_edge")
        self._blade_mocap_id = int(self.model.body_mocapid[self._blade_body_id])
        self._first_fruit_body_id = self._body_id("fruit_0")

        self._fruit_joint_ids = [self._joint_id(f"fruit_{index}_joint") for index in range(3)]
        self._fruit_geom_ids = [self._geom_id(f"fruit_{index}_geom") for index in range(3)]
        self._half_joint_ids = [
            (self._joint_id(f"fruit_{index}_half_a_joint"), self._joint_id(f"fruit_{index}_half_b_joint"))
            for index in range(3)
        ]
        self._half_geom_ids = [
            (self._geom_id(f"fruit_{index}_half_a_geom"), self._geom_id(f"fruit_{index}_half_b_geom"))
            for index in range(3)
        ]

        self._rest = np.concatenate((np.zeros(15), LEFT_REST, RIGHT_REST))
        self._windup = self._rest.copy()
        self._windup[12] = -0.08
        self._windup[22:29] = RIGHT_WINDUP
        self._follow = self._rest.copy()
        self._follow[12] = 0.10
        self._follow[22:29] = RIGHT_FOLLOW
        self._guarded_target = self._rest.copy()

        self._kp = np.array([90.0] * 12 + [55.0] * 3 + [38.0] * 8 + [30.0] * 4 + [12.0] * 2)
        self._kd = np.array([3.5] * 12 + [2.5] * 3 + [1.8] * 8 + [1.2] * 4 + [0.55] * 2)

        self._cycle_started = 0.0
        self._cycle_index = 0
        self._active_fruit: int | None = None
        self._launched_this_cycle = False
        self._split_this_cycle = False
        self._launches = 0
        self._hits = 0
        self._misses = 0
        self._shield_events = 0
        self._shield_active = False
        self._phase = "READY"
        self._last_wrist_pos = np.zeros(3)
        self._wrist_speed = 0.0
        self._peak_wrist_speed = 0.0
        self._last_error: str | None = None

        self.reset()
        if enable_renderer:
            self._create_renderer()

    def _body_id(self, name: str) -> int:
        return int(mujoco.mj_name2id(self.model, mujoco.mjtObj.mjOBJ_BODY, name))

    def _geom_id(self, name: str) -> int:
        return int(mujoco.mj_name2id(self.model, mujoco.mjtObj.mjOBJ_GEOM, name))

    def _joint_id(self, name: str) -> int:
        return int(mujoco.mj_name2id(self.model, mujoco.mjtObj.mjOBJ_JOINT, name))

    def _create_renderer(self) -> None:
        self._renderer = mujoco.Renderer(self.model, height=360, width=640)
        self._camera = mujoco.MjvCamera()
        self._camera.lookat[:] = np.array([0.17, 0.0, 0.83])
        self._camera.distance = 1.92
        self._camera.azimuth = 145.0
        self._camera.elevation = -8.0
        self._scene_option = mujoco.MjvOption()
        self._scene_option.geomgroup[0] = 0
        self._scene_option.geomgroup[3] = 0

    def reset(self) -> None:
        with self._lock:
            mujoco.mj_resetData(self.model, self.data)
            self.data.qpos[:7] = np.array([0.0, 0.0, 0.793, 1.0, 0.0, 0.0, 0.0])
            self.data.qpos[self._qpos_addresses] = self._rest
            self.data.qvel[:] = 0.0
            self._guarded_target = self._rest.copy()
            self._cycle_started = 0.0
            self._cycle_index = 0
            self._active_fruit = None
            self._launched_this_cycle = False
            self._split_this_cycle = False
            self._launches = 0
            self._hits = 0
            self._misses = 0
            self._shield_events = 0
            self._shield_active = False
            self._phase = "READY"
            self._last_error = None
            self._hide_all_fruit()
            mujoco.mj_forward(self.model, self.data)
            self._attach_blade()
            mujoco.mj_forward(self.model, self.data)
            self._last_wrist_pos = self.data.xpos[self._wrist_id].copy()
            self._wrist_speed = 0.0
            self._peak_wrist_speed = 0.0

    def _set_free_joint(
        self,
        joint_id: int,
        position: np.ndarray,
        velocity: np.ndarray | None = None,
        angular_velocity: np.ndarray | None = None,
    ) -> None:
        qadr = int(self.model.jnt_qposadr[joint_id])
        dadr = int(self.model.jnt_dofadr[joint_id])
        self.data.qpos[qadr : qadr + 3] = position
        self.data.qpos[qadr + 3 : qadr + 7] = np.array([1.0, 0.0, 0.0, 0.0])
        self.data.qvel[dadr : dadr + 3] = np.zeros(3) if velocity is None else velocity
        self.data.qvel[dadr + 3 : dadr + 6] = (
            np.zeros(3) if angular_velocity is None else angular_velocity
        )

    def _set_geom_active(self, geom_id: int, active: bool, color: np.ndarray) -> None:
        self.model.geom_rgba[geom_id] = color
        self.model.geom_rgba[geom_id, 3] = 1.0 if active else 0.0
        # Collision masks stay compiled in; inactive bodies are parked far below.

    def _hide_all_fruit(self) -> None:
        for index, joint_id in enumerate(self._fruit_joint_ids):
            self._set_free_joint(joint_id, np.array([index * 1.5, 0.0, -5.0]))
            self._set_geom_active(self._fruit_geom_ids[index], False, FRUIT_COLORS[index])
            for half_number, half_joint_id in enumerate(self._half_joint_ids[index]):
                self._set_free_joint(
                    half_joint_id, np.array([index * 1.5, 0.5 + half_number * 0.5, -5.0])
                )
                half_color = FRUIT_COLORS[index].copy()
                half_color[:3] = np.minimum(1.0, half_color[:3] + 0.12 * half_number)
                self._set_geom_active(self._half_geom_ids[index][half_number], False, half_color)

    def _launch_fruit(self) -> None:
        index = self._cycle_index % 3
        start = np.array([0.82, -0.08, 0.16])
        target = np.array([0.405, -0.08, 1.235])
        flight_seconds = INTERCEPT_TIME - LAUNCH_TIME
        gravity = np.array([0.0, 0.0, -9.81])
        velocity = (target - start - 0.5 * gravity * flight_seconds**2) / flight_seconds
        velocity[1] += (index - 1) * 0.025
        self._set_free_joint(self._fruit_joint_ids[index], start, velocity)
        self._set_geom_active(self._fruit_geom_ids[index], True, FRUIT_COLORS[index])
        self._active_fruit = index
        self._launched_this_cycle = True
        self._split_this_cycle = False
        self._launches += 1

    def _split_fruit(self, index: int) -> None:
        joint_id = self._fruit_joint_ids[index]
        qadr = int(self.model.jnt_qposadr[joint_id])
        dadr = int(self.model.jnt_dofadr[joint_id])
        position = self.data.qpos[qadr : qadr + 3].copy()
        velocity = self.data.qvel[dadr : dadr + 3].copy()
        self._set_geom_active(self._fruit_geom_ids[index], False, FRUIT_COLORS[index])
        self._set_free_joint(joint_id, np.array([index * 1.5, 0.0, -5.0]))
        for half_number, direction in enumerate((-1.0, 1.0)):
            half_joint = self._half_joint_ids[index][half_number]
            half_velocity = velocity + np.array([0.12, direction * 1.0, 0.55])
            half_angular = np.array([direction * 7.0, 4.0, direction * 2.5])
            self._set_free_joint(
                half_joint,
                position + np.array([0.0, direction * 0.018, 0.0]),
                half_velocity,
                half_angular,
            )
            half_color = FRUIT_COLORS[index].copy()
            half_color[:3] = np.minimum(1.0, half_color[:3] + 0.12 * half_number)
            self._set_geom_active(self._half_geom_ids[index][half_number], True, half_color)
        self._split_this_cycle = True
        self._hits += 1

    def _desired_target(self, cycle_time: float) -> np.ndarray:
        if cycle_time < 0.30:
            self._phase = "READY"
            return self._rest
        if cycle_time < 0.70:
            self._phase = "WIND UP"
            return _blend(self._rest, self._windup, (cycle_time - 0.30) / 0.40)
        if cycle_time < 1.04:
            self._phase = "TRACK"
            return self._windup
        if cycle_time < INTERCEPT_TIME:
            self._phase = "SLICE"
            return _blend(self._windup, self._follow, (cycle_time - 1.04) / 0.31)
        if cycle_time < 1.67:
            self._phase = "FOLLOW THROUGH"
            return self._follow
        self._phase = "RECOVER"
        return _blend(self._follow, self._rest, (cycle_time - 1.67) / 0.58)

    def _target_has_collision(self, target: np.ndarray) -> bool:
        self.shadow.qpos[:] = self.data.qpos
        self.shadow.qvel[:] = 0.0
        self.shadow.qpos[self._qpos_addresses] = target
        mujoco.mj_forward(self.model, self.shadow)
        for contact_index in range(self.shadow.ncon):
            contact = self.shadow.contact[contact_index]
            body_a = int(self.model.geom_bodyid[contact.geom1])
            body_b = int(self.model.geom_bodyid[contact.geom2])
            if 0 < body_a < self._first_fruit_body_id and 0 < body_b < self._first_fruit_body_id:
                return True
        return False

    def _update_controller(self, cycle_time: float) -> None:
        desired = self._desired_target(cycle_time)
        margin = np.minimum(0.08, (self._joint_ranges[:, 1] - self._joint_ranges[:, 0]) * 0.05)
        desired = np.clip(desired, self._joint_ranges[:, 0] + margin, self._joint_ranges[:, 1] - margin)
        delta = np.clip(desired - self._guarded_target, -0.08, 0.08)
        candidate = self._guarded_target + delta
        if self._target_has_collision(candidate):
            self._shield_active = True
            self._shield_events += 1
            recovery = np.clip(self._rest - self._guarded_target, -0.035, 0.035)
            self._guarded_target += recovery
        else:
            self._shield_active = False
            self._guarded_target = candidate

    def _apply_pd_torque(self) -> None:
        position = self.data.qpos[self._qpos_addresses]
        velocity = self.data.qvel[self._dof_addresses]
        torque = self._kp * (self._guarded_target - position) - self._kd * velocity
        torque += self.data.qfrc_bias[self._dof_addresses]
        self.data.ctrl[:] = np.clip(torque, self._ctrl_ranges[:, 0], self._ctrl_ranges[:, 1])

    def _attach_blade(self) -> None:
        wrist_position = self.data.xpos[self._wrist_id]
        self.data.mocap_pos[self._blade_mocap_id] = wrist_position
        wrist_quaternion = np.empty(4)
        mujoco.mju_mat2Quat(wrist_quaternion, self.data.xmat[self._wrist_id])
        self.data.mocap_quat[self._blade_mocap_id] = wrist_quaternion

    def _detect_blade_hit(self) -> None:
        if self._active_fruit is None or self._split_this_cycle:
            return
        fruit_geom = self._fruit_geom_ids[self._active_fruit]
        wanted = {self._blade_geom_id, fruit_geom}
        for contact_index in range(self.data.ncon):
            contact = self.data.contact[contact_index]
            if {int(contact.geom1), int(contact.geom2)} == wanted:
                self._split_fruit(self._active_fruit)
                return

    def _advance_cycle_if_needed(self, cycle_time: float) -> float:
        if cycle_time < CYCLE_SECONDS:
            return cycle_time
        if self._launched_this_cycle and not self._split_this_cycle:
            self._misses += 1
        self._hide_all_fruit()
        self._active_fruit = None
        self._launched_this_cycle = False
        self._split_this_cycle = False
        self._cycle_index += 1
        self._cycle_started = float(self.data.time)
        return 0.0

    def step(self) -> None:
        with self._lock:
            cycle_time = self._advance_cycle_if_needed(float(self.data.time - self._cycle_started))
            physics_step = int(round(self.data.time / TIMESTEP))
            if physics_step % CONTROL_DECIMATION == 0:
                self._update_controller(cycle_time)
            if cycle_time >= LAUNCH_TIME and not self._launched_this_cycle:
                self._launch_fruit()
            self._apply_pd_torque()
            self._attach_blade()
            mujoco.mj_step(self.model, self.data)
            self._attach_blade()
            mujoco.mj_forward(self.model, self.data)
            self._detect_blade_hit()
            wrist_position = self.data.xpos[self._wrist_id].copy()
            self._wrist_speed = float(np.linalg.norm(wrist_position - self._last_wrist_pos) / TIMESTEP)
            self._peak_wrist_speed = max(self._peak_wrist_speed, self._wrist_speed)
            self._last_wrist_pos = wrist_position

    def _render(self) -> None:
        if self._renderer is None or self._camera is None or self._scene_option is None:
            return
        with self._lock:
            self.render_data.qpos[:] = self.data.qpos
            self.render_data.qvel[:] = self.data.qvel
            self.render_data.mocap_pos[:] = self.data.mocap_pos
            self.render_data.mocap_quat[:] = self.data.mocap_quat
            self.render_data.time = self.data.time
            mujoco.mj_forward(self.model, self.render_data)
        self._renderer.update_scene(
            self.render_data, camera=self._camera, scene_option=self._scene_option
        )
        pixels = self._renderer.render()
        output = io.BytesIO()
        Image.fromarray(pixels).save(output, format="JPEG", quality=86, optimize=False)
        now = time.time()
        with self._condition:
            delta = max(now - self._last_render_wall, 1.0e-6)
            instant = 1.0 / delta
            self._measured_stream_fps = instant if self._frame_seq == 0 else 0.88 * self._measured_stream_fps + 0.12 * instant
            self._last_render_wall = now
            self._jpeg = output.getvalue()
            self._frame_seq += 1
            self._condition.notify_all()

    def _run(self) -> None:
        next_tick = time.perf_counter()
        try:
            while not self._stop.is_set():
                self.step()
                next_tick += TIMESTEP
                remaining = next_tick - time.perf_counter()
                if remaining > 0:
                    self._stop.wait(remaining)
                elif remaining < -0.25:
                    next_tick = time.perf_counter()
        except Exception as error:
            with self._condition:
                self._last_error = f"{type(error).__name__}: {error}"
                self._condition.notify_all()

    def _run_renderer(self) -> None:
        interval = 1.0 / STREAM_HZ
        next_frame = time.perf_counter()
        try:
            while not self._stop.is_set():
                self._render()
                next_frame += interval
                remaining = next_frame - time.perf_counter()
                if remaining > 0:
                    self._stop.wait(remaining)
                elif remaining < -interval:
                    next_frame = time.perf_counter()
        except Exception as error:
            with self._condition:
                self._last_error = f"{type(error).__name__}: {error}"
                self._condition.notify_all()

    def start(self) -> None:
        if self._thread is not None:
            return
        self._thread = threading.Thread(target=self._run, name="mujoco-fruit-ninja", daemon=True)
        self._render_thread = threading.Thread(
            target=self._run_renderer, name="mujoco-fruit-ninja-renderer", daemon=True
        )
        self._thread.start()
        self._render_thread.start()

    def close(self) -> None:
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout=3.0)
        if self._render_thread is not None:
            self._render_thread.join(timeout=3.0)
        if self._renderer is not None:
            self._renderer.close()

    def wait_for_frame(self, after: int, timeout: float = 3.0) -> tuple[int, bytes | None]:
        with self._condition:
            if self._frame_seq <= after and self._last_error is None:
                self._condition.wait(timeout)
            return self._frame_seq, self._jpeg

    def run_headless_steps(self, steps: int) -> dict[str, Any]:
        for _ in range(steps):
            self.step()
        return self.status()

    def status(self) -> dict[str, Any]:
        with self._lock:
            active_name = None if self._active_fruit is None else FRUIT_NAMES[self._active_fruit]
            return {
                "ready": self._last_error is None and (self._renderer is None or self._frame_seq > 0),
                "error": self._last_error,
                "simulation": "MuJoCo",
                "mujocoVersion": mujoco.__version__,
                "renderer": os.environ.get("MUJOCO_GL", "default"),
                "controller": "scripted upper-body strike",
                "policyLoaded": False,
                "physicsHz": PHYSICS_HZ,
                "controlHz": CONTROL_HZ,
                "streamTargetFps": STREAM_HZ,
                "streamFps": round(self._measured_stream_fps, 1),
                "frameSeq": self._frame_seq,
                "simTimeSeconds": round(float(self.data.time), 3),
                "uptimeSeconds": round(time.time() - self._wall_started, 1),
                "phase": self._phase,
                "fruit": active_name,
                "launches": self._launches,
                "hits": self._hits,
                "misses": self._misses,
                "hitRate": round(self._hits / max(self._launches, 1), 3),
                "wristSpeedMps": round(self._wrist_speed, 2),
                "peakWristSpeedMps": round(self._peak_wrist_speed, 2),
                "shieldActive": self._shield_active,
                "shieldEvents": self._shield_events,
                "supportedBase": True,
                "realPhysicsContacts": True,
                "simulationOnly": True,
                "host": socket.gethostname(),
                "architecture": platform.machine(),
            }
