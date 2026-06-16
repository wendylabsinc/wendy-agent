#!/usr/bin/env python3
"""Keyboard teleop from local MuJoCo G1 to optional real Unitree G1 DDS bridge."""

from __future__ import annotations

import argparse
import html
import http.server
import json
import math
import os
import platform
import shlex
import socketserver
import subprocess
import sys
import threading
import time
import urllib.parse
import webbrowser
from pathlib import Path

from g1_mapping import G1_JOINTS, JOINT_BY_NAME, GROUPS, clamp_joint, names_for_group


DEFAULT_MODEL = "/Users/smile/dog/models/unitree_g1_coffee/scene_29dof.xml"
DEFAULT_REMOTE_SCRIPT = "/home/unitree/unitree_sdk2_python/g1_lowcmd_jsonl_server.py"


def ssh_timeout(value: float) -> str:
    return str(max(1, int(math.ceil(value))))


def rad(deg: float) -> float:
    return math.radians(deg)


def deg(rad_value: float) -> float:
    return math.degrees(rad_value)


class RobotStream:
    def __init__(self, args: argparse.Namespace, allowed_joints: tuple[str, ...]) -> None:
        self.ready = threading.Event()
        self.start_q: dict[str, float] = {}
        self.exit_code: int | None = None
        allowed = ",".join(allowed_joints)
        remote_args = [
            "python3",
            "-u",
            args.remote_script,
            "--allowed-joints",
            allowed,
            "--rate",
            str(args.robot_publish_rate),
            "--kp",
            str(args.kp),
            "--kd",
            str(args.kd),
            "--max-delta-rad",
            str(rad(args.max_real_delta_deg)),
            "--max-offset-rad",
            str(rad(args.max_real_offset_deg)),
            "--stale-timeout-sec",
            str(args.stale_timeout_sec),
        ]
        if args.release_mode:
            remote_args.append("--release-mode")
        if args.dds_interface:
            remote_args.extend(["--dds-interface", args.dds_interface])

        remote_cmd = "cd /home/unitree/unitree_sdk2_python && exec " + " ".join(
            shlex.quote(part) for part in remote_args
        )
        ssh_cmd = [
            "ssh",
            "-o",
            "BatchMode=yes",
            "-o",
            f"ConnectTimeout={ssh_timeout(args.ssh_connect_timeout)}",
            "-o",
            "ServerAliveInterval=1",
            "-o",
            "ServerAliveCountMax=3",
            f"{args.robot_user}@{args.robot_host}",
            remote_cmd,
        ]
        print("[robot] starting:", " ".join(shlex.quote(part) for part in ssh_cmd))
        self.proc = subprocess.Popen(
            ssh_cmd,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
        )
        self._closed = False
        self._start_reader("stdout", self.proc.stdout)
        self._start_reader("stderr", self.proc.stderr)
        self._start_waiter()

    def _start_reader(self, name: str, pipe) -> None:
        def run() -> None:
            if pipe is None:
                return
            for line in pipe:
                if line.startswith("__G1_READY__ "):
                    try:
                        payload = json.loads(line.removeprefix("__G1_READY__ "))
                        self.start_q = {
                            str(joint): float(value)
                            for joint, value in payload.get("start_q", {}).items()
                        }
                        self.ready.set()
                    except (TypeError, ValueError, json.JSONDecodeError) as exc:
                        print(f"[robot:{name}] bad ready payload: {exc}")
                    continue
                print(f"[robot:{name}] {line}", end="")

        thread = threading.Thread(target=run, daemon=True)
        thread.start()

    def _start_waiter(self) -> None:
        def run() -> None:
            self.exit_code = self.proc.wait()
            if not self.ready.is_set():
                self.ready.set()

        thread = threading.Thread(target=run, daemon=True)
        thread.start()

    def wait_ready(self, timeout: float) -> bool:
        if not self.ready.wait(timeout):
            return False
        return self.exit_code is None and bool(self.start_q)

    def send(self, seq: int, deltas: dict[str, float]) -> None:
        if self._closed or self.proc.stdin is None:
            return
        payload = {"seq": seq, "time": time.time(), "deltas": deltas}
        try:
            self.proc.stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
            self.proc.stdin.flush()
        except BrokenPipeError:
            self._closed = True
            print("[robot] ssh pipe closed")

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        if self.proc.stdin is not None:
            try:
                self.proc.stdin.write(json.dumps({"stop": True}) + "\n")
                self.proc.stdin.flush()
                self.proc.stdin.close()
            except BrokenPipeError:
                pass
        try:
            self.proc.wait(timeout=3.0)
        except subprocess.TimeoutExpired:
            self.proc.terminate()


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", default=DEFAULT_MODEL, help="Path to G1 MuJoCo scene XML.")
    parser.add_argument("--group", default="left_arm", choices=sorted(GROUPS), help="Joint group to control.")
    parser.add_argument(
        "--joints",
        nargs="+",
        help="Explicit MuJoCo joint names. Overrides --group.",
    )
    parser.add_argument("--step-deg", type=float, default=2.0, help="Keyboard target increment in degrees.")
    parser.add_argument("--ui-port", type=int, default=8765, help="Browser joint-control UI port.")
    parser.add_argument("--no-browser", action="store_true", help="Do not automatically open the browser control UI.")
    parser.add_argument("--no-web-ui", action="store_true", help="Disable the browser joint-control UI.")
    parser.add_argument("--send-rate", type=float, default=25.0, help="Target stream rate to robot.")
    parser.add_argument("--arm-real", action="store_true", help="Actually stream target deltas to the real robot.")
    parser.add_argument("--robot-host", default="192.168.0.107")
    parser.add_argument("--robot-user", default="unitree")
    parser.add_argument("--remote-script", default=DEFAULT_REMOTE_SCRIPT)
    parser.add_argument("--robot-publish-rate", type=float, default=200.0)
    parser.add_argument("--robot-ready-timeout", type=float, default=10.0)
    parser.add_argument("--ssh-connect-timeout", type=float, default=5.0)
    parser.add_argument("--dds-interface", default="", help="Optional DDS network interface for Unitree SDK2.")
    parser.add_argument("--release-mode", action="store_true", help="Ask robot server to release high-level mode.")
    parser.add_argument("--no-sync-start", action="store_true", help="Do not initialize MuJoCo body joints from robot LowState.")
    parser.add_argument("--real-scale", type=float, default=0.2, help="Scale sim deltas before streaming to hardware.")
    parser.add_argument("--max-real-delta-deg", type=float, default=1.0, help="Robot-side per-cycle delta clamp.")
    parser.add_argument("--max-real-offset-deg", type=float, default=10.0, help="Robot-side offset clamp from measured start.")
    parser.add_argument("--stale-timeout-sec", type=float, default=0.5, help="Robot holds current targets if client data goes stale.")
    parser.add_argument("--kp", type=float, default=20.0)
    parser.add_argument("--kd", type=float, default=1.0)
    return parser


