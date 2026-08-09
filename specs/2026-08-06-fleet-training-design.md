# Fleet Training: design

One command starts a training job on one or many WendyOS devices. The declaration
lives in `wendy.json` plus environment variables; the conveniences are layered so
an experienced Machine Learning (ML) engineer can peel back anything they do not
want, down to a bare container with a documented contract.

This formalizes what two efforts proved by hand: the go2-pointing runs on the
Sparks (single-device Reinforcement Learning (RL) with checkpoint resume) and the
G1 fleet draft (pull request #1423: mesh works across devices, Graphics
Processing Unit (GPU) passthrough works, Evolution Strategies (ES) and Proximal
Policy Optimization (PPO) both train across three Sparks).

## Problem

Training on Wendy devices today is artisanal. Every project reinvents the same
five things, and gets some of them wrong:

1. Checkpoint and resume. The G1 draft saves `theta.npy` but never loads it, and
   never saves optimizer state; a coordinator restart silently restarts the run
   from random weights while reporting healthy generation numbers.
2. Multi-device roles. `ROLE`, `MESH_SELF`, `MESH_PEERS` are computed by a human
   and typed per device. That is the opposite of one click.
3. Wire formats. Array payloads carry no architecture metadata, so receivers
   infer network shape from parameter counts (a quadratic solved against the
   flat vector length), which silently falls back on mismatch.
4. Honest status. A generation that times out with 20 of 60 returns applies an
   update from a different effective population while `mean_return` reports as
   if nothing changed. A status field that lies is worse than none.
5. The deployment boundary. Nothing owns the contract between a trained artifact
   and its consumer: shapes, observation layout, checksums. In go2-pointing every
   expensive failure was at this boundary, none were in the training loop.

## Non-goals

- No new algorithm framework. PyTorch, JAX, TensorFlow, or plain NumPy all work;
  the harness never imports any of them in its core.
- No scheduler or cluster manager. Devices are named explicitly in a fleet file.
- No hard dependency on any single device type. Sparks are the test hardware,
  nothing more. Any WendyOS device with the mesh entitlement qualifies; GPU is
  an optional entitlement, not an assumption.
- No claim that distribution is always right. The guidance documents when a
  single GPU device beats a Central Processing Unit (CPU) fleet (the G1 numbers:
  one CUDA-graph device does ~734k environment steps/s; three CPU Sparks
  evaluate 60 episodes per generation).

## Shape of the solution

A new top-level `Training/` directory in this repository:

```
Training/
  README.md            documentation entry point
  wendytrain/          core library (pip-installable, NumPy is the only dependency)
  templates/
    single/            one device, built-in NumPy environment, full run lifecycle
    sweep/             N independent runs across devices (the fan-out primitive)
    es-fleet/          coordinator plus workers, mirrored-sampling ES
    ppo-fleet/         learner plus actors, PPO (PyTorch, optional)
    byo/               bring-your-own: the bare contract, no library at all
  launch/              one-click fleet launcher
  tests/integration/   scripted hardware verification
```

### The layers (peel-back model)

- Layer 0, the contract: `wendy.json` entitlements (mesh, gpu, persist) plus a
  documented set of environment variables and paths. Use nothing else and you
  still get one-click multi-device deploys. The `byo` template is exactly this.
- Layer 1, the library: `wendytrain` gives config loading, durable runs with
  resume, peer and role resolution, a self-describing wire codec, artifact
  manifests, and a small HTTP service helper. Import only what you want; every
  module stands alone.
- Layer 2, algorithm math: `wendytrain.es`, `wendytrain.optim`, `wendytrain.rl`
  are pure functions (ES gradient with combined rank normalization, Adam,
  Generalized Advantage Estimation (GAE)). Optional imports.
- Layer 3, templates: complete runnable projects built on layers 0 to 2. Copy
  one, replace the environment and reward, keep the lifecycle.
- Layer 4, the launcher: `launch/fleet.py` reads a `fleet.toml`, computes roles
  and peers per device, and drives the `wendy` Command Line Interface (CLI).

Nothing above a layer is required by anything below it.

### Environment variable contract (layer 0)

Extends the conventions already established by `Examples/HelloMesh`:

| Variable | Meaning | Default |
|---|---|---|
| `MESH_PEERS` | comma list: bare asset id, `id:port`, or `host[:port]` | empty |
| `MESH_SELF` | this device's asset id | empty |
| `MESH_PORT` | default port for entries without one | 8080 |
| `WT_ROLE` | `coordinator`, `worker`, `learner`, `actor`, or `auto` | `auto` |
| `WT_RUN_ID` | stable run identifier, names the checkpoint directory | `default` |
| `WT_CKPT_DIR` | checkpoint root (a persist entitlement path) | `/data/checkpoints` |
| `WT_CONFIG` | path to a config file baked into the image | unset |

