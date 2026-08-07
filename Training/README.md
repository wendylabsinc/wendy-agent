# Training on WendyOS devices

One command starts a training job on one or many WendyOS devices. The
declaration lives in `wendy.json` plus environment variables; every convenience
on top is layered so an experienced Machine Learning (ML) engineer can peel
back anything unwanted, down to a bare container with a documented contract.
The pieces are `wendytrain` (a core library whose only dependency is NumPy),
five ready-to-run templates, and a fleet launcher that computes each device's
role and drives the `wendy` Command Line Interface (CLI).

| Directory | What it is |
|---|---|
| `wendytrain/` | The core library: durable runs with resume, layered configuration, mesh roles, a self-describing wire codec, artifact manifests, a threaded HyperText Transfer Protocol (HTTP) helper, and Evolution Strategies (ES), Adam, and Generalized Advantage Estimation (GAE) math. Pip-installable, Python 3.11 or newer, NumPy is the only dependency. |
| `templates/single/` | One device, built-in NumPy cart-pole, the full run lifecycle. |
| `templates/sweep/` | N independent runs across devices, results collected into one table. |
| `templates/es-fleet/` | Coordinator plus workers, mirrored-sampling ES. |
| `templates/ppo-fleet/` | Learner plus actors, Proximal Policy Optimization (PPO); the one template that uses PyTorch. |
| `templates/byo/` | Bring your own: the bare layer-0 contract, no library required. |
| `launch/` | `fleet.py`, the one-command launcher, and `fleet.toml.example`. |

Each template's `README.md` documents its endpoints, configuration keys, and
tests; this file documents what they share.

## Quickstart: one device

The launcher needs the library importable (`python3 -m pip install -e
Training/wendytrain` pulls NumPy). Then, from the repository root:

```sh
cd Training/launch
cp fleet.toml.example fleet.toml   # set template = "single", devices = ["<your-device>.local"]
python3 fleet.py up
```

`up` stages a self-contained build context, then runs
`wendy --device <host> run --detach -y --prefix <staged-dir>` for the device.
Follow the run with `python3 fleet.py logs`. Checkpoints land on the device
under the persist volume at `/data/checkpoints`, so a container restart resumes
the run instead of restarting it.

## Quickstart: a fleet

Point `fleet.toml` at a multi-device template and list the devices. Asset
identifiers are resolved once through `wendy cloud device list --json` and
cached in `.fleet-state.json`; they can also be pinned per device:

```toml
[fleet]
template = "es-fleet"
devices = [
  { host = "spark-3011.local", asset_id = 334 },
  { host = "spark-48fd.local", asset_id = 211 },
  { host = "spark-edeb.local", asset_id = 283 },
]

[env]
WT_RUN_ID = "demo-1"
ES_POP = "64"
```

Audit first. `render` prints exactly what `up` would do, including the staged
context and the full per-device environment, and executes nothing. Output of
`python3 fleet.py render` against the file above, exactly as printed apart
from long absolute paths shortened to `...`:

```
template:  es-fleet (.../Training/templates/es-fleet)
app id:    sh.wendy.training.es-fleet
transport: mesh
staged:    .../T/wendy-fleet-es-fleet-qx6ojals (stage-manifest.json written)

device spark-3011.local (asset 334, role worker)
  env ES_POP=64
  env MESH_PEERS=334,211,283
  env MESH_SELF=334
  env WT_ROLE=worker
  env WT_RUN_ID=demo-1
  command: wendy --device spark-3011.local run --detach -y --prefix .../T/wendy-fleet-es-fleet-qx6ojals

device spark-48fd.local (asset 211, role coordinator)
  env ES_POP=64
  env MESH_PEERS=334,211,283
  env MESH_SELF=211
  env WT_ROLE=coordinator
  env WT_RUN_ID=demo-1
  command: wendy --device spark-48fd.local run --detach -y --prefix .../T/wendy-fleet-es-fleet-qx6ojals

device spark-edeb.local (asset 283, role worker)
  env ES_POP=64
  env MESH_PEERS=334,211,283
  env MESH_SELF=283
  env WT_ROLE=worker
  env WT_RUN_ID=demo-1
  command: wendy --device spark-edeb.local run --detach -y --prefix .../T/wendy-fleet-es-fleet-qx6ojals

nothing was executed; `up` runs the commands above in order
```