def print_help(joints: tuple[str, ...]) -> None:
    print("\nControls")
    print("  [ / ]    previous / next joint")
    print("  - / =    decrease / increase selected target")
    print("  0        reset selected joint")
    print("  R        reset all controlled joints")
    print("  P        pause / resume robot streaming")
    print("  H        print this help")
    print("\nControlled joints:")
    for name in joints:
        joint = JOINT_BY_NAME[name]
        print(f"  {name:30s} dds={joint.dds_index:2d} limit=[{joint.lower:+.3f}, {joint.upper:+.3f}] rad")
    print()


def verify_passwordless_ssh(args: argparse.Namespace) -> bool:
    cmd = [
        "ssh",
        "-o",
        "BatchMode=yes",
        "-o",
        f"ConnectTimeout={ssh_timeout(args.ssh_connect_timeout)}",
        f"{args.robot_user}@{args.robot_host}",
        "true",
    ]
    result = subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    if result.returncode == 0:
        return True
    print("[robot] passwordless SSH is not working; live streaming cannot start.", file=sys.stderr)
    print("[robot] fix with:", file=sys.stderr)
    print(f"  ssh-copy-id {args.robot_user}@{args.robot_host}", file=sys.stderr)
    if result.stderr.strip():
        print("[robot] ssh error: " + result.stderr.strip(), file=sys.stderr)
    return False


