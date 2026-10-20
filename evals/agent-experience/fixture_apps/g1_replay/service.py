"""Headless recorded-expert G1 replay, used by the with/without deployment eval.

This replays supplied targets in real MuJoCo physics. It is not policy training
or evidence of a physical pickup. Rendering, ROS and hardware I/O are absent.
"""
import hashlib
import json
import os
from pathlib import Path
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

os.environ.setdefault("MUJOCO_GL", "disable")
import mujoco
import numpy as np

ROOT = Path(__file__).resolve().parent
CONFIG = json.loads((ROOT / "config.json").read_text())
MODEL_PATH = ROOT / "assets" / CONFIG["model_file"]
ROLLOUT_PATH = ROOT / "assets" / CONFIG["rollout_file"]
RESULT = json.loads((ROOT / "assets/result.json").read_text())
model = mujoco.MjModel.from_binary_path(str(MODEL_PATH))
data = mujoco.MjData(model)
with np.load(ROLLOUT_PATH, allow_pickle=False) as archive:
    initial = archive["initial_state"].copy()
    targets = archive["trace"][:, 87:].copy()
    names = archive["joint_names"].tolist()
joint_ids = np.array([model.joint(name).id for name in names])
actuator_joints = model.actuator_trnid[:, 0]
order = np.array([names.index(model.joint(int(joint)).name) for joint in actuator_joints])
qa = model.jnt_qposadr[joint_ids]
aq = model.jnt_qposadr[actuator_joints]
av = model.jnt_dofadr[actuator_joints]
upper = [12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 29, 30, 31, 32, 33, 34, 35]
hands = [36, 37, 38, 39, 40, 41, 42]
owned = np.isin(order, upper + hands)
locked = np.isin(order, [13, 14])
kp, kd = np.full(43, 60.0), np.full(43, 1.5)
kp[[19, 20, 21, 33, 34, 35]] = 40.0
controller = RESULT["controller"]
kp[hands] = controller.get("hand_kp_by_joint", [controller["hand_kp"]] * 7)
kd[hands] = controller["hand_kd"]
kp, kd = kp[order], kd[order]
bounds = model.jnt_actfrcrange[actuator_joints]
mujoco.mj_setState(model, data, initial, mujoco.mjtState.mjSTATE_INTEGRATION)
mujoco.mj_forward(model, data)
start = data.qpos[qa].copy()
lock = threading.Lock()
tick = 0
failure = None
asset_hashes = {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                for p in (MODEL_PATH, ROLLOUT_PATH)}


def simulate():
    global tick, failure
    try:
        while True:
            with lock:
                # Fixed original 40 Hz targets; one 1 ms physics step per tick.
                target = targets[min(tick // 25, len(targets) - 1)]
                base = 100 * (start[order] - data.qpos[aq]) - 4 * data.qvel[av] + data.qfrc_bias[av]
                pd = kp * (target[order] - data.qpos[aq]) - kd * data.qvel[av]
                torque = np.where(owned, pd, base)
                torque[locked] = 0
                data.ctrl[:] = np.clip(torque, bounds[:, 0], bounds[:, 1])
                mujoco.mj_step(model, data)
                if not np.isfinite(data.qpos).all():
                    raise RuntimeError("nonfinite simulation state")
                tick += 1
            time.sleep(.005)
    except Exception as exc:
        failure = str(exc)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path not in ("/health", "/state"):
            self.send_error(404)
            return
        with lock:
            value = {"schema": "g1-replay-eval.v1", "nonce": CONFIG["nonce"],
                     "version": CONFIG["version"], "tick": tick,
                     "simulation_time": float(data.time), "wall_time": time.time(),
                     "joint_names": names, "joint_positions": data.qpos[qa].tolist(),
                     "actuators": int(model.nu), "mujoco": mujoco.__version__,
                     "assets": asset_hashes, "error": failure}
        body = json.dumps(value, allow_nan=False).encode()
        self.send_response(200 if failure is None and tick > 0 else 503)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        pass


if __name__ == "__main__":
    threading.Thread(target=simulate, daemon=True).start()
    print(json.dumps({"event": "started", "nonce": CONFIG["nonce"]}), flush=True)
    ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
