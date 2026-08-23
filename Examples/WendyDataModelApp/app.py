#!/usr/bin/env python3
"""Reference model app for the Wendy model harness.

Demonstrates the three harness contracts end to end:

  1. Sensors in    — camera frames FED BY THE HARNESS over the app-private
                     sensor socket granted by the sensors entitlement. The
                     app opens no device: it subscribes to the same
                     producer the episode capture adapter consumes, so the
                     two never fight over the camera, and every frame the
                     model sees is recorded into the active episode under
                     the identifier the app was given.
  2. Predictions out — one "prediction" record per processed frame, naming
                     the sample identifiers it was computed from, plus a
                     "person_detected" event, over the app-private data
                     socket granted by the data entitlement.
  3. Actuation out — a Robot Operating System 2 (ROS 2) Twist command on
                     /cmd_vel when ROS 2 is available and enabled; the
                     same decision is logged when it is not.

The detector is YOLOv8n exported to Open Neural Network Exchange (ONNX)
at image build time and run with onnxruntime on the Central Processing
Unit (CPU) by default, so the app runs on any dev box. See README.md for
the Jetson Graphics Processing Unit (GPU) wheel path.
"""

from __future__ import annotations

import logging
import os
import sys
import time

import cv2
import numpy as np
import onnxruntime as ort

import wendydata
import wendysensors

log = logging.getLogger("model-app")

MODEL_NAME = "yolov8n"
# Pinned in the Dockerfile export stage; keep the two in sync.
MODEL_VERSION = os.environ.get("WENDY_MODEL_VERSION", "8.3.63")
MODEL_PATH = os.environ.get("WENDY_MODEL_PATH", os.path.join(os.path.dirname(__file__), "yolov8n.onnx"))
# The harness source to consume, as reported by `wendy data sources`
# (for example "v4l2:/dev/video0" or "ipcamera:200"). Empty means "pick the
# first subscribable camera the harness offers", which is what a
# single-camera demo device wants.
CAMERA_SOURCE = os.environ.get("WENDY_CAMERA_SOURCE", "")
CONFIDENCE_THRESHOLD = float(os.environ.get("WENDY_CONFIDENCE_THRESHOLD", "0.25"))
IOU_THRESHOLD = float(os.environ.get("WENDY_IOU_THRESHOLD", "0.45"))
# Rate-limit friendliness: the agent caps an app at 200 records per second
# across its connections; this app skips frames so it sends at most this
# many predictions per second (default 5). Skipping happens AFTER the
# harness delivered the frame, so the episode's model-input ledger records
# every frame the app received, including the ones it chose not to score.
PREDICTIONS_PER_SECOND = float(os.environ.get("WENDY_PREDICTIONS_PER_SECOND", "5"))
# Actuation is optional: a plain Jetson demo has no ROS 2 stack. Set
# WENDY_MODEL_APP_ROS2=1 (and run an image with rclpy) to publish Twists.
ROS2_REQUESTED = os.environ.get("WENDY_MODEL_APP_ROS2", "0") == "1"
ACTUATION_TOPIC = os.environ.get("WENDY_ACTUATION_TOPIC", "/cmd_vel")
INPUT_SIZE = 640
PERSON_CLASS_ID = 0
TOP_DETECTIONS = 5

COCO_CLASSES = [
    "person", "bicycle", "car", "motorcycle", "airplane", "bus", "train", "truck", "boat",
    "traffic light", "fire hydrant", "stop sign", "parking meter", "bench", "bird", "cat",
    "dog", "horse", "sheep", "cow", "elephant", "bear", "zebra", "giraffe", "backpack",
    "umbrella", "handbag", "tie", "suitcase", "frisbee", "skis", "snowboard", "sports ball",
    "kite", "baseball bat", "baseball glove", "skateboard", "surfboard", "tennis racket",
    "bottle", "wine glass", "cup", "fork", "knife", "spoon", "bowl", "banana", "apple",
    "sandwich", "orange", "broccoli", "carrot", "hot dog", "pizza", "donut", "cake", "chair",
    "couch", "potted plant", "bed", "dining table", "toilet", "tv", "laptop", "mouse",
    "remote", "keyboard", "cell phone", "microwave", "oven", "toaster", "sink",
    "refrigerator", "book", "clock", "vase", "scissors", "teddy bear", "hair drier",
    "toothbrush",
]