class ControlPanel:
    def __init__(
        self,
        *,
        port: int,
        joints: tuple[str, ...],
        targets: dict[str, float],
        sim_start: dict[str, float],
        lock: threading.Lock,
        selected: dict[str, int],
        robot_paused: dict[str, bool],
    ) -> None:
        self.port = port
        self.joints = joints
        self.targets = targets
        self.sim_start = sim_start
        self.lock = lock
        self.selected = selected
        self.robot_paused = robot_paused
        panel = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                if self.path == "/" or self.path.startswith("/?"):
                    self._send_html(panel.render_html())
                    return
                if self.path == "/state":
                    self._send_json(panel.snapshot())
                    return
                self.send_error(404)

            def do_POST(self) -> None:
                length = int(self.headers.get("Content-Length", "0"))
                body = self.rfile.read(length).decode("utf-8")
                params = urllib.parse.parse_qs(body)
                if self.path == "/set":
                    try:
                        name = params["name"][0]
                        value = float(params["value"][0])
                        panel.set_target(name, value)
                    except (KeyError, ValueError, IndexError) as exc:
                        self.send_error(400, str(exc))
                        return
                    self._send_json(panel.snapshot())
                    return
                if self.path == "/select":
                    try:
                        panel.select(params["name"][0])
                    except (KeyError, ValueError, IndexError) as exc:
                        self.send_error(400, str(exc))
                        return
                    self._send_json(panel.snapshot())
                    return
                if self.path == "/reset":
                    name = params.get("name", [""])[0]
                    panel.reset(name or None)
                    self._send_json(panel.snapshot())
                    return
                if self.path == "/pause":
                    panel.toggle_pause()
                    self._send_json(panel.snapshot())
                    return
                self.send_error(404)

            def log_message(self, fmt: str, *args: object) -> None:
                return

            def _send_html(self, body: str) -> None:
                data = body.encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def _send_json(self, value: object) -> None:
                data = json.dumps(value).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        class ThreadingHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
            daemon_threads = True
            allow_reuse_address = True

        self.server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def start(self, open_browser: bool) -> str:
        self.thread.start()
        url = f"http://127.0.0.1:{self.port}/"
        print(f"[ui] joint control panel: {url}")
        if open_browser:
            webbrowser.open(url)
        return url

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    def set_target(self, name: str, value: float) -> None:
        if name not in self.targets:
            raise ValueError(f"unknown controlled joint {name!r}")
        with self.lock:
            self.targets[name] = clamp_joint(name, value)
            self.selected["idx"] = self.joints.index(name)

    def select(self, name: str) -> None:
        if name not in self.targets:
            raise ValueError(f"unknown controlled joint {name!r}")
        with self.lock:
            self.selected["idx"] = self.joints.index(name)

    def reset(self, name: str | None = None) -> None:
        with self.lock:
            names = (name,) if name else self.joints
            for joint_name in names:
                if joint_name not in self.targets:
                    raise ValueError(f"unknown controlled joint {joint_name!r}")
                self.targets[joint_name] = self.sim_start[joint_name]

    def toggle_pause(self) -> None:
        with self.lock:
            self.robot_paused["value"] = not self.robot_paused["value"]

    def snapshot(self) -> dict[str, object]:
        with self.lock:
            selected_name = self.joints[self.selected["idx"] % len(self.joints)]
            return {
                "selected": selected_name,
                "robot_paused": self.robot_paused["value"],
                "joints": [
                    {
                        "name": name,
                        "dds": JOINT_BY_NAME[name].dds_index,
                        "group": JOINT_BY_NAME[name].group,
                        "lower": JOINT_BY_NAME[name].lower,
                        "upper": JOINT_BY_NAME[name].upper,
                        "target": self.targets[name],
                        "start": self.sim_start[name],
                        "delta": self.targets[name] - self.sim_start[name],
                    }
                    for name in self.joints
                ],
            }

    def render_html(self) -> str:
        rows = []
        for name in self.joints:
            joint = JOINT_BY_NAME[name]
            safe_name = html.escape(name)
            rows.append(
                f"""
                <section class="joint" data-name="{safe_name}">
                  <div class="topline">
                    <button onclick="selectJoint('{safe_name}')">DDS {joint.dds_index}</button>
                    <strong>{safe_name}</strong>
                    <span class="group">{html.escape(joint.group)}</span>
                  </div>
                  <input
                    id="slider-{safe_name}"
                    type="range"
                    min="{deg(joint.lower):.3f}"
                    max="{deg(joint.upper):.3f}"
                    step="0.1"
                    oninput="setJointDeg('{safe_name}', this.value)"
                  />
                  <div class="readout">
                    <span id="target-{safe_name}"></span>
                    <span id="delta-{safe_name}"></span>
                    <button onclick="resetJoint('{safe_name}')">Reset</button>
                  </div>
                </section>
                """
            )
        return f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>G1 MuJoCo Sim-To-Real</title>
  <style>
    :root {{
      color-scheme: light dark;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: #121417;
      color: #eef1f4;
    }}
    body {{
      margin: 0;
      padding: 24px;
      background: #121417;
    }}
    main {{
      max-width: 980px;
      margin: 0 auto;
    }}
    header {{
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      margin-bottom: 18px;
    }}
    h1 {{
      font-size: 22px;
      line-height: 1.2;
      margin: 0;
    }}
    .status {{
      display: flex;
      align-items: center;
      gap: 10px;
      font-size: 14px;
    }}
    .joint {{
      border: 1px solid #323942;
      border-radius: 8px;
      padding: 14px;
      margin-bottom: 10px;
      background: #1a1f25;
    }}
    .joint.selected {{
      border-color: #74b9ff;
      box-shadow: inset 3px 0 0 #74b9ff;
    }}
    .topline, .readout {{
      display: flex;
      align-items: center;
      gap: 10px;
      flex-wrap: wrap;
    }}
    .topline {{
      margin-bottom: 10px;
    }}
    .readout {{
      margin-top: 8px;
      color: #c4cbd3;
      font-size: 13px;
    }}
    .group {{
      color: #9aa5b1;
      font-size: 13px;
    }}
    input[type="range"] {{
      width: 100%;
    }}
    button {{
      border: 1px solid #46515d;
      border-radius: 6px;
      padding: 6px 10px;
      background: #242b33;
      color: #eef1f4;
      cursor: pointer;
    }}
    button:hover {{
      background: #2d3640;
    }}
  </style>