The lowest asset identifier became the coordinator with no human assignment.
When the plan reads right:

```sh
python3 fleet.py up        # deploy to every device, in order
python3 fleet.py status    # poll each device's /status or /healthz
python3 fleet.py logs      # stream one device's logs (default: the coordinator)
python3 fleet.py down      # stop this template's app id on every device, nothing else
```

`down` matches containers by the template's `appId` from `wendy.json` exactly
(plus its `appId_service` variants) and never touches anything else running on
the device.

### Staging, or why the launcher builds a temporary directory

The repository keeps exactly one copy of `wendytrain`, and the CLI rejects
build contexts that reach into parent directories. So `up` and `render` stage a
flat build context in a temporary directory: the template's files, the
pip-installable library tree at `wendytrain/`, `cartpole.py` from
`templates/single/` when the template references it (and the single trainer as
`single_train.py` for the sweep template), plus a `stage-manifest.json`
recording the Secure Hash Algorithm 256 (SHA-256) checksum of every staged
file. Template Dockerfiles copy
only from that context; vendoring is a launch-time build step verified by
checksums, never a committed copy.

### Transports: mesh and lan

`fleet.toml` accepts `transport = "mesh"` (the default) or `"lan"`. Mesh sends
`MESH_PEERS` as asset identifiers resolved over the mesh overlay, which is the
intended path. The `lan` (Local Area Network) transport exists because the
mesh overlay was found unreliable on a fleet with mixed agent versions during
development; it is the audited fallback, not the recommendation. With
`transport = "lan"` the launcher rewrites the staged `wendy.json`'s network
entitlement to `{"type": "network", "mode": "host"}` and renders `MESH_PEERS`
as `hostname:port` entries excluding the device itself (hostname entries
cannot be self-skipped by asset identifier). `render` shows the rewrite before
anything runs; an excerpt from the same fleet file with `transport = "lan"`,
paths shortened as above:

```
transport: lan
staged:    .../T/wendy-fleet-es-fleet-2minjign (stage-manifest.json written)
network entitlement rewritten to: {"type": "network", "mode": "host"}

device spark-3011.local (asset 334, role worker)
  env ES_POP=64
  env MESH_PEERS=spark-48fd.local:8080,spark-edeb.local:8080
  env MESH_SELF=334
```

### Trust boundary

The fleet endpoints move model parameters and accept contributions that steer
the update, so they are never left open on a network by default: `fleet.py up`
generates a `WT_FLEET_TOKEN` per fleet (cached next to `fleet.toml`, masked in
`render` output), every template's endpoints reject requests without the
bearer header, and every client attaches it. Set your own token in `[env]` to
override, or unset it only for same-machine experiments. The token
authenticates peers; it does not encrypt traffic. Treat the device network as
the trust boundary and use the mesh transport once available, since host mode
(`lan`) removes the container's network namespace isolation: the container
shares every host interface, which is precisely why it is a fallback rather
than the default, and why `render` prints the rewritten entitlement before
anything runs.

## The layers

Nothing above a layer is required by anything below it. What remains when each
layer is peeled off:

| Peel off | What you lose | What you keep |
|---|---|---|
| Layer 4, the launcher (`launch/fleet.py`) | Computed roles, peer lists, staging, one-command deploys | Everything else; export `MESH_*` and `WT_*` yourself and run `wendy run` per device |
| Layer 3, the templates | A working training loop to copy | The library and the contract; write your own container against `wendytrain` |
| Layer 2, the algorithm math (`wendytrain.es`, `.optim`, `.rl`) | ES gradient, Adam, GAE | Runs, configuration, mesh roles, wire codec, manifests, the HTTP helper; bring your own math |
| Layer 1, the library (`wendytrain`) | Everything above | The layer-0 contract alone: entitlements plus environment variables, readable from any language (this is exactly the `byo` template) |
| Layer 0, the contract | Nothing left to peel | `wendy.json` entitlements (mesh, persist, optional gpu) and the environment variable table below are the substrate itself |

