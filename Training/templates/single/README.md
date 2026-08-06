# single: one device, full run lifecycle

Trains a small policy on the built-in NumPy cart-pole with mirrored-sampling
Evolution Strategies (ES) on a single WendyOS device. This is the smallest
complete example of the `wendytrain` run lifecycle: layered configuration, a
durable run that resumes from its latest checkpoint including optimizer state,
honest per-generation metrics, and an artifact manifest when training
completes.

## Files

| File | Purpose |
|---|---|
| `cartpole.py` | Built-in environment, pure NumPy, deterministic per seed. Other templates copy this file at build time. |
| `train.py` | The trainer. Exposes `train_loop(cfg, run) -> dict`, which the sweep template imports. |
| `config.toml` | Baked-in configuration; every key overridable per deploy. |
| `Dockerfile` | Builds from a staged context (see below). |
| `wendy.json` | One service named `trainer` with a persist entitlement. |
| `tests/` | In-process tests; no device, no Docker, no network. |

## How training works

The policy is a tanh Multi-Layer Perceptron (MLP) held as one flat float32
vector. Each generation evaluates `es.pop` mirrored perturbation pairs (the
environment seed equals the perturbation seed, so both sides of a pair see the
same episode randomness), estimates the ascent direction with
`wendytrain.es.gradient` (combined rank normalization), and applies
`wendytrain.optim.adam_step(..., maximize=True)`. Evaluation fans out over a
process pool of `WT_WORKERS` processes (default: the CPU count).

## Configuration

Defaults live in `train.py` and are mirrored by `config.toml`; the layering is
defaults, then the file named by `WT_CONFIG`, then `WT_<SECTION>__<KEY>`
environment variables. Later wins.

| Key | Default | Meaning |
|---|---|---|
| `es.pop` | 32 | mirrored perturbation pairs per generation |
| `es.sigma` | 0.1 | perturbation scale |
| `es.lr` | 0.02 | Adam learning rate |
| `run.max_iterations` | 200 | generations to train |
| `run.checkpoint_every` | 10 | checkpoint cadence in generations |
| `run.seed` | 0 | initialization and perturbation stream seed |
| `policy.hidden` | `[32]` | hidden layer widths |

Example: `WT_ES__POP=64 WT_RUN__MAX_ITERATIONS=500` doubles the population and
extends the run, with no image rebuild.

## Durability contract

A checkpoint at iteration `n` contains the policy and the full Adam state
(`m`, `v`, and the step count `t`), so a restarted container continues the
exact trajectory rather than restarting from random weights. On resume the
first log line reports `resumed iteration=<n> adam_t=<t>`. The test suite
proves the stronger property that a run interrupted at generation 10 and
resumed to 20 produces bit-identical parameters to an uninterrupted 20
generation run. Metrics append to `metrics.jsonl` with `population` and
`n_contributed` on every line.

On completion the run directory gains `policy.wtw` (the flat parameters plus
the architecture in metadata, so consumers never infer network shape from
parameter counts) and a `manifest.json` with SHA-256 checksums;
`wendytrain.manifest.verify_manifest` validates the artifact before
deployment.

## Building and deploying

The Dockerfile expects a staged build context: this template's files plus the
pip-installable `wendytrain` project in a `wendytrain/` subdirectory. The
fleet launcher (`Training/launch/fleet.py`) prepares that staging directory
and drives the `wendy` Command Line Interface (CLI); building this directory
directly with Docker fails at `pip install ./wendytrain`, by design, because
the CLI rejects build contexts that reach into parent directories and the
repository keeps exactly one copy of the library. To stage by hand:

```sh
STAGE=$(mktemp -d)
cp -R Training/templates/single/ "$STAGE"/
cp -R Training/wendytrain "$STAGE"/wendytrain
docker build "$STAGE"
```

The persist entitlement mounts the volume `wt-single-ckpt` at
`/data/checkpoints`, which is the default `WT_CKPT_DIR`; checkpoints survive
container restarts and redeploys.

## Tests

```sh
python3 -m venv .venv
.venv/bin/pip install numpy pytest -e Training/wendytrain
.venv/bin/python -m pytest Training/templates/single/tests -q
```
