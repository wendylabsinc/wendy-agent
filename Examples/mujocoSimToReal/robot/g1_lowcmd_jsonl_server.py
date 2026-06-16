#!/usr/bin/env python3
"""Robot-side Unitree G1 LowCmd server.

Reads JSON lines on stdin:
  {"seq": 1, "deltas": {"left_elbow_joint": 0.02}}

Each delta is relative to the measured startup pose on the robot. The server
publishes full 29-motor LowCmd frames continuously and holds every joint not
listed in --allowed-joints at the measured startup pose.
"""

from __future__ import annotations

import argparse
import json
import math
import queue
import sys
import threading
import time


G1_DDS = {
    "left_hip_pitch_joint": 0,
    "left_hip_roll_joint": 1,
    "left_hip_yaw_joint": 2,
    "left_knee_joint": 3,
    "left_ankle_pitch_joint": 4,
    "left_ankle_roll_joint": 5,
    "right_hip_pitch_joint": 6,
    "right_hip_roll_joint": 7,
    "right_hip_yaw_joint": 8,
    "right_knee_joint": 9,
    "right_ankle_pitch_joint": 10,
    "right_ankle_roll_joint": 11,
    "waist_yaw_joint": 12,
    "waist_roll_joint": 13,
    "waist_pitch_joint": 14,
    "left_shoulder_pitch_joint": 15,
    "left_shoulder_roll_joint": 16,
    "left_shoulder_yaw_joint": 17,
    "left_elbow_joint": 18,
    "left_wrist_roll_joint": 19,
    "left_wrist_pitch_joint": 20,
    "left_wrist_yaw_joint": 21,
    "right_shoulder_pitch_joint": 22,
    "right_shoulder_roll_joint": 23,
    "right_shoulder_yaw_joint": 24,
    "right_elbow_joint": 25,
    "right_wrist_roll_joint": 26,
    "right_wrist_pitch_joint": 27,
    "right_wrist_yaw_joint": 28,
}


def clamp(value: float, lower: float, upper: float) -> float:
    return min(max(value, lower), upper)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--allowed-joints", required=True, help="Comma-separated MuJoCo joint names.")
    parser.add_argument("--rate", type=float, default=200.0, help="LowCmd publish rate.")
    parser.add_argument("--kp", type=float, default=20.0)
    parser.add_argument("--kd", type=float, default=1.0)
    parser.add_argument("--max-delta-rad", type=float, default=math.radians(1.0))
    parser.add_argument("--max-offset-rad", type=float, default=math.radians(10.0))
    parser.add_argument("--stale-timeout-sec", type=float, default=0.5)
    parser.add_argument("--dds-interface", default="")
    parser.add_argument("--release-mode", action="store_true")
    return parser.parse_args()


def import_unitree_sdk():
    from unitree_sdk2py.core.channel import ChannelFactoryInitialize, ChannelPublisher, ChannelSubscriber
    from unitree_sdk2py.idl.default import unitree_hg_msg_dds__LowCmd_
    from unitree_sdk2py.idl.unitree_hg.msg.dds_ import LowCmd_, LowState_
    from unitree_sdk2py.utils.crc import CRC

    try:
        from unitree_sdk2py.comm.motion_switcher.motion_switcher_client import MotionSwitcherClient
    except Exception:
        MotionSwitcherClient = None

    return {
        "ChannelFactoryInitialize": ChannelFactoryInitialize,
        "ChannelPublisher": ChannelPublisher,
        "ChannelSubscriber": ChannelSubscriber,
        "LowCmd_": LowCmd_,
        "LowState_": LowState_,
        "default_lowcmd": unitree_hg_msg_dds__LowCmd_,
        "CRC": CRC,
        "MotionSwitcherClient": MotionSwitcherClient,
    }


def release_high_level_mode(MotionSwitcherClient) -> None:
    if MotionSwitcherClient is None:
        print("[server] MotionSwitcherClient unavailable; cannot release high-level mode", flush=True)
        return
    try:
        client = MotionSwitcherClient()
        client.SetTimeout(5.0)
        client.Init()
        status, result = client.CheckMode()
        print(f"[server] motion mode before release: status={status} result={result}", flush=True)
        client.ReleaseMode()
        time.sleep(0.5)
        status, result = client.CheckMode()
        print(f"[server] motion mode after release: status={status} result={result}", flush=True)
    except Exception as exc:
        print(f"[server] failed to release high-level mode: {exc}", flush=True)


