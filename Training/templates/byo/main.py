"""Bring-your-own training entry point: the layer-0 contract, nothing more.

This is a starting point, not a framework. It resolves the documented
environment contract (role, peers, run directory), prints it, answers
GET /healthz, and then waits. Replace the wait with any training stack;
README.md shows how to wire torch.distributed or anything else on top.
"""

import json
import os
import signal
import threading

from wendytrain import Fleet, serve


def main() -> None:
    # Enumerated ${VAR} passthrough in wendy.json can deliver unset variables
    # as empty strings; treat empty as unset so every default applies.
    env = {k: v for k, v in os.environ.items() if v != ""}
    try:
        fleet = Fleet.from_env(env)
    except ValueError as exc:
        raise SystemExit(f"cannot resolve the layer-0 contract: {exc}") from exc

    run_dir = os.path.join(fleet.ckpt_dir, fleet.run_id)
    os.makedirs(run_dir, exist_ok=True)
    contract = {
        "role": fleet.role,
        "self": fleet.self_id,
        "peers": fleet.peers,
        "port": fleet.port,
        "run_id": fleet.run_id,
        "run_dir": run_dir,
    }
    print("resolved layer-0 contract:", flush=True)
    for key, value in contract.items():
        print(f"  {key} = {value}", flush=True)

    body = json.dumps({"status": "ok", **contract}).encode()
    server = serve(
        {("GET", "/healthz"): lambda _request: (200, body, "application/json")},
        port=fleet.port,
    )
    print(f"serving GET /healthz on port {server.server_address[1]}", flush=True)

    # Your training stack goes here. The coordinator (the lowest asset id,
    # already resolved into contract["role"]) is the natural rendezvous
    # point for collectives; see README.md for a torch.distributed example.
    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    stop.wait()
    server.shutdown()


if __name__ == "__main__":
    main()