## The environment variable contract (layer 0)

| Variable | Meaning | Default |
|---|---|---|
| `MESH_PEERS` | comma list: bare asset identifier, `id:port`, or `host[:port]` | empty |
| `MESH_SELF` | this device's asset identifier | empty |
| `MESH_PORT` | default port for entries without one | 8080 |
| `WT_ROLE` | `coordinator`, `worker`, `learner`, `actor`, or `auto` | `auto` |
| `WT_RUN_ID` | stable run identifier, names the checkpoint directory | `default` |
| `WT_CKPT_DIR` | checkpoint root (a persist entitlement path) | `/data/checkpoints` |
| `WT_CONFIG` | path to a configuration file baked into the image | unset |
| `WT_COORDINATOR` | the coordinating node as `host:port`, emitted by the launcher | unset |
| `WT_NODE_INDEX` | this node's rank by ascending asset identifier, coordinator first | unset |
| `WT_NODE_COUNT` | number of nodes in the fleet | unset |
| `WT_FLEET_TOKEN` | shared bearer token; when set, every fleet endpoint requires `Authorization: Bearer <token>` | unset |

A bare asset identifier in `MESH_PEERS` expands to
`device-<id>.cloud.wendy.dev:<MESH_PORT>`, matching the conventions of
`Examples/HelloMesh`. `WT_ROLE=auto` derives the role deterministically: the
lowest numeric asset identifier among `MESH_PEERS` plus `MESH_SELF` is the
coordinator (the `ppo-fleet` template maps coordinator to learner and worker
to actor); every other node is a worker. The rule never guesses: when
`MESH_SELF` or any peer entry is not a numeric asset identifier, role
derivation raises and asks for `WT_ROLE` to be set explicitly. For fleets
addressed by hostname (the lan transport) the launcher always emits the
generic topology trio `WT_COORDINATOR`, `WT_NODE_INDEX`, and `WT_NODE_COUNT`,
which the multi-device templates honor after their own explicit variables:
`es-fleet` prefers `ES_COORDINATOR` (`host:port`), `ES_WORKER_INDEX`, and
`ES_WORKER_COUNT`, and `ppo-fleet` prefers `PPO_LEARNER`; all documented in
the template `README.md` files. Numeric derivation from `MESH_PEERS` remains
the last resort and never guesses.

`wendy.json` has no wildcard environment passthrough, so each template
enumerates the `${VAR}` list it forwards (open a template's `wendy.json` for
the exact set). An unset variable arrives in the container as an empty string;
consumers of the contract must treat empty as unset before parsing, the way
`es-fleet`'s entry point drops empty values so every default applies as
documented. Anything a template does not enumerate can still reach the
container at deploy time with `wendy run --env KEY=VALUE`.