class LowStateBuffer:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.latest = None
        self.updated_at = 0.0

    def callback(self, msg) -> None:
        with self.lock:
            self.latest = msg
            self.updated_at = time.monotonic()

    def wait(self, timeout: float):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            with self.lock:
                if self.latest is not None:
                    return self.latest
            time.sleep(0.01)
        raise TimeoutError("timed out waiting for rt/lowstate")


def stdin_reader(out: queue.Queue) -> None:
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            out.put(json.loads(line))
        except json.JSONDecodeError as exc:
            print(f"[server] bad JSON ignored: {exc}", flush=True)
    out.put({"stop": True, "reason": "stdin_eof"})


def main() -> int:
    args = parse_args()
    allowed_names = tuple(name for name in args.allowed_joints.split(",") if name)
    unknown = [name for name in allowed_names if name not in G1_DDS]
    if unknown:
        print(f"[server] unknown allowed joint(s): {unknown}", file=sys.stderr, flush=True)
        return 2
    allowed_indices = {G1_DDS[name] for name in allowed_names}
    print(f"[server] allowed joints: {allowed_names}", flush=True)

    sdk = import_unitree_sdk()
    if args.dds_interface:
        sdk["ChannelFactoryInitialize"](0, args.dds_interface)
    else:
        sdk["ChannelFactoryInitialize"](0)

    if args.release_mode:
        release_high_level_mode(sdk["MotionSwitcherClient"])
    else:
        print("[server] not releasing high-level mode; pass --release-mode from client when ready", flush=True)

    state_buffer = LowStateBuffer()
    subscriber = sdk["ChannelSubscriber"]("rt/lowstate", sdk["LowState_"])
    subscriber.Init(state_buffer.callback, 10)

    publisher = sdk["ChannelPublisher"]("rt/lowcmd", sdk["LowCmd_"])
    publisher.Init()

    state = state_buffer.wait(timeout=5.0)
    start_q = [float(state.motor_state[i].q) for i in range(29)]
    ready = {
        "type": "ready",
        "start_q": {
            name: start_q[idx]
            for name, idx in G1_DDS.items()
        },
        "allowed_joints": allowed_names,
    }
    print("__G1_READY__ " + json.dumps(ready, separators=(",", ":")), flush=True)
    current_q = list(start_q)
    desired_q = list(start_q)
    kp = [0.0] * 29
    kd = [0.0] * 29
    for i in range(29):
        kp[i] = args.kp
        kd[i] = args.kd

    crc = sdk["CRC"]()
    inbox: queue.Queue = queue.Queue()
    threading.Thread(target=stdin_reader, args=(inbox,), daemon=True).start()

    last_client_msg = time.monotonic()
    period = 1.0 / max(args.rate, 1.0)
    print("[server] streaming LowCmd frames", flush=True)

    while True:
        while True:
            try:
                msg = inbox.get_nowait()
            except queue.Empty:
                break
            if msg.get("stop"):
                print(f"[server] stop requested: {msg.get('reason', 'client')}", flush=True)
                return 0
            last_client_msg = time.monotonic()
            deltas = msg.get("deltas", {})
            if not isinstance(deltas, dict):
                continue
            for name, delta in deltas.items():
                idx = G1_DDS.get(name)
                if idx is None or idx not in allowed_indices:
                    continue
                offset = clamp(float(delta), -args.max_offset_rad, args.max_offset_rad)
                desired_q[idx] = start_q[idx] + offset

        if time.monotonic() - last_client_msg > args.stale_timeout_sec:
            # Hold current desired_q; do not chase new targets until client resumes.
            pass

        cmd = sdk["default_lowcmd"]()
        cmd.mode_pr = 0
        try:
            with state_buffer.lock:
                if state_buffer.latest is not None:
                    cmd.mode_machine = state_buffer.latest.mode_machine
        except Exception:
            pass

        for i in range(29):
            step = clamp(desired_q[i] - current_q[i], -args.max_delta_rad, args.max_delta_rad)
            current_q[i] += step
            cmd.motor_cmd[i].mode = 1
            cmd.motor_cmd[i].q = current_q[i]
            cmd.motor_cmd[i].dq = 0.0
            cmd.motor_cmd[i].kp = kp[i]
            cmd.motor_cmd[i].kd = kd[i]
            cmd.motor_cmd[i].tau = 0.0

        cmd.crc = crc.Crc(cmd)
        publisher.Write(cmd)
        time.sleep(period)


if __name__ == "__main__":
    raise SystemExit(main())
