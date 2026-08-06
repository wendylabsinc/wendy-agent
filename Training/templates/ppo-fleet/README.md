# ppo-fleet template

Proximal Policy Optimization (PPO) across a fleet: one learner owns the
policy, the value network, and the optimizer; any number of actors pull
weights, collect trajectories in the built-in cart-pole environment, and post
them back over the mesh. This is the template for off-policy actor pools
where trajectories, not scalar returns, travel over the wire; for population
methods with scalar returns use `es-fleet` instead.

PyTorch is the one framework dependency and it stays inside this template;
the `wendytrain` core remains pure NumPy. Torch is imported lazily, so any
import failure produces one actionable message naming the install command.

## How it works

Roles come from the layer-0 contract. With `WT_ROLE=auto` the lowest numeric
asset id among `MESH_SELF` plus `MESH_PEERS` becomes the learner and every
other device an actor; `WT_ROLE=learner` or `WT_ROLE=actor` pins a device
explicitly.

The learner serves three endpoints on `MESH_PORT` (default 8080):

| Endpoint | Payload |
|---|---|
| `GET /weights` | wire blob: flat `policy` and `value` parameter vectors; metadata `version`, `architecture` (hidden sizes list), `log_std` |
| `POST /rollout` | wire blob: `obs`, `actions`, `logprobs`, `rewards`, `dones`, `values`; metadata `weights_version`, `tail_bootstrap`, `episode_returns` |
| `GET /status` | JSON: `version`, `updates`, `accepted_rollouts`, `stale_rollouts`, `queue_depth`, `mean_return`, `complete` |

Receivers rebuild networks from the `architecture` metadata; nothing is ever
inferred from parameter counts. Rollouts whose `weights_version` lags the
current version by more than the staleness budget are rejected with a JSON
reply naming the reason, and every rejection is counted in `/status` as
`stale_rollouts`; nothing is silently dropped. Once the learner has applied
its configured number of updates it keeps serving `/weights` and `/status`
but declines further rollouts with the reason `complete`.

The library's Generalized Advantage Estimation (GAE) bootstraps 0 past the
end of a batch, so when an actor's final step does not end its episode the
actor appends the synthetic bootstrap step itself (reward and value both set
to the value estimate of the final observation, marked done) and flags it
with `tail_bootstrap`; the learner drops that row again after computing
advantages.

## Durability

Checkpoints carry the policy, the value network, the log standard deviation,
and the complete torch optimizer state dictionary serialized with
`torch.save` into a `uint8` wire array. A restarted learner resumes at the
saved version with Adam step counts intact and logs one line:

    [ppo-fleet] resumed version=<n> adam_steps=<t>

Metrics append to `metrics.jsonl` in the run directory; every update records
the rollout's `weights_version` and the running `stale_rollouts` count, so
partial or stale contributions are visible in the record.

## Configuration

Defaults live in `train.py` and are mirrored in `config.toml`; point
`WT_CONFIG=/app/config.toml` at the baked file or override single keys with
environment variables such as `WT_PPO__LR=1e-4`. Three variables are read
directly:

| Variable | Meaning | Default |
|---|---|---|
| `PPO_STEPS` | environment steps per rollout an actor collects | `ppo.steps` (512) |
| `PPO_MAX_STALENESS` | accepted weight-version lag before rejection | `ppo.max_staleness` (2) |
| `PPO_LEARNER` | explicit learner `host:port` when peers are not numeric asset ids | derived from `MESH_PEERS` |

The standard `WT_RUN_ID`, `WT_CKPT_DIR`, `MESH_*` contract applies; see the
`Training/` documentation for the full table.

## Deploying

Use the fleet launcher, which stages the build context (this directory plus
`wendytrain/` plus `cartpole.py` from `templates/single/`) and computes the
per-device environment:

    python Training/launch/fleet.py up --config fleet.toml

with `template = "ppo-fleet"` in `fleet.toml`. The `wendy.json` here declares
the mesh entitlement (serviceCIDR 10.99.0.0/16, port 8080) and a persist
volume `wt-ppo-ckpt` at `/data/checkpoints`, so checkpoints outlive the
container.

## Testing locally

    pip install numpy pytest torch -e Training/wendytrain
    python -m pytest Training/templates/ppo-fleet/tests -q

The suite proves the three plan requirements in process: return improvement
over 15 versions, resume restoring optimizer step counts exactly, and stale
rollouts rejected and counted in `/status`. The tests skip with an
explanatory message when torch is not installed.
