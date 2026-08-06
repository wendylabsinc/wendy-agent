# es-fleet: Evolution Strategies across a device fleet

One coordinator and any number of workers train a policy with mirrored-sampling
Evolution Strategies (ES). Workers regenerate perturbations from integer seeds,
so only seeds and scalar returns cross the wire; the gradient estimate, the
combined rank normalization, and the Adam update all come from `wendytrain.es`
and `wendytrain.optim`. The built-in cartpole environment stands in for your
problem; replace `episode_return` in `train.py` and keep the lifecycle.

## Topology

The lowest numeric asset id among `MESH_SELF` plus `MESH_PEERS` coordinates;
every other node is a worker. Each node's worker index is its rank in the
sorted id list, and `wendytrain.mesh.worker_slice` gives it a disjoint share of
the population. The coordinator runs a loopback worker thread over the same
HyperText Transfer Protocol (HTTP) protocol, so its own slice is evaluated
instead of timing out every generation.

Pin roles with `WT_ROLE=coordinator` or `WT_ROLE=worker` when the automatic
rule does not fit. When peers are hostnames rather than asset ids, also set
`ES_COORDINATOR` (`host:port`), `ES_WORKER_INDEX`, and `ES_WORKER_COUNT`; the
template refuses to guess.

## Endpoints (coordinator, port 8080)

| Endpoint | Payload |
|---|---|
| `GET /params` | wire blob: `theta` plus metadata `generation`, `seed_base`, `architecture` (hidden layer sizes), `population`, `sigma`, `done` |
| `POST /returns` | wire blob: `seeds`, `returns_plus`, `returns_minus`, metadata `generation` |
| `GET /status` | JavaScript Object Notation (JSON): `generation`, `population`, `n_contributed`, `mean_return`, `best_return`, `stale_posts`, `pending_contributions`, `done` |

The architecture always travels in the wire metadata; workers construct the
policy from it and raise on any mismatch rather than inferring shapes from
parameter counts. Posts for any generation other than the current one are
dropped and counted in `stale_posts`.

## Durability and honesty

Every checkpoint carries `theta` and the full Adam state (`adam_m`, `adam_v`,
`adam_t`); a restarted coordinator resumes at the next generation with the
optimizer intact and logs `resumed generation=<n> adam_t=<t>` as its first
line. When a generation hits `ES_GEN_TIMEOUT_S` the coordinator advances with
whatever arrived, and both the metrics line and `/status` record
`n_contributed` against `population`; a status field that lies is worse than
none. On completion the coordinator exports `params.wtw` plus a checksummed
`manifest.json` into the run directory.

## Configuration

Defaults live in `config.toml` (population, sigma, learning rate, generation
timeout, checkpoint cadence, hidden layer sizes). Environment variables
override the file: the layered form `WT_ES__POP` works wherever arbitrary
variables can be forwarded, and the enumerated direct forms (`ES_POP`,
`ES_SIGMA`, `ES_LR`, `ES_GEN_TIMEOUT_S`, `ES_MAX_GENERATIONS`,
`ES_CHECKPOINT_EVERY`, `ES_SEED`) are passed through `wendy.json` and win
last. `WT_RUN_ID` names the run; keep it stable across restarts so resume
finds the checkpoints under `/data/checkpoints`.

## Deploying

The fleet launcher (`Training/launch/fleet.py`) stages a build context
containing this directory, the pip-installable `wendytrain` tree at
`wendytrain/`, and `cartpole.py` from `templates/single/`, then drives the
`wendy` Command Line Interface (CLI) per device with the mesh variables
computed for you. The Dockerfile only ever copies from that staged context;
parent-directory contexts are rejected by the CLI. Devices with a Graphics Processing Unit (GPU)
can add `{"type": "gpu"}` to the service entitlements; nothing in the template
requires one.

## Testing locally

```
python -m pytest Training/templates/es-fleet/tests -q
```

The suite runs an in-process fleet (coordinator plus two worker threads on
loopback ephemeral ports) and pins the five correctness requirements: resume,
honest partial generations, library-only gradient math, architecture in the
wire metadata, and stale-post accounting.
