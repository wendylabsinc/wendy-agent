# sweep: N independent runs across devices

Runs the single template's trainer once per device, each member with its own
parameter set, and collects the results into one sorted table. This is the
fan-out primitive, and usually the best first use of extra devices: no
gradient traffic, no coordination, just N independent runs of something that
already works on one device.

## Files

| File | Purpose |
|---|---|
| `train.py` | The member. Merges its parameter set into the configuration, reuses `train_loop` from the single template, serves `GET /result`. |
| `collect.py` | The collector. Polls every member's `/result`, writes `results.json` sorted by score. |
| `Dockerfile` | Builds from a staged context (see below). |
| `wendy.json` | One service named `trainer` with mesh and persist entitlements. |
| `tests/` | In-process tests on the loopback interface; no device, no Docker. |

## How a member picks its work

`wendy fleet train up --sweep` bakes the whole sweep into `WT_SWEEP_PARAMS`, a
JSON list of parameter dictionaries, and gives each device its own
`WT_SWEEP_INDEX`:

```
WT_SWEEP_PARAMS='[{"seed": 1}, {"seed": 2}, {"es.lr": 0.05}]'
WT_SWEEP_INDEX=1
```

The member merges `params[WT_SWEEP_INDEX]` into the single template's
configuration. Keys may be dotted paths (`es.lr`) or bare keys (`seed`); a
bare key must match exactly one section of the defaults, otherwise the member
fails loudly instead of guessing where the value belongs. Every member appends
its index to `WT_RUN_ID` (run `demo` on member 2 becomes `demo-2`), so
checkpoints never collide and each run keeps the single template's full
durability contract: resume with optimizer state, honest metrics, and an
artifact manifest.

## Collecting results

Each member trains to completion, then serves its result on
`GET /result` (port `MESH_PORT`, default 8080) and keeps serving:

```json
{"run_id": "demo-1", "iterations": 200, "final_mean_return": 341.2,
 "best_mean_return": 355.7, "sweep_index": 1, "params": {"seed": 2}}
```

The endpoint requires the fleet bearer token, so `collect.py` needs it too. It
reads `--token`, or `WT_FLEET_TOKEN` from the environment. `wendy fleet train`
generates the token at deploy time and saves it under the configuration
directory, at `~/.wendy/train/<group>__<appId>.json`, with both name parts
lowercased and every character outside letters, digits, hyphen, and dot
replaced by an underscore. For group `spark-*` and this template that is
`~/.wendy/train/spark-___sh.wendy.training.sweep.json`. From any machine that
can reach the members:

```sh
export WT_FLEET_TOKEN=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['token'])" \
  ~/.wendy/train/spark-___sh.wendy.training.sweep.json)
python collect.py device-a.local:8080 device-b.local:8080 device-c.local:8080
```

`jq -r .token ~/.wendy/train/spark-___sh.wendy.training.sweep.json` reads the
same field. The file is written owner-readable only, because the token is what
keeps the training endpoints closed.

`collect.py` polls until every member responds or `--timeout-s` (default 300)
passes, writes `results.json` sorted by final mean return, and prints the
table. A member that never responds gets a `{"status": "unreachable"}` row
rather than aborting the table, and the exit code is nonzero so scripts
notice.

## Building and deploying

The Dockerfile expects a staged build context: this template's files, the
single template's `train.py` staged as `single_train.py`, the shared
`cartpole.py`, and the pip-installable `wendytrain` project in a `wendytrain/`
subdirectory. `wendy fleet train up --group <group> --template sweep --sweep
<params.json>` prepares that staging directory and gives each device its own
`WT_SWEEP_INDEX` from the parameter array, which must hold exactly one object
per device. The `wendy` Command Line Interface (CLI) rejects build contexts
that reach into parent directories, which is why staging exists. To stage by
hand:

```sh
STAGE=$(mktemp -d)
cp -R Training/templates/sweep/ "$STAGE"/
cp Training/templates/single/train.py "$STAGE"/single_train.py
cp Training/templates/single/cartpole.py "$STAGE"/
cp -R Training/wendytrain "$STAGE"/wendytrain
docker build "$STAGE"
```

The persist entitlement mounts the volume `wt-sweep-ckpt` at
`/data/checkpoints`; the mesh entitlement exposes port 8080 to peers so the
collector can reach `/result`.

## Tests

```sh
python3 -m venv .venv
.venv/bin/pip install numpy pytest -e Training/wendytrain
.venv/bin/python -m pytest Training/templates/sweep/tests -q
```