class Actuator:
    """Publishes Twist commands over ROS 2 when available, or logs the
    decision when not. Both modes exercise the same control logic."""

    def __init__(self):
        self.node = None
        self.publisher = None
        self.twist_type = None
        if not ROS2_REQUESTED:
            log.info("actuation: ROS 2 disabled (set WENDY_MODEL_APP_ROS2=1 to publish %s)", ACTUATION_TOPIC)
            return
        try:
            import rclpy
            from geometry_msgs.msg import Twist
        except ImportError as exc:
            log.warning("actuation: ROS 2 requested but rclpy is unavailable (%s); logging decisions instead", exc)
            return
        rclpy.init()
        self.node = rclpy.create_node("wendy_data_model_app")
        self.publisher = self.node.create_publisher(Twist, ACTUATION_TOPIC, 10)
        self.twist_type = Twist
        log.info("actuation: publishing geometry_msgs/Twist on %s", ACTUATION_TOPIC)

    def act(self, angular_z: float, linear_x: float) -> None:
        if self.publisher is None:
            log.info("actuation decision (not published): linear.x=%.3f angular.z=%.3f", linear_x, angular_z)
            return
        msg = self.twist_type()
        msg.linear.x = float(linear_x)
        msg.angular.z = float(angular_z)
        self.publisher.publish(msg)

    def close(self) -> None:
        if self.node is not None:
            import rclpy

            self.node.destroy_node()
            rclpy.shutdown()


def steer_towards(detections, frame_width: int) -> tuple[float, float]:
    """A demo proportional controller: turn towards the largest detected
    person and creep forward when one is roughly centered. Returns
    (angular_z, linear_x); zeros when no person is in view."""
    people = [d for d in detections if d["class_id"] == PERSON_CLASS_ID]
    if not people or frame_width <= 0:
        return 0.0, 0.0
    largest = max(people, key=lambda d: d["box"][2] * d["box"][3])
    x, _, w, _ = largest["box"]
    center_offset = ((x + w / 2.0) / frame_width) - 0.5  # -0.5 .. 0.5
    angular_z = -1.2 * center_offset
    linear_x = 0.1 if abs(center_offset) < 0.15 else 0.0
    return angular_z, linear_x


def letterbox(frame: np.ndarray) -> tuple[np.ndarray, float, int, int]:
    """Resize with preserved aspect ratio onto a 640x640 canvas."""
    h, w = frame.shape[:2]
    scale = min(INPUT_SIZE / w, INPUT_SIZE / h)
    nw, nh = int(round(w * scale)), int(round(h * scale))
    resized = cv2.resize(frame, (nw, nh))
    canvas = np.full((INPUT_SIZE, INPUT_SIZE, 3), 114, dtype=np.uint8)
    dx, dy = (INPUT_SIZE - nw) // 2, (INPUT_SIZE - nh) // 2
    canvas[dy : dy + nh, dx : dx + nw] = resized
    return canvas, scale, dx, dy


def detect(session: ort.InferenceSession, input_name: str, frame: np.ndarray) -> list[dict]:
    """Run YOLOv8n and return [{class_id, class_name, confidence, box}]
    with boxes as [x, y, w, h] in original frame pixels."""
    canvas, scale, dx, dy = letterbox(frame)
    blob = canvas[:, :, ::-1].astype(np.float32) / 255.0  # BGR -> RGB
    blob = np.transpose(blob, (2, 0, 1))[np.newaxis, ...]
    (output,) = session.run(None, {input_name: blob})
    # Output shape (1, 84, 8400): 4 box coordinates + 80 class scores.
    predictions = np.squeeze(output, axis=0).T  # (8400, 84)
    scores = predictions[:, 4:]
    class_ids = np.argmax(scores, axis=1)
    confidences = scores[np.arange(len(class_ids)), class_ids]
    keep = confidences >= CONFIDENCE_THRESHOLD
    boxes_cxcywh = predictions[keep, :4]
    class_ids = class_ids[keep]
    confidences = confidences[keep]
    boxes = []
    for cx, cy, bw, bh in boxes_cxcywh:
        x = (cx - bw / 2.0 - dx) / scale
        y = (cy - bh / 2.0 - dy) / scale
        boxes.append([x, y, bw / scale, bh / scale])
    if not boxes:
        return []
    indices = cv2.dnn.NMSBoxes(boxes, confidences.tolist(), CONFIDENCE_THRESHOLD, IOU_THRESHOLD)
    detections = []
    for i in np.array(indices).flatten():
        detections.append(
            {
                "class_id": int(class_ids[i]),
                "class_name": COCO_CLASSES[int(class_ids[i])],
                "confidence": round(float(confidences[i]), 4),
                "box": [round(float(v), 1) for v in boxes[i]],
            }
        )
    detections.sort(key=lambda d: d["confidence"], reverse=True)
    return detections