`WT_ROLE=auto` derives the role deterministically: the lowest asset id among
`MESH_PEERS` plus `MESH_SELF` is the coordinator (or learner); everyone else is
a worker (or actor). One rule, no human assignment, and any device can still be
pinned explicitly.

Config layering: library defaults, then the `WT_CONFIG` file (TOML via the
standard library; YAML accepted when PyYAML is present), then environment
variables `WT_<SECTION>__<KEY>`, then explicit code overrides. Later wins.

### Durable runs (the resume fix)

A `Run` owns `{WT_CKPT_DIR}/{WT_RUN_ID}/`. Checkpoints are written atomically
(temporary file, then rename), a `latest` pointer advances only after the write
lands, and old checkpoints are pruned. `Run.load_latest()` returns arrays, JSON
metadata, and the iteration number. The contract templates follow: optimizer
state is part of the checkpoint (Adam moments and step count included), so a
restart continues rather than restarts. Metrics append to `metrics.jsonl`; when
a fleet update proceeds with partial results, the record says so
(`n_contributed`, `population`), because a status field that lies is worse than
none.

### Wire format (the metadata fix)

`WTW1`: a four-byte magic, a length-prefixed JSON header naming every array
(name, dtype, shape, byte offset) plus an open `meta` object (architecture,
generation, framework), then the concatenated raw buffers, gzipped. Any language
can produce or parse it. Receivers never infer architecture from parameter
counts.

### Artifact manifests (the boundary fix)

A run's exportable output is a manifest: input and output shapes, observation
layout description, framework, file list with SHA-256 checksums. `verify` fails
on any mismatch. This is the piece that turns "training finished" into
"deployable artifact", and it is the single thing that would have saved the most
time on go2-pointing.

### Topology guidance (documented, never enforced)

| Situation | Recommended template |
|---|---|
| One device, or a batched-simulation GPU | `single` |
| Hyperparameter, seed, or reward-shaping search | `sweep` (usually the best first use of extra devices) |
| Population methods, scalar returns over the wire | `es-fleet` |
| Off-policy actor pools, trajectories over the wire | `ppo-fleet` |
| torch.distributed, JAX collectives, anything else | `byo` |

The launcher accepts any template against any device list. Suggestions live in
documentation; nothing is locked.

## Verification plan

Unit and static tests run locally. Hardware verification runs on the three
currently discoverable Sparks, and must not stop or remove any existing
application on them:

| Device | Asset id |
|---|---|
| spark-3011.local | 334 |
| spark-48fd.local | 211 |
| spark-edeb.local | 283 |

1. Resume proof (single device): start `single`, kill the container mid-run,
   restart, assert the iteration counter continues and the Adam step count
   survives.
2. Fleet proof (three devices): `es-fleet` for a bounded number of generations,
   assert every device contributed returns and the checkpoint advanced.
3. Fan-out proof (three devices): `sweep` with three seeds, assert three
   distinct result artifacts collect back.
4. Non-interference: `container list` on each device before and after; the diff
   must contain only our applications.

## Relationship to pull request #1423

The draft stays what it is, a G1-specific example with real hardware evidence.
This design generalizes its substrate findings and fixes the defects found in
review (resume that never loads, per-set rank normalization, architecture
inference, silent partial generations, four rsync copies of the library). The G1
examples can later be rebased onto `wendytrain` if their author wants, but
nothing here depends on that.

## Addendum: the launcher became a Command Line Interface subcommand

Written 2026-08-07, after the first version of this feature shipped its layer 4
as `Training/launch/fleet.py`.

The launcher proved the mechanics that matter: deterministic roles, staged build
contexts, the two transports, a shared token, and a plan you can audit before
anything runs. What it got wrong was where those mechanics live. It recomputed
device targeting that the Command Line Interface already owns, since
`wendy fleet run --group` resolves a group over cloud asset tags or a local
network name pattern and fans a deploy out across it, and it delivered
per-device environment by riding this machine's process environment through each
template's `${VAR}` passthrough.

`wendy fleet train` reuses that group resolution and the per-device deploy
unchanged, and adds only what training genuinely needs: ranks and roles derived
from asset identifiers, per-device identity injected through the create-time
environment channel the fleet manifest path already uses, a generated bearer
token, and per-device sweep parameters. Layers 0 through 3 are untouched, and
the layer 0 environment contract is identical, which is why no template changed.

Three platform defects surfaced on the way and were fixed rather than worked
around: the multi-service deploy path dropped injected environment entirely, so
no service container could have received a per-device identity; fleet targets
discarded the asset identifier that discovery had already parsed; and the peer
address shipped to devices was the agent dial address rather than a host another
device can reach.

Two things did not change. The local-network transport still exists because the
mesh overlay remains unreliable on this fleet, and it is still the fallback
rather than the default. The verification record in
`Training/tests/integration/checklist.md` keeps both runs: the original through
the launcher, and the re-run through the subcommand.
