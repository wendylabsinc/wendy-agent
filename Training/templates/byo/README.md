# byo: bring your own training stack

This template is the layer-0 contract and nothing else. It is a starting
point, not a framework: `main.py` resolves the contract, prints it, answers
`GET /healthz`, and waits. Everything interesting is yours to write. If you
want a working training loop out of the box, use `single`, `sweep`,
`es-fleet`, or `ppo-fleet` instead.

## The contract you get

When `wendy fleet train up --group <group> --template byo` deploys this
template to the devices a group resolves to, every container starts with:

| Variable | Meaning |
|---|---|
| `MESH_SELF` | this device's asset id |
| `MESH_PEERS` | comma list of every fleet member (asset ids, or resolved `address:port` under `--transport lan`) |
| `MESH_PORT` | default port for peer entries without one (default 8080) |
| `WT_ROLE` | `coordinator` on the lowest asset id, `worker` elsewhere, unless overridden with `--role <device>=<role>` |
| `WT_RUN_ID` | stable run identifier |
| `WT_CKPT_DIR` | persistent checkpoint root, `/data/checkpoints` by this template's persist entitlement |
| `WT_COORDINATOR` | the coordinating device as `host:port`, ready to dial without parsing `MESH_PEERS` |
| `WT_NODE_INDEX` | this device's rank, counting from 0 by ascending asset id, coordinator first |
| `WT_NODE_COUNT` | how many devices are in this run |
| `WT_FLEET_TOKEN` | shared bearer token; the fleet endpoints require `Authorization: Bearer <token>` |

The last four rows are newer than the rest, and they arrive for a different
reason. `wendy.json` has no wildcard environment passthrough, so a template
only forwards the `${VAR}` keys it enumerates, and this one's list stops at
`WT_CONFIG`. The subcommand injects the per-device variables at container
create instead, where an injected value overrides whatever `wendy.json` says
for the same key, so the whole table above lands in the container whether or
not it is enumerated. Add your own with `--env KEY=VALUE`, which arrives the
same way. Only a plain `wendy run` of this directory is still limited to the
enumerated list.

`wendytrain.Fleet.from_env()` turns those into resolved values, as `main.py`
shows, but the variables are the contract; you can read them with
`os.environ` in any language and delete `wendytrain` entirely.

`wendy.json` grants a mesh network entitlement (peers reach each other on
port 8080 across devices) and a persist entitlement (checkpoints survive
container restarts). That is the whole substrate.

## Wiring torch.distributed on top

The deterministic role rule gives you a rendezvous point for free: exactly
one device resolves `WT_ROLE=coordinator`, and its address is a peer entry
every other device already has. A minimal `TCPStore` rendezvous:

```python
import os
import torch.distributed as dist
from wendytrain import Fleet

fleet = Fleet.from_env()
world_size = len(fleet.peers) + 1

# Rank 0 is the coordinator; workers rank themselves by their sorted
# position among the fleet's asset ids.
ordered = sorted([fleet.self_id] + [p.split(":")[0].split("-")[1].split(".")[0]
                                    for p in fleet.peers], key=int)
rank = ordered.index(fleet.self_id)

if fleet.role == "coordinator":
    store = dist.TCPStore("0.0.0.0", fleet.port + 1, world_size, is_master=True)
else:
    coordinator_host = fleet.peers[0].rsplit(":", 1)[0]  # or find rank 0's entry
    store = dist.TCPStore(coordinator_host, fleet.port + 1, world_size, is_master=False)

dist.init_process_group("gloo", store=store, rank=rank, world_size=world_size)
```

Add `torch` to the Dockerfile (`pip install torch --index-url
https://download.pytorch.org/whl/cpu`), open the extra port in `wendy.json`
(add `{ "host": 8081, "container": 8081 }` to the mesh entitlement's ports),
and replace the wait in `main.py` with your loop. The same shape works for a
JAX coordination service, a Ray head node, or a plain socket protocol: the
coordinator listens, everyone else has its address.

The parsing above is deliberately crude; it assumes mesh-transport peer
entries (`device-<id>.cloud.wendy.dev:<port>`). `WT_NODE_INDEX` and
`WT_NODE_COUNT` give you the same rank and world size without any parsing, and
`WT_COORDINATOR` gives you the rendezvous host directly. Adapt whichever you
prefer, or carry your own ranks through repeated `--env KEY=VALUE` flags. This
template will not do it for you; that is the point.

## What you may still want from the library

Nothing in `wendytrain` is required here, but three pieces compose well with
any stack: `Run` (atomic checkpoints and resume under `WT_CKPT_DIR`), `wire`
(a self-describing array codec for peer-to-peer payloads), and
`write_manifest` (a checksummed artifact manifest at the deployment
boundary). Import them individually or not at all.

## Running it

```sh
wendy fleet train up --group <group> --lan --template byo --dry-run  # audit the plan, nothing runs
wendy fleet train up --group <group> --lan --template byo
wendy fleet train status --group <group> --lan
wendy --device <device> device logs sh.wendy.training.byo
wendy fleet train stop --group <group> --lan
```

Drop `--lan` to resolve the group from cloud device tags instead of matching
device names over multicast Domain Name System (mDNS). Point `--template` at
this directory instead of naming it to iterate on your own copy.