def resolve_camera_source(client: wendysensors.SensorClient) -> str:
    """Pick the camera source to subscribe to, reporting honestly when the
    harness has none to give."""
    if CAMERA_SOURCE:
        return CAMERA_SOURCE
    try:
        sources = client.sources()
    except Exception as exc:  # grpc.RpcError and connection failures alike
        log.error("cannot reach the sensor socket at %s (%s); is the sensors entitlement granted?", client.target, exc)
        sys.exit(1)
    cameras = [s for s in sources if s.kind == "camera" and s.subscribable and s.healthy]
    if not cameras:
        for source in sources:
            if source.kind == "camera":
                log.error("camera %s is not available to models: %s", source.id, source.detail)
        log.error("no subscribable camera source; set WENDY_CAMERA_SOURCE to choose one explicitly")
        sys.exit(1)
    if len(cameras) > 1:
        log.info("several cameras available; using %s (set WENDY_CAMERA_SOURCE to choose)", cameras[0].id)
    return cameras[0].id


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
    log.info("model %s v%s from %s", MODEL_NAME, MODEL_VERSION, MODEL_PATH)
    session = ort.InferenceSession(MODEL_PATH, providers=ort.get_available_providers())
    input_name = session.get_inputs()[0].name
    log.info("onnxruntime providers: %s", session.get_providers())

    client = wendydata.DataSocketClient()
    log.info("data socket: %s", client.path)
    actuator = Actuator()

    sensors = wendysensors.SensorClient("", model=MODEL_NAME)
    source_id = resolve_camera_source(sensors)
    sensors.source_id = source_id
    log.info("sensor socket: %s, source: %s", sensors.target, source_id)

    interval = 1.0 / max(PREDICTIONS_PER_SECOND, 0.1)
    next_score_at = 0.0
    person_present = False
    skipped = 0
    try:
        for sensor_frame in sensors.frames():
            if sensor_frame.dropped_before:
                # The harness produced frames this app never received. Say so
                # rather than leaving a silent gap in the sample identifiers.
                log.warning(
                    "the harness dropped %d sample(s) before %s#%s: the model is not keeping up",
                    sensor_frame.dropped_before,
                    sensor_frame.source_id,
                    sensor_frame.sample_ids[-1] if sensor_frame.sample_ids else "?",
                )
            now = time.monotonic()
            if now < next_score_at:
                # Frame skipping keeps predictions at or under the configured
                # rate. The skipped frames were still delivered, so the
                # episode's ledger holds them.
                skipped += 1
                continue
            next_score_at = now + interval
            frame = sensor_frame.image

            detections = detect(session, input_name, frame)
            uncertainty = wendydata.uncertainty_score(
                (d["confidence"] for d in detections), CONFIDENCE_THRESHOLD
            )

            record = wendydata.build_prediction(
                MODEL_NAME,
                MODEL_VERSION,
                uncertainty,
                detections[:TOP_DETECTIONS],
                {
                    "frame_width": frame.shape[1],
                    "frame_height": frame.shape[0],
                    "sensor_boottime_nanos": sensor_frame.boottime_nanos,
                    "frames_skipped_since_last_prediction": skipped,
                },
                inputs=sensor_frame.input_refs(),
            )
            skipped = 0
            client.send(record)

            # Edge-trigger the demo event so a person standing in front of
            # the camera fires the campaign once, not once per frame.
            has_person = any(d["class_id"] == PERSON_CLASS_ID for d in detections)
            if has_person and not person_present:
                best = max(
                    (d for d in detections if d["class_id"] == PERSON_CLASS_ID),
                    key=lambda d: d["confidence"],
                )
                client.send(wendydata.build_event("person_detected", {"confidence": best["confidence"]}))
                log.info("person detected (confidence %.2f)", best["confidence"])
            person_present = has_person

            angular_z, linear_x = steer_towards(detections, frame.shape[1])
            actuator.act(angular_z, linear_x)
    except KeyboardInterrupt:
        pass
    finally:
        sensors.close()
        client.close()
        actuator.close()


if __name__ == "__main__":
    main()
