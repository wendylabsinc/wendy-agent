# WendyDataModelApp — reference app for the model harness

The smallest complete app that proves the three model-harness contracts on
WendyOS, so a Wendy Arm demo needs nothing beyond `wendy run` and
`wendy data campaign deploy campaign.yaml`.

| Contract | Mechanism in this app |
|---|---|
| Sensors in | Webcam frames via Open Computer Vision (OpenCV) over Video4Linux2 (V4L2), granted by the `camera` entitlement |
| Predictions out | One `prediction` record per processed frame, plus a `person_detected` event, over the app-private data socket granted by the `data` entitlement (`WENDY_DATA_SOCKET`) |
| Actuation out | A Robot Operating System 2 (ROS 2) `geometry_msgs/Twist` on `/cmd_vel` when ROS 2 is enabled; the identical control decision is logged when it is not |

```
WendyDataModelApp/
├── wendy.json          ← camera + data entitlements
├── Dockerfile          ← build-time YOLOv8n ONNX export, CPU runtime
├── app.py              ← capture, detect, record, actuate loop
├── wendydata.py        ← data socket client (standard library only)
├── test_wendydata.py   ← unit tests for framing + uncertainty formula
└── campaign.yaml       ← flight-recorder plan armed by the app's records
```

## What the app does

Every loop iteration (at most 5 per second, see rate limiting below):

1. Grabs a frame from `/dev/video0` (override with `WENDY_CAMERA_DEVICE`).
2. Runs YOLOv8n, exported to Open Neural Network Exchange (ONNX) at image
   build time, through onnxruntime.
3. Sends a `prediction` record: model `yolov8n`, the pinned model version,
   the frame's uncertainty score, and the top detections in attributes.
4. Sends a `person_detected` event when a person newly enters the view
   (edge-triggered, so a person standing still fires the campaign once).
5. Computes an actuation decision — turn towards the largest person and
   creep forward when centered — and publishes it as a Twist (ROS 2 mode)
   or logs it (default mode).

### The uncertainty formula

```
uncertainty = 1 - max(confidence over detections >= threshold)
            = 1.0 when nothing was detected above the threshold
```

Clamped to 0..1. A frame the model is sure about scores near 0; an empty
or ambiguous frame scores 1.0. That makes `model.uncertainty > 0.65` in
the campaign collect exactly the frames worth labeling: the ones the
current model cannot explain. The formula lives in
`wendydata.uncertainty_score` and is unit-tested.

### Prediction rate limiting

The agent rejects more than 200 records per second per connection. This
app stays far below it by frame sampling: it processes at most
`WENDY_PREDICTIONS_PER_SECOND` frames per second (default 5) and sends one
prediction per processed frame. Acknowledgements are honored:
`buffered` and `recorded` are success, `rejected` is logged with the
agent's reason, and the client reconnects and retries once when the
socket drops.

## Run on a dev box (no GPU, no ROS 2)

The default image is CPU-only and needs no ROS 2 stack:

```sh
cd Examples/WendyDataModelApp
wendy run
```

The build's export stage installs Ultralytics (pinned to 8.3.63), exports
`yolov8n.onnx` at opset 12, and discards itself; the runtime image carries
only onnxruntime, OpenCV, and NumPy. Without a data socket (running
outside WendyOS, or without the `data` entitlement) the app keeps running
and logs each dropped record; without ROS 2 it logs each actuation
decision. Nothing hard-requires the harness, the GPU, or a robot.

Unit tests (no camera or agent needed):

```sh
python3 -m unittest discover Examples/WendyDataModelApp
```

## Run on a Jetson with the arm (GPU, ROS 2 on)

Two independent switches:

**GPU**: swap the runtime wheel for the Jetson build of onnxruntime. In
the Dockerfile's runtime stage replace `onnxruntime` with
`onnxruntime-gpu` resolved against the Jetson wheel index — the same
wheel `Examples/HelloONNX` resolves with the Stagefile's `cuda: true` GPU
profile — and add the `gpu` entitlement to `wendy.json`. The app already
passes `ort.get_available_providers()`, so the CUDA (Compute Unified
Device Architecture) provider is picked up automatically when present.

**ROS 2 actuation**: build from a ROS base image so `rclpy` is importable
(for example `ros:humble` plus this Dockerfile's pip packages), set the
environment gate, and declare the ROS 2 framework so the agent injects the
Data Distribution Service (DDS) domain:

```jsonc
// wendy.json additions for the ROS 2 variant
"env": { "WENDY_MODEL_APP_ROS2": "1" },
"frameworks": {
  "ros2": { "domainId": 42, "rmw": "rmw_cyclonedds_cpp", "distro": "humble" }
}
```

See `Examples/ROS2` for what `frameworks.ros2` injects
(`ROS_DOMAIN_ID`, `RMW_IMPLEMENTATION`) and the shared-ipc isolation a
multi-container ROS graph wants. With the gate off, or with `rclpy`
missing, the app logs the same decisions it would have published, so the
demo works on a plain Jetson without any ROS 2 stack.

## Deploy the campaign

```sh
wendy data campaign deploy campaign.yaml
wendy data campaign list
```

`campaign.yaml` arms two triggers — the `person_detected` event and
`model.uncertainty > 0.65` — and snapshots the camera every 30 seconds.
The `applications` source is always captured; it does not appear under
`sources:` because it cannot be deselected.

To fire an episode without waiting for a trigger:

```sh
wendy data campaign trigger model-harness-demo --reason commissioning
```

## What to expect in an episode

```sh
wendy data episodes
wendy data inspect <episode-id>
```

- `events.jsonl` — the application records: the app's `prediction`
  records (model, model_version, uncertainty, top detections) and any
  `person_detected` events, each stamped with agent receipt time and
  clock-uncertainty metadata. Records honor the 10-second pre-trigger
  buffer.
- Camera snapshot stills at the 30-second interval (on a camera transport
  that delivers whole encoded access units; other transports record
  continuously and the manifest's source detail says so).
- A manifest tying sources, trigger reason (`event:person_detected` or
  `model_uncertainty:<value>`), and the campaign revision together.

With `upload.when: always`, finished episodes upload whenever the device
has connectivity.
