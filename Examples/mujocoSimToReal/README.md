# MuJoCo Sim-To-Real G1 Prototype

This is a first-pass bridge for moving a Unitree G1 limb from a local MuJoCo
viewer and optionally streaming the same target deltas to the real robot over
SSH.

The default mode is simulation-only. Real robot output requires `--arm-real`.

## Layout

- `sim_to_real.py` - local MuJoCo keyboard teleop and SSH stream client.
- `g1_mapping.py` - explicit MuJoCo joint name to DDS index map.
- `robot/g1_lowcmd_jsonl_server.py` - robot-side Unitree SDK2 LowCmd server.
- `scripts/install_robot_server.sh` - copies the robot server to the G1.
- `docs/support_runbook.md` - support notes and failure checklist.

## Install Local Dependencies

```sh
cd /Users/smile/WendyOS/mujocoSimToReal
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
```

## Run Simulation Only

On macOS, MuJoCo's passive viewer must be run with `mjpython`, not plain
`python`:

```sh
uv run mjpython sim_to_real.py --group left_arm
```

This opens two UIs:

- The MuJoCo 3D viewer.
- A browser joint-control panel at `http://127.0.0.1:8765`.

Move the sliders in the browser panel to move the same joints in MuJoCo.

Keyboard controls still work in the MuJoCo viewer:

- `[` / `]` selects the previous or next joint.
- `-` / `=` moves the selected joint by `--step-deg`.
- `0` resets the selected joint to its simulation start value.
- `R` resets every controlled joint.
- `P` pauses or resumes robot streaming.
- `H` prints the controls.

The script references the existing local model:

```text
/Users/smile/dog/models/unitree_g1_coffee/scene_29dof.xml
```

## Install Robot Server

This copies the server into the existing SDK checkout documented for the G1:

```sh
./scripts/install_robot_server.sh
```

Override defaults if needed:

```sh
ROBOT_HOST=192.168.0.107 ROBOT_USER=unitree ./scripts/install_robot_server.sh
```

## Run Against The Real Robot

Only do this with the robot suspended or otherwise physically safe, e-stop
ready, and a human watching the robot.

The real stream requires passwordless SSH because the bridge uses stdin for
joint target messages. This has been set up for `unitree@192.168.0.107` on this
machine. If it breaks, fix it with:

```sh
ssh-copy-id unitree@192.168.0.107
```

```sh
uv run mjpython sim_to_real.py \
  --group left_arm \
  --arm-real \
  --robot-host 192.168.0.107 \
  --robot-user unitree \
  --real-scale 0.2 \
  --max-real-delta-deg 1.0 \
  --max-real-offset-deg 10.0 \
  --release-mode
```

The bridge sends relative deltas, not raw simulation absolute angles:

```text
real_target = robot_measured_start + real_scale * (sim_target - sim_start)
```

When `--arm-real` is used, the robot server reports its measured startup
`LowState` and the local MuJoCo model initializes all 29 known body joints from
that pose. The UI still only commands the selected group. If a joint still
points the wrong way, that joint needs sign/offset calibration.

## Safety Defaults

- Real robot movement is disabled unless `--arm-real` is present.
- Only the selected joint group is allowed to move.
- The robot server holds all 29 body joints at measured startup positions.
- The local sim syncs all 29 known body joints from robot `LowState` by default.
- Real target deltas are rate-limited and clamped.
- Commands are position targets through Unitree `LowCmd.q`, not raw torque.

Start with `left_arm` or `right_arm`. Do not start with `legs`, `waist`, or
`all` on live hardware.