Configuration is layered, later wins: defaults in code, then the file named by
`WT_CONFIG` (Tom's Obvious Minimal Language (TOML) via the standard library;
YAML Ain't Markup Language (YAML) when PyYAML is installed), then environment
variables of the form `WT_<SECTION>__<KEY>`, for example `WT_ES__POP=64`.
Templates that ship a `config.toml` bake it into the image at
`/app/config.toml` (the `single` image points `WT_CONFIG` at it in its
Dockerfile, `es-fleet` falls back to it in code, and `ppo-fleet` reads it when
`WT_CONFIG` names it; each template's defaults in code mirror its file either
way). A deploy-time `WT_CONFIG` replaces the file wholesale.

## Topology guidance (documented, never enforced)

| Situation | Recommended template |
|---|---|
| One device, or a batched-simulation Graphics Processing Unit (GPU) | `single` |
| Hyperparameter, seed, or reward-shaping search | `sweep` |
| Population methods, scalar returns over the wire | `es-fleet` |
| Off-policy actor pools, trajectories over the wire | `ppo-fleet` |
| torch.distributed, JAX collectives, anything else | `byo` |

Distribution is not always right, and the design numbers are blunt about it:
one device running batched simulation under Compute Unified Device
Architecture (CUDA) graphs reached roughly 734,000 environment steps per
second, while three Central Processing Unit (CPU) devices in the fleet
evaluated 60 episodes per generation. If the workload fits one accelerator,
`single` on that device wins; a fleet of modest devices does not outrun it.
The usually best first use of extra devices is a parameter sweep, because N
independent runs of something that already works need no coordination and
waste nothing on communication. The launcher accepts any template against any
device list; the table is guidance, nothing is locked.

## Framework notes

PyTorch plugs in at layer 0 always: the `byo` template's `README.md` shows a
`torch.distributed` TCPStore rendezvous built on nothing but the derived role
and peer list. At layer 1 a checkpoint is named arrays plus opaque bytes, and
the `ppo-fleet` template is the working example: it serializes the torch
optimizer state dictionary with `torch.save` into a `uint8` wire array and
restores it on resume, step counts intact. The math modules (`wendytrain.es`,
`wendytrain.optim`, `wendytrain.rl`) are NumPy and framework-neutral; convert
tensors at the boundary.

JAX plugs in at layer 0 the same way: read the contract from the environment
and run any collective scheme over the mesh, with the coordinator as the
rendezvous point. Layer 1 fits naturally because JAX arrays convert to NumPy
losslessly, and an optimizer state pytree flattens into named wire arrays (or
one opaque bytes blob if you prefer); `Run`, `wire`, and `write_manifest`
neither know nor care which framework produced the arrays.

TensorFlow: layer 0 always works, since the contract is environment variables
and HTTP. For layer 1, variables checkpoint as named arrays via `.numpy()`,
and anything that resists a clean array form (optimizer slots, saved-model
blobs) travels the same way torch state does, as serialized bytes in a
`uint8` array. GAE and the ES gradient stay usable unchanged because they are
pure NumPy functions.

## Checkpoint durability contract

A `Run` owns `{WT_CKPT_DIR}/{WT_RUN_ID}/`. Checkpoints are wire-format blobs
named `step_{iteration:012d}.wtw`, written to a temporary sibling and moved
into place with an atomic rename; a `latest` text file containing the current
checkpoint's filename advances only after the write lands; files beyond
`keep_last` (default 5) are pruned oldest-first, never pruning the `latest`
target. Loading follows the pointer and falls back to the newest parseable
checkpoint when the pointer or its target is corrupt, so a torn write costs at
most one checkpoint, never the run.

Optimizer state is part of every checkpoint by contract: `single` and
`es-fleet` store the Adam moments and step count (`adam_m`, `adam_v`,
`adam_t`), and `ppo-fleet` stores the complete torch optimizer state
dictionary. A restart therefore continues the run; it never restarts it. Each
template logs one line when it resumes, and these are the exact strings:

```
[single] resumed iteration=<n> adam_t=<t>
resumed generation=<n> adam_t=<t>
[ppo-fleet] resumed version=<n> adam_steps=<n>
```

(the first from the `single` trainer, the second from the `es-fleet`
coordinator, the third from the `ppo-fleet` learner).

Metrics append to `metrics.jsonl` in the run directory, one JavaScript Object
Notation (JSON) object per line with `time` and `iteration` injected. When a
fleet update proceeds from partial results, the same line records
`n_contributed` and `population`, because a status field that lies is worse
than none. On completion a run exports its artifact plus a `manifest.json`
listing input and output shapes, the observation layout, the framework, and
per-file SHA-256 checksums; `wendytrain.manifest.verify_manifest` fails on the
first mismatch, which turns "training finished" into "deployable artifact".

## Wire format specification (WTW1)

`wendytrain.wire` encodes named arrays plus open metadata into a blob any
language can produce or parse. Receivers never infer architecture from
parameter counts; every array carries its own dtype and shape, and meaning
(such as `architecture`) travels in the metadata. Checkpoint files and all
template HTTP payloads are this format. Byte layout:

| Offset | Size | Content |
|---|---|---|
| 0 | 4 | magic: the American Standard Code for Information Interchange (ASCII) bytes `WTW1` |
| 4 | 4 | header length `L`, unsigned 32-bit little-endian integer |
| 8 | `L` | header, UTF-8 JSON |
| 8 + `L` | to end | payload: one gzip stream (Request for Comments (RFC) 1952) |

The header is one JSON object:

```json
{
  "meta": {"generation": 12, "architecture": [32]},
  "arrays": [
    {"name": "theta", "dtype": "<f4", "shape": [193], "offset": 0, "nbytes": 772},
    {"name": "adam_t", "dtype": "<i8", "shape": [], "offset": 772, "nbytes": 8}
  ]
}
```

`meta` is an open JSON object and is present even when empty. `arrays` entries
appear in payload order. `dtype` is a NumPy dtype string: a byte-order
character (`<` little-endian, `>` big-endian, `|` not applicable for
single-byte types), a kind character (`f` float, `i` signed integer, `u`
unsigned integer, `b` boolean), and the item size in bytes; in practice the
templates use `<f4` (float32), `<f8` (float64), `<i8` (int64), and `|u1`
(uint8). `shape` is the row-major (C-order) shape, where `[]` means a
zero-dimensional scalar. `offset` indexes into the decompressed payload, and
`nbytes` must equal the item size times the product of `shape` (the empty
product is 1, so a scalar's `nbytes` is one item; an empty array's is 0).

The decompressed payload is the concatenation of each array's raw bytes in
header order, row-major, with no padding or alignment. Opaque bytes (for
example, serialized torch state) travel as `|u1` arrays. A decoder must
reject: a wrong magic, a header extending past the blob, a header that is not
valid JSON, an invalid gzip stream, an entry whose `offset` plus `nbytes`
exceeds the payload, a `shape` and `dtype` that contradict `nbytes`, and an
unknown dtype string. The reference implementation is
`Training/wendytrain/wendytrain/wire.py`, and `tests/test_wire.py` pins the
behavior.

## Hardware notes

Any WendyOS device qualifies; nothing in the library or launcher is specific
to a device type. The Sparks named in the examples are the test hardware,
nothing more. The templates declare their needs as `wendy.json` entitlements:

| Entitlement | Purpose | Used by |
|---|---|---|
| `persist` | named volume mounted at `/data/checkpoints`, the default `WT_CKPT_DIR`; checkpoints survive restarts and redeploys | all five templates |
| `network` (mode `mesh`) | peers reach each other over HTTP on port 8080, serviceCIDR `10.99.0.0/16` | `sweep`, `es-fleet`, `ppo-fleet`, `byo` |
| `gpu` | optional acceleration | no template requires it; add `{"type": "gpu"}` to a service's entitlements where wanted |

The `single` template declares no network entitlement at all, which also means
the `lan` transport (which works by rewriting a network entitlement) applies
to the mesh templates, not to `single`.

## Running the tests

Module names collide across templates (each has a `train.py` and a `tests/`
directory), so the suites run per directory rather than as one collection.
With NumPy, pytest, torch, and the library installed
(`python3 -m pip install numpy pytest torch -e Training/wendytrain`), from the
repository root:

```sh
python3 -m pytest Training/wendytrain/tests -q          # 89 passed
python3 -m pytest Training/templates/single/tests -q    # 16 passed
python3 -m pytest Training/templates/sweep/tests -q     # 17 passed
python3 -m pytest Training/templates/es-fleet/tests -q  # 13 passed
python3 -m pytest Training/templates/ppo-fleet/tests -q # 6 passed
python3 -m pytest Training/launch/tests -q              # 23 passed
```

The counts are from the suites as of this writing. The `ppo-fleet` suite
skips itself with an explanatory message when torch is not installed; every
other suite needs only NumPy. No test touches a device, Docker, or the
network beyond the loopback interface.
