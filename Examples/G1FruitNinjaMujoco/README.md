# G1 Fruit Ninja — instant MuJoCo deployment with Wendy

This is a self-contained, recording-ready port of the IsaacLab G1 Fruit Ninja
demo. It deploys a full 29-DOF Unitree G1 MuJoCo scene and a browser UI with a
live rendered stream, telemetry, reset control, and cinematic mode.

The fruit trajectories, blade contacts, gravity, and split-fruit debris are
real MuJoCo dynamics. The upper-body strike is currently a deterministic
scripted controller; an Isaac/RSL-RL checkpoint has **not** been converted or
claimed as a MuJoCo policy. The pelvis is welded to a supervised support, just
like the supported-base training configuration. This project never commands a
physical robot.

## Run locally with Wendy + Docker Desktop

From this directory:

```sh
wendy run --device docker --build-type compose
```

Then open:

```text
http://localhost:8878
```

The Compose path publishes port 8878 and selects OSMesa, so it works without a
local NVIDIA GPU. Leave the command running while recording; press Ctrl-C to
stop it.

## Run on the DGX Spark

First confirm the Mac is on the same LAN and the Spark is visible:

```sh
wendy device list | rg spark-48fd.local
```

From this directory:

```sh
wendy run --device spark-48fd.local --build-type docker
```

Then open:

```text
http://spark-48fd.local:8878
```

`wendy.json` grants the container GPU access and host networking. The runtime
preflights EGL first when NVIDIA devices are visible, then falls back to OSMesa
if EGL cannot create a render context. Port 8878 intentionally does not replace
the older `g1-fruit-ninja-smoke` service on port 8877.

If discovery returns nothing, reconnect to the Spark's LAN before running the
deployment command. A registry TLS timeout after the device disappears from
discovery is a network-path failure, not a MuJoCo build failure.

For a detached deployment:

```sh
wendy run --device spark-48fd.local --build-type docker --detach
```

## Recording the instant-deployment shot

1. Put the terminal on the left and a browser at the target URL on the right.
2. Start screen recording before entering the `wendy run` command.
3. Enter the one-line command and leave the build/deploy output visible.
4. When the readiness gate passes, refresh/open the browser.
5. Click **CINEMATIC**, or append `?cinematic=1` to the URL.
6. Let three fruits loop, then stop the recording.

Health and runtime evidence are available at:

```text
/api/health
/api/status
/stream.mjpg
```

Reset the deterministic loop with `POST /api/reset` or the UI button.

## Direct Docker fallback

If you want to separate Wendy setup from app debugging:

```sh
docker compose up --build
```

## Native developer checks

The supported deployment artifact is the container. For a native Python check:

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r requirements.txt pytest
MUJOCO_GL=glfw python -m fruit_ninja.server
```

On headless Linux use `MUJOCO_GL=osmesa`; on a GPU-enabled Spark container use
`MUJOCO_GL=egl`.

Run tests with:

```sh
pytest -q
```

## Port boundary from IsaacLab

Preserved:

- supported/fixed base and upper-body strike shape;
- 50 Hz control loop and deterministic fruit interception;
- bounded target rate, joint-limit margin, predictive pose collision rejection;
- visible fruit flight and hit telemetry;
- simulation-only boundary.

Changed intentionally:

- Isaac PhysX dynamics → MuJoCo 3.x dynamics;
- RSL-RL policy → deterministic scripted strike;
- analytic detector observations → direct simulation telemetry for the demo UI;
- Isaac renderer/video artifact → live headless MuJoCo MJPEG stream;
- one fruit per evaluation episode → three-color looping recording sequence.

The original policy observation/action normalization and actuator model must be
exported alongside a checkpoint before policy-output parity can be validated.

## Vendored model

The G1 XML and referenced meshes come from Unitree's BSD-licensed
`unitree_mujoco` repository. Exact provenance is recorded in
`models/unitree_g1/UPSTREAM.md`; the upstream license is vendored beside it.
