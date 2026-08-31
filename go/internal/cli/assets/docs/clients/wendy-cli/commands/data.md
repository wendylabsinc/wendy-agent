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
| `wendy data sources` | List camera, audio, ROS 2, application, and telemetry sources. |
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

`sources` prints an aligned table whose `DETAIL` column carries the device's own
description of each source, truncated to keep the table readable. A single kind
can dominate a board: an NVIDIA Jetson exposes 20 internal audio-DMA routing
channels alongside its real microphone, so when more than six sources share a
kind the first six are listed and the remainder are counted in a note. Nothing
is hidden from `--json`.

```text
SOURCE            KIND         CLOCK                       STATUS   DETAIL
applications      application  CLOCK_BOOTTIME              healthy
audio:16777217    audio        ALSA_CAPTURE/AGENT_RECEIPT  healthy  C920 [HD Pro Webcam C920], device 0: USB Audi...
audio:16777729    audio        ALSA_CAPTURE/AGENT_RECEIPT  healthy  APE [NVIDIA Jetson Orin Nano APE], device 0: ...
audio:16777730    audio        ALSA_CAPTURE/AGENT_RECEIPT  healthy  APE [NVIDIA Jetson Orin Nano APE], device 1: ...
audio:16777731    audio        ALSA_CAPTURE/AGENT_RECEIPT  healthy  APE [NVIDIA Jetson Orin Nano APE], device 2: ...
audio:16777732    audio        ALSA_CAPTURE/AGENT_RECEIPT  healthy  APE [NVIDIA Jetson Orin Nano APE], device 3: ...
audio:16777733    audio        ALSA_CAPTURE/AGENT_RECEIPT  healthy  APE [NVIDIA Jetson Orin Nano APE], device 4: ...
telemetry         telemetry    CLOCK_BOOTTIME              healthy
v4l2:/dev/video0  camera       V4L2_BUFFER_TIMESTAMP       healthy  HD Pro Webcam C920: HD Pro Webc VIDEO_TRANSPO...

... 15 more audio sources not listed (--kind audio to list all, --json for everything)
```

Narrow the table with `--kind`, which is repeatable or comma-separated and
matches case-insensitively. A kind named there is never summarised, because
asking for a kind is asking to see all of it. Without `--kind`, `--json` stays
the full unfiltered response the device sent, so existing scripts keep working;
with `--kind` it carries the filtered set.

```sh
wendy data sources --kind camera
wendy data sources --kind camera,telemetry
wendy data sources --kind audio            # every audio source, nothing summarised
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
| `ros2: <topic>` | A ROS 2 topic name such as `/lidar/points`. Selects that topic on every healthy ROS 2 graph publishing it, and the episode records that topic and nothing else. A topic no healthy graph publishes is an error. A full per-topic source ID such as `ros2:rmw_cyclonedds_cpp:domain-42:/lidar/points` selects that one source. A domain-level source ID such as `ros2:rmw_cyclonedds_cpp:domain-42`, or any other value, selects the whole DDS domain and records all of it. Requested topics are retained separately in the manifest either way. |
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

What each adapter honors today: cameras implement `continuous` and `snapshot`,
audio implements `continuous` and `threshold`, and the ROS 2 and telemetry
paths implement `continuous` only. Every other combination validates and
deploys, and deployment prints a warning naming each source whose requested
mode is not implemented for its kind; those sources record continuously for
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

## Playing back camera capture

Camera capture is kept as a raw H.264 Annex-B elementary stream
(`cameras/<source>/segment-NNNNNN.h264`). An elementary stream has no
container, so it carries no timing at all, and a player handed one has nothing
to build a presentation schedule from. Players invent a frame rate instead, so
the clip runs at the wrong speed and reports the wrong duration. Passing a
fixed rate with `ffmpeg -r` does not fix this: the capture rate is genuinely
variable, falling as the device gets busy with inference, so no single number,
including the measured average, describes more than a fraction of the clip.

The timing is already recorded. `cameras/<source>/index.jsonl` carries one line
per kept frame with `canonical_episode_nanos`, the frame's position on the
Episode's canonical `CLOCK_BOOTTIME` timeline, alongside the segment file, byte
offset and `byte_size` of its payload. The `episode-playable` command remuxes
those payload bytes into an MP4 that gives every frame the presentation time
the index recorded for it, so the variable frame rate is represented honestly
rather than averaged away:

```sh
# From the repository root.
CGO_ENABLED=0 go build -o bin/episode-playable ./go/cmd/episode-playable

wendy data download <episode-id> -o /absolute/path/to/episode --device <device-hostname>
./bin/episode-playable -o /absolute/path/to/playable /absolute/path/to/episode
```

Note that `wendy data download` needs an absolute `-o` path; a relative one
fails with "server file path escapes destination".

One `<source>.mp4` is written per camera source into the output directory,
which must be somewhere other than the Episode. The command prints the index
line count, the frames written, the segments read, the index's first-to-last
span, and the minimum, mean and maximum inter-frame interval, which is enough
to check the conversion numerically without watching it.

Details worth knowing:

- The Episode is only ever read. The elementary stream and its index are
  checksummed archival truth, and `index.jsonl` addresses frames by byte offset
  within the stream, so rewriting it in place would break the join between a
  frame and the model input recorded against it.
- It is a remux, not a transcode. The coded pictures are copied verbatim, so
  the output is the same video, only re-framed.
- MP4 was chosen over Matroska because the output has to play unmodified in
  both VLC and QuickTime, and QuickTime cannot open Matroska. MP4 states an
  explicit duration for every sample in its `stts` box, which represents a
  variable frame rate exactly as precisely as Matroska timestamps do.
- There is no external dependency. The muxer is pure Go, so no ffmpeg,
  mkvmerge or PyAV is needed anywhere on the path from an Episode to a playable
  file.
- A frame the index names but whose bytes are missing or truncated is reported
  and skipped rather than written short, because a truncated access unit makes
  the file undecodable from that point on. A partial trailing index line, the
  shape an interrupted Episode leaves behind, is counted as unusable and
  ignored. Multiple segments per source and multiple sources per Episode are
  both handled.
- `byte_size` from the index is authoritative for how many bytes belong to a
  frame. The model-input ledger's `payload_bytes` can exceed it for a frame
  that opens a segment, because the segment begins at the parameter-set prefix
  inside that payload rather than at its first byte.
- The index records frames in capture order, which is coded order. That is
  exact for the B-frame-free streams the device's encoder produces. A stream
  containing B slices has a presentation order that differs from its coded
  order, which the index does not carry, so such a stream is flagged with a
  warning rather than silently mistimed.

`wendy data export` is the natural future home for this, at which point the
command becomes a verb on the CLI rather than a separate binary.

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

The device-wide quota is the smaller of a fifth of the episode store's
filesystem and `WENDY_DATA_MAX_BYTES` (default 50 GiB), and eviction preserves
`WENDY_DATA_RESERVE_BYTES` of free space on it (default 5 GiB). Both are read
by the agent from its environment file.

## Current layer boundaries

- Upload policy: `upload.when` and `upload.max_rate` are applied by the agent's
  episode transfer worker, which uploads sealed episodes to the cloud ingest
  service. `wifi` is currently treated as `always` because no device
  network-type signal is plumbed to the worker yet. `upload.destination` is a
  logical dataset name stored with the plan; the device never receives bucket
  layouts or credentials.
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
