# Detection through the ChatGPT gateway

The gateway supports persistent YOLO detectors, approved detection triggers,
and Wendy Data notification metadata without a local terminal. It does not
expose general campaign YAML deployment, episode download, or durable stream
export. Inspect available tools before choosing this path.

## Deploy a YOLO detector

Use the exact authorized `robot_id` from `list_robots`. For a requested YOLO
detector, call `deploy_yolo_detector` with `robot_id`, a detector `name`, and a
public Hugging Face `model_ref`. It supports YOLOv8/YOLO11 float32 ONNX raw
detection exports with static batch-1 input and embedded class names. It does
not run `.pt` checkpoints or repository Python code.

The gateway resolves a branch, tag, or revision to an immutable model commit
before deployment. `model_file` can be omitted only when that repository has
exactly one ONNX file. Use an existing user-selected model or resolve a suitable
repository; do not invent a model path or revision.

Use an exact healthy Wendy Data camera `source_id`. It can be omitted only when
one healthy camera exists. If the tool reports multiple choices, use its actual
source IDs to resolve the requested camera. `labels` defaults to `person`,
`threshold` to `0.5`, and `rate` to one frame per second. Set them to match the
user's requested detection.

Deployment activates the camera and stores campaign `chatgpt-yolo-<name>`.
Each detection records a one-second local episode with `upload.when: manual`.
Its requested 128 MiB campaign quota is not enforced; the device-wide episode
quota applies. It emits immediate Wendy Data detection notifications.

Keep the returned robot, campaign, source, and pinned model identifiers. Call
`inspect_yolo_detector` with the same `robot_id` and detector `name` to check
model loading, camera state, warnings, and notification errors. An armed plan
or `readiness: not_checked` does not prove that inference is running.

For a requested stop, call `stop_yolo_detector` and inspect until
`inference_status.state` is `disabled`. `stop_requested` does not prove that
the worker and camera subscription have finished stopping. Previously recorded
episodes remain on the device.

## Read or subscribe to notifications

`read_device_notifications` reads immediate campaign notification metadata.
Pass `robot_id` and, where relevant, exact `campaign` and `event` filters.
Keep its returned `cursor` between reads. An empty cursor starts at the current
tail unless `replay: true`; an empty initial result is not evidence that no past
notification exists. `gap: true` means retained history was lost.

For requested background delivery, use the host's available MCP event
subscription mechanism with event name `wendy.data.notification` and arguments
`robot_id`, plus optional exact `campaign` and `event` filters. YOLO deployment
returns the matching event name and arguments. The event is not a callable tool;
do not invent a `subscribe_*` tool if the host lacks subscription support.
Report whether a subscription was actually created or only a bounded read was
performed. Report its returned expiry: gateway subscriptions default to and
are capped at 24 hours, even when the detector continues running.

These notifications cover `notify.on: detection` and `notify.on: event`.
Cloud `episode_committed` notifications are separate. Reading or subscribing
does not deploy inference or start cameras. Payloads identify the notification,
campaign, source, model revision, count, and occurrence time; they do not contain
an image. A later camera snapshot is a separate observation, not a picture of
the detection instant.

## Existing approved triggers

Use `list_device_triggers` to inspect triggers already approved for the device.
`configure_device_trigger` enables or disables a managed trigger using its
returned `trigger_id`. External app triggers remain managed by their app.
Disabling inference does not delete retained episodes or disable every part of
the approved capture policy.

`list_device_events` and `wait_for_device_event` read that trigger's event
history. Preserve their cursors and report gaps. They are separate from the
immediate notification journal used by `read_device_notifications`. A bounded
wait cannot create future ChatGPT turns after the host disconnects.
