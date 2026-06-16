# Support Runbook

## Purpose

This folder tests the smallest useful sim-to-real loop:

```text
MuJoCo keyboard target -> scaled joint delta -> SSH JSONL stream -> Unitree SDK2 LowCmd
```

It is not a learned policy and it is not a whole-body controller.

## Expected Setup

- Local model: `/Users/smile/dog/models/unitree_g1_coffee/scene_29dof.xml`
- Robot host: `192.168.0.107`
- Robot user: `unitree`
- Robot SDK checkout: `/home/unitree/unitree_sdk2_python`
- Robot server path: `/home/unitree/unitree_sdk2_python/g1_lowcmd_jsonl_server.py`

## Preflight

1. Robot is suspended or otherwise physically safe.
2. E-stop is reachable.
3. Run simulation-only first:

   ```sh
   cd /Users/smile/WendyOS/mujocoSimToReal
   . .venv/bin/activate
   uv run mjpython sim_to_real.py --group left_arm
   ```

   On macOS, using plain `python` will fail because MuJoCo's passive viewer
   requires `mjpython`.

4. Install the robot server:

   ```sh
   ./scripts/install_robot_server.sh
   ```

5. Start real streaming with conservative limits:

   ```sh
   uv run mjpython sim_to_real.py --group left_arm --arm-real --real-scale 0.2 \
     --max-real-delta-deg 1.0 --max-real-offset-deg 10.0 --release-mode
   ```

## What Must Be True For Hardware Movement

- The local command includes `--arm-real`. Without it, the UI is simulation
  only and no SSH process is started.
- Passwordless SSH works:

  ```sh
  ssh -o BatchMode=yes unitree@192.168.0.107 true
  ```

- `rt/lowstate` is visible to the robot-side script.
- `rt/lowcmd` publish succeeds.
- High-level motion mode has been released.
- The selected MuJoCo joint name maps to the intended DDS index.
- The robot-side server has the joint in `--allowed-joints`.
- The sim startup pose was synced for all 29 known body joints from the robot server's measured `LowState`
  unless `--no-sync-start` was used.

## Common Failure Modes

### SSH connects but Python import fails

The Unitree SDK checkout is missing or the script is not running from:

```text
/home/unitree/unitree_sdk2_python
```

### No movement

First check whether `--arm-real` was passed. If the console says
`simulation-only`, no robot command stream exists.

Then check whether passwordless SSH works. The bridge cannot answer a password
prompt because stdin is reserved for JSON target messages.

Finally, check whether `--release-mode` was passed. The robot may ignore
low-level commands while a high-level mode is active.

### Sim pose does not match robot pose

With `--arm-real`, the script syncs all 29 known body joints from robot
`LowState` before opening the viewer. If an arm still points the wrong
direction, the MuJoCo-to-DDS sign or zero offset for that joint is not
calibrated yet.

### Joint moves opposite from MuJoCo

Stop and record the mismatch in a calibration table. Fix the sign mapping
before increasing range or speed.

### Joint moves too far

Lower `--real-scale`, `--max-real-delta-deg`, and `--max-real-offset-deg`.
The first hardware envelope should be small arm-only motion.

### Stream disconnects

The robot server exits on stdin EOF. Restart the local script after checking
robot posture and mode.

## Support Evidence To Capture

- Exact command line.
- Robot IP and SDK path.
- Controlled joint group.
- `real_scale`, `max_real_delta_deg`, `max_real_offset_deg`, `kp`, `kd`.
- Console output from both `[sim]` and `[robot:*]` lines.
- Which MuJoCo joint moved and what physical motion was observed.