</head>
<body>
  <main>
    <header>
      <h1>G1 MuJoCo Sim-To-Real Joint Control</h1>
      <div class="status">
        <span id="selected"></span>
        <button onclick="resetAll()">Reset All</button>
        <button id="pause" onclick="togglePause()">Pause Robot Stream</button>
      </div>
    </header>
    {''.join(rows)}
  </main>
  <script>
    const radToDeg = (v) => v * 180 / Math.PI;
    const degToRad = (v) => v * Math.PI / 180;

    async function post(path, params) {{
      const body = new URLSearchParams(params || {{}});
      const res = await fetch(path, {{ method: 'POST', body }});
      if (!res.ok) throw new Error(await res.text());
      return res.json();
    }}

    async function setJointDeg(name, degrees) {{
      await post('/set', {{ name, value: String(degToRad(Number(degrees))) }});
      refresh();
    }}

    async function selectJoint(name) {{
      await post('/select', {{ name }});
      refresh();
    }}

    async function resetJoint(name) {{
      await post('/reset', {{ name }});
      refresh();
    }}

    async function resetAll() {{
      await post('/reset', {{}});
      refresh();
    }}

    async function togglePause() {{
      await post('/pause', {{}});
      refresh();
    }}

    async function refresh() {{
      const state = await (await fetch('/state')).json();
      document.getElementById('selected').textContent = `Selected: ${{state.selected}}`;
      document.getElementById('pause').textContent =
        state.robot_paused ? 'Resume Robot Stream' : 'Pause Robot Stream';
      for (const joint of state.joints) {{
        const slider = document.getElementById(`slider-${{joint.name}}`);
        const target = document.getElementById(`target-${{joint.name}}`);
        const delta = document.getElementById(`delta-${{joint.name}}`);
        if (document.activeElement !== slider) {{
          slider.value = radToDeg(joint.target);
        }}
        target.textContent = `target ${{radToDeg(joint.target).toFixed(2)}} deg`;
        delta.textContent = `delta ${{radToDeg(joint.delta).toFixed(2)}} deg`;
        document.querySelector(`[data-name="${{joint.name}}"]`).classList.toggle(
          'selected',
          joint.name === state.selected
        );
      }}
    }}

    refresh();
    setInterval(refresh, 250);
  </script>
