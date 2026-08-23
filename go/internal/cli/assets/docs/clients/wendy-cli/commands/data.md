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
| `version` | yes | Campaign schema version the author wrote the file against. This release supports version `1`; higher versions are a deploy-time error. |
| `name` | yes | Unique device-local name using letters, numbers, `.`, `-`, or `_`; maximum 128 characters. |
| `fleet` | no | Fleet selector retained with the plan. A direct device deployment applies to the connected device. |
| `sources` | yes | One or more source entries. |
| `capture` | yes | Buffer, post-trigger duration, and triggers. |
| `upload` | yes | Upload condition, optional logical destination, and optional rate cap. |
| `retention` | no | Optional on-device storage bounds. |
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

### Per-source capture policy

Each source may carry an optional `capture` block declaring how it records.
Fields belonging to a different mode than the declared one are rejected.

| Field | Mode | Description |
|---|---|---|
| `mode` | | One of `continuous` (default), `snapshot`, `fragment`, or `threshold`. |
| `rate` | `continuous` | Optional capture frequency cap in hertz. |
| `interval` | `snapshot` | Required snapshot period, for example `2s`. |
| `pre`, `post` | `fragment` | Durations bounding a fragment around an occurrence; at least one is required. |
| `trigger` | `threshold` | Required expression `<field> <op> <number>`, for example `model.uncertainty > 0.9` or `level_db > -20`. Only `model.uncertainty` is bounded to 0 through 1; other fields carry their own units. |
| `fragment` | `threshold` | Optional captured duration per threshold crossing. |
| `max_resolution` | any | Camera sources only: `WxH` cap such as `1280x720`. |

Only `continuous` is implemented by the capture adapters today. Other modes
validate and deploy, and deployment prints a warning naming each source whose
requested mode is not implemented yet; those sources record continuously for
now.

`capture.buffer` must be a Go-style duration from `0s` through `5m`.
`capture.after_trigger` must be greater than `0s` and no more than `24h`. At
least one trigger is required, and each trigger selects exactly one condition:

| Trigger | Description |
|---|---|
| `event: <name>` | Matches an entitled application's event name. |
| `model.uncertainty: "> 0.65"` | Compares prediction uncertainty with `<`, `<=`, `==`, `>=`, or `>` and a value in `[0, 1]`. |

Prediction uncertainty is read from the structured `uncertainty` attribute
when present, otherwise from the prediction value.

The `upload` and `retention` blocks:

| Field | Required | Description |
|---|---|---|
| `upload.when` | yes | One of `always`, `wifi`, or `manual`. |
| `upload.destination` | no | Logical dataset name the fleet backend maps to storage. Not a URL; devices never receive bucket layouts or credentials through campaign plans. |
| `upload.max_rate` | no | Upload bandwidth cap in bytes per second; plain integers and rates such as `5MB/s` are accepted. |
| `retention.local_quota` | no | Declared on-device episode storage bound in bytes; plain integers and sizes such as `10GiB` are accepted. Stored with the plan; this release enforces only the device-wide quota and deployment prints a warning. |

```yaml
version: 1
name: forklift-failures
fleet: warehouse-west

sources:
  - camera: front
    capture:
      mode: snapshot
      interval: 2s
      max_resolution: 1280x720
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
  destination: forklift-episodes
  max_rate: 5MB/s

retention:
  local_quota: 10GiB

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

## Concurrency and retention

Each campaign records at most one active Episode at a time; a second trigger
for the same campaign while its Episode is active is dropped. Different
campaigns capture concurrently, and one ad-hoc `wendy data record` Episode can
run beside them. `wendy data stop` finalizes the ad-hoc Episode when one is
running, otherwise the single active campaign Episode.

When the device storage quota is exceeded, the oldest Episodes that are
already uploaded or were recorded without an upload policy are evicted first.
Episodes still awaiting upload are evicted only as a last resort, with a
warning in the agent log naming the Episode and campaign.

## Current layer boundaries

- Upload policy (`upload.when`, `upload.destination`, `upload.max_rate`) is
  stored as durable pending state, but this release does not run a cloud
  transfer worker.
- Per-source capture modes other than `continuous` are validated and stored,
  but the adapters record continuously; deployment warns per source.
- `retention.local_quota` is validated and stored, but eviction currently
  applies only the device-wide quota; deployment warns when it is set.
- `export.annotation` is stored as labeling lifecycle state, but this release
  does not create CVAT tasks.
- Fleet catalog search, replay, evaluation, and model redeployment are later
  Wendy Data layers and are not performed by these commands yet.

Episodes remain locally inspectable and downloadable regardless of network or
UTC confidence.
