# `wendy data`

Wendy Data records synchronized, device-local Episodes and manages
flight-recorder campaigns. An Episode uses Linux `CLOCK_BOOTTIME` as its
canonical timeline, so wall-clock changes cannot reorder records. Its Wendy
manifest retains UTC correlation intervals, native clock mappings, source and
device identity, drop accounting, calibration revisions, lifecycle state, file
sizes, formats, and SHA-256 checksums.

## Episode commands

| Command | Description |
|---|---|
| `wendy data sources` | List camera, ROS 2, application, and telemetry sources. |
| `wendy data record` | Start one detached Episode. With no source flags, every healthy discovered source is selected. |
| `wendy data stop` | Stop and seal the active Episode. |
| `wendy data episodes` | List finalized Episodes. |
| `wendy data inspect <episode>` | Print the manifest and verify every sealed payload checksum. |
| `wendy data download <episode>` | Resume a per-file download, verify it, and publish the local directory atomically. |

Use repeatable `--source` flags to narrow recording or `--exclude-source` to
remove a default source. `record` also accepts `--name`, repeatable
`--calibration <source>=<local-path>`, and
`--require-utc-uncertainty <duration>`. The uncertainty option refuses to start
when fresh UTC evidence does not meet the bound; it never affects local
monotonic ordering.

```sh
wendy data sources
wendy data record --name commissioning --source v4l2:/dev/video2
wendy data stop
wendy data episodes
wendy data inspect <episode-id>
wendy data download <episode-id> --output ./commissioning-episode
```

Episode IDs are stable opaque identifiers. Their readable UTC prefix is only a
convenience; canonical ordering comes from the Episode's `CLOCK_BOOTTIME`
timestamps and boot ID.

## Campaign commands

| Command | Description |
|---|---|
| `wendy data campaign deploy <file.yaml>` | Strictly validate, persist, and arm a campaign on the connected device. |
| `wendy data campaign list` | List deployed campaigns and revisions. |
| `wendy data campaign inspect <name>` | Print the canonical deployed plan and state. |
| `wendy data campaign trigger <name>` | Trigger a campaign manually; `--reason` is stored in the Episode. |

Event and model-uncertainty triggers are armed immediately after deployment.
A complete plan is in `Examples/WendyDataCampaign` in the WendyOS repository.

## Campaign YAML reference

A campaign file contains exactly one YAML document. Unknown fields are
rejected.

| Top-level field | Required | Description |
|---|---|---|
| `name` | yes | Unique device-local name using letters, numbers, `.`, `-`, or `_`; maximum 128 characters. |
| `fleet` | no | Fleet selector retained with the plan. A direct device deployment applies to the connected device. |
| `sources` | yes | One or more source entries. |
| `capture` | yes | Buffer, post-trigger duration, and triggers. |
| `upload` | yes | Upload condition and destination lifecycle intent. |
| `export` | yes | Annotation integration lifecycle intent. |
| `models` | no | Map of model name to deployed version, copied into Episodes. |
| `privacy` | no | List of declared transforms with optional revisions. |

Each `sources` item selects exactly one source:

| Source | Description |
|---|---|
| `camera: <selector>` | A stable source ID, `/dev/videoN` path, or unambiguous name fragment. `front` and `default` select the only healthy camera when exactly one exists. |
| `ros2: <topic>` | Selects healthy local ROS graph recorders and retains the requested topic separately in the manifest. |
| `telemetry: true` | Includes device telemetry. |

An optional `calibration_revision` can accompany a source. Application records
are included automatically because they carry campaign trigger evidence and
pre-roll.

`capture.buffer` must be a Go-style duration from `0s` through `5m`.
`capture.after_trigger` must be greater than `0s` and no more than `24h`. At
least one trigger is required, and each trigger selects exactly one condition:

| Trigger | Description |
|---|---|
| `event: <name>` | Matches an entitled application's event name. |
| `model.uncertainty: "> 0.65"` | Compares prediction uncertainty with `<`, `<=`, `==`, `>=`, or `>` and a value in `[0, 1]`. |

Prediction uncertainty is read from the structured `uncertainty` attribute
when present, otherwise from the prediction value.

```yaml
name: forklift-failures
fleet: warehouse-west

sources:
  - camera: front
  - ros2: /lidar/points
  - ros2: /vehicle/odometry

capture:
  buffer: 10s
  after_trigger: 20s
  triggers:
    - event: emergency_stop
    - model.uncertainty: "> 0.65"

upload:
  when: wifi
  destination: s3://acme-ml/forklift

export:
  annotation: cvat
```

## Pre-trigger behavior

Application pre-roll is exact, device-wide, and bounded by the campaign buffer
(with a five-minute/50 MiB global ceiling). Camera and ROS 2 adapters currently
start at the trigger. Their manifest entries preserve both the requested offset
and the achieved offset; campaign deployment prints a warning when sensor
pre-roll was requested. Unknown drop counts are displayed as unknown, never as
zero.

## Current layer boundaries

- `upload.when` and `upload.destination` are stored as durable pending state,
  but this release does not run an S3 or cloud transfer worker.
- `export.annotation` is stored as labeling lifecycle state, but this release
  does not create CVAT tasks.
- Fleet catalog search, replay, evaluation, and model redeployment are later
  Wendy Data layers and are not performed by these commands yet.

Episodes remain locally inspectable and downloadable regardless of network or
UTC confidence.