</body>
</html>"""


def main() -> int:
    args = build_parser().parse_args()
    model_path = Path(args.model)
    if not model_path.exists():
        print(f"model not found: {model_path}", file=sys.stderr)
        return 2

    joints = tuple(args.joints) if args.joints else names_for_group(args.group)
    unknown = [name for name in joints if name not in JOINT_BY_NAME]
    if unknown:
        print(f"unknown joint(s): {', '.join(unknown)}", file=sys.stderr)
        return 2

    non_arm = [name for name in joints if JOINT_BY_NAME[name].group not in {"left_arm", "right_arm"}]
    if args.arm_real and non_arm:
        print("Refusing live robot output for non-arm joints in this first prototype.", file=sys.stderr)
        print("Use simulation-only for legs/waist/all until arm behavior is characterized.", file=sys.stderr)
        print("Blocked joint(s): " + ", ".join(non_arm), file=sys.stderr)
        return 2

    try:
        import mujoco
        import mujoco.viewer
    except ImportError as exc:
        print("Missing dependency. Run: pip install -r requirements.txt", file=sys.stderr)
        print(str(exc), file=sys.stderr)
        return 2

    launched_with_mjpython = bool(os.environ.get("MJPYTHON_BIN"))
    if platform.system() == "Darwin" and not launched_with_mjpython:
        print("MuJoCo viewer on macOS must be launched with mjpython.", file=sys.stderr)
        print("Run:", file=sys.stderr)
        print("  cd /Users/smile/WendyOS/mujocoSimToReal", file=sys.stderr)
        print("  uv run mjpython sim_to_real.py --group left_arm", file=sys.stderr)
        print("For live arm streaming, add --arm-real after confirming the robot is physically safe.", file=sys.stderr)
        return 2

    model = mujoco.MjModel.from_xml_path(os.fspath(model_path))
    data = mujoco.MjData(model)
    mujoco.mj_forward(model, data)

    qadr: dict[str, int] = {}
    for name in joints:
        jid = mujoco.mj_name2id(model, mujoco.mjtObj.mjOBJ_JOINT, name)
        if jid < 0:
            print(f"joint {name!r} is not in model {model_path}", file=sys.stderr)
            return 2
        qadr[name] = int(model.jnt_qposadr[jid])
    all_qadr: dict[str, int] = {}
    for joint in G1_JOINTS:
        jid = mujoco.mj_name2id(model, mujoco.mjtObj.mjOBJ_JOINT, joint.mujoco)
        if jid >= 0:
            all_qadr[joint.mujoco] = int(model.jnt_qposadr[jid])

    lock = threading.Lock()
    selected = {"idx": 0}
    targets = {name: float(data.qpos[qadr[name]]) for name in joints}
    sim_start = dict(targets)
    robot_paused = {"value": False}
    step = rad(args.step_deg)

    print_help(joints)
    if args.arm_real:
        if not verify_passwordless_ssh(args):
            return 2
        print("[robot] REAL ROBOT STREAMING IS ENABLED")
        robot = RobotStream(args, joints)
        print("[robot] waiting for rt/lowstate and startup pose...")
        if not robot.wait_ready(args.robot_ready_timeout):
            print("[robot] robot server did not become ready; no commands will be sent.", file=sys.stderr)
            robot.close()
            return 2
        if not args.no_sync_start:
            clamped: list[str] = []
            with lock:
                for name, measured in robot.start_q.items():
                    if name not in all_qadr:
                        continue
                    synced = clamp_joint(name, measured)
                    if synced != measured:
                        clamped.append(f"{name}={measured:+.3f}->{synced:+.3f}")
                    data.qpos[all_qadr[name]] = synced
                    if name in targets:
                        targets[name] = synced
                        sim_start[name] = synced
            mujoco.mj_forward(model, data)
            print(f"[robot] synced {len(robot.start_q)} MuJoCo body joints from robot LowState")
            if clamped:
                print("[robot] clamped startup joints outside MJCF limits: " + ", ".join(clamped))
    else:
        print("[robot] simulation-only; pass --arm-real to stream to hardware")
        robot = None

    panel = None
    if not args.no_web_ui:
        panel = ControlPanel(
            port=args.ui_port,
            joints=joints,
            targets=targets,
            sim_start=sim_start,
            lock=lock,
            selected=selected,
            robot_paused=robot_paused,
        )
        panel.start(open_browser=not args.no_browser)

    def selected_name() -> str:
        return joints[selected["idx"] % len(joints)]

    def report_selected() -> None:
        name = selected_name()
        delta = targets[name] - sim_start[name]
        print(
            f"[sim] selected={name} target={targets[name]:+.4f} rad "
            f"delta={delta:+.4f} rad ({deg(delta):+.2f} deg)"
        )

    def key_callback(keycode: int) -> None:
        char = chr(keycode).lower() if 0 <= keycode < 128 else ""
        with lock:
            if char == "]":
                selected["idx"] = (selected["idx"] + 1) % len(joints)
                report_selected()
            elif char == "[":
                selected["idx"] = (selected["idx"] - 1) % len(joints)
                report_selected()
            elif char in {"=", "+"}:
                name = selected_name()
                targets[name] = clamp_joint(name, targets[name] + step)
                report_selected()
            elif char in {"-", "_"}:
                name = selected_name()
                targets[name] = clamp_joint(name, targets[name] - step)
                report_selected()
            elif char == "0":
                name = selected_name()
                targets[name] = sim_start[name]
                report_selected()
            elif char == "r":
                for name in joints:
                    targets[name] = sim_start[name]
                print("[sim] reset all controlled joints")
            elif char == "p":
                robot_paused["value"] = not robot_paused["value"]
                state = "paused" if robot_paused["value"] else "resumed"
                print(f"[robot] streaming {state}")
            elif char == "h":
                print_help(joints)

    seq = 0
    last_send = 0.0
    send_period = 1.0 / max(args.send_rate, 1.0)

    try:
        with mujoco.viewer.launch_passive(model, data, key_callback=key_callback) as viewer:
            while viewer.is_running():
                with lock:
                    for name, target in targets.items():
                        data.qpos[qadr[name]] = target
                    current_deltas = {
                        name: args.real_scale * (targets[name] - sim_start[name])
                        for name in joints
                    }

                mujoco.mj_forward(model, data)
                viewer.sync()

                now = time.monotonic()
                if robot is not None and not robot_paused["value"] and now - last_send >= send_period:
                    seq += 1
                    robot.send(seq, current_deltas)
                    last_send = now
                time.sleep(0.005)
    except KeyboardInterrupt:
        print("\n[sim] stopped")
    finally:
        if panel is not None:
            panel.close()
        if robot is not None:
            robot.close()

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
