"""Sweep member: one independent training run per device, results collected over HTTP.

The sweep is the fan-out primitive: the fleet launcher bakes a JSON list of
parameter dictionaries into ``WT_SWEEP_PARAMS`` and gives every device its own
``WT_SWEEP_INDEX``. Each member merges its parameter set into the single
template's configuration, reuses ``train_loop`` from that template unchanged
(same resume guarantees, same artifact manifest), then serves its result as
JSON on ``GET /result`` and keeps serving so a collector can fetch it at any
time after completion.

Parameter keys may be dotted paths into the configuration (``es.lr``) or bare
keys (``seed``); a bare key must match exactly one section of the defaults,
otherwise the member fails loudly instead of guessing.

Environment contract, in addition to the single template's:
    WT_SWEEP_INDEX   this member's index into the parameter list, default 0
    WT_SWEEP_PARAMS  JSON list of parameter dictionaries, default empty
    MESH_PORT        port for the result service, default 8080
"""

from __future__ import annotations

import importlib.util
import json
import os
import sys
import time
from pathlib import Path

from wendytrain import Run, load_config
from wendytrain.config import Config
from wendytrain.service import serve

_HERE = Path(__file__).resolve().parent


def _load_single_train():
    """Import the single template's trainer.

    In the container the fleet launcher stages ``templates/single/train.py``
    as ``single_train.py`` next to this file, so the plain import wins. In the
    repository checkout the module is loaded straight from the single
    template's directory instead.
    """
    try:
        import single_train  # staged next to this file by the fleet launcher

        return single_train
    except ImportError:
        path = _HERE.parent / "single" / "train.py"
        spec = importlib.util.spec_from_file_location("single_train", path)
        module = importlib.util.module_from_spec(spec)
        sys.modules["single_train"] = module
        spec.loader.exec_module(module)
        return module


single_train = _load_single_train()


def resolve_params(env=None) -> tuple[int, dict]:
    """Read this member's index and parameter set from the environment."""
    if env is None:
        env = os.environ
    index = int(env.get("WT_SWEEP_INDEX", "0"))
    raw = env.get("WT_SWEEP_PARAMS", "[]")
    try:
        params_list = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError(f"WT_SWEEP_PARAMS is not valid JSON: {exc}") from exc
    if not isinstance(params_list, list):
        raise ValueError(
            f"WT_SWEEP_PARAMS must be a JSON list of parameter objects, "
            f"got {type(params_list).__name__}"
        )
    if not params_list:
        return index, {}
    if not 0 <= index < len(params_list):
        raise ValueError(
            f"WT_SWEEP_INDEX {index} is out of range for {len(params_list)} parameter sets"
        )
    params = params_list[index]
    if not isinstance(params, dict):
        raise ValueError(
            f"WT_SWEEP_PARAMS[{index}] must be a parameter object, "
            f"got {type(params).__name__}"
        )
    return index, params


def _resolve_bare_key(data: dict, key: str) -> list[str]:
    """Resolve a bare parameter key against the configuration tree.

    A top-level scalar matches itself; otherwise the key must appear in
    exactly one section. Anything else raises, so a typo or an ambiguous name
    never lands silently in the wrong place.
    """
    if key in data and not isinstance(data[key], dict):
        return [key]
    sections = [
        section
        for section, value in data.items()
        if isinstance(value, dict) and key in value
    ]
    if len(sections) == 1:
        return [sections[0], key]
    if not sections:
        raise ValueError(
            f"sweep parameter {key!r} does not match any configuration key; "
            f"use a dotted path such as 'es.{key}'"
        )
    raise ValueError(
        f"sweep parameter {key!r} is ambiguous, found in sections {sections}; "
        "use a dotted path"
    )


def apply_params(cfg: Config, params: dict) -> Config:
    """Merge one parameter set into a configuration, later wins.

    Values are type-checked against existing keys the same way the config
    loader checks its layers: an integer promotes to a float default, any
    other mismatch raises ``TypeError``. Dotted keys may extend the schema.
    """
    data = cfg.as_dict()
    for key, value in params.items():
        path = key.split(".") if "." in key else _resolve_bare_key(data, key)
        node = data
        for part in path[:-1]:
            node = node.setdefault(part, {})
            if not isinstance(node, dict):
                raise ValueError(
                    f"sweep parameter {key!r} descends through {part!r}, "
                    "which is not a configuration section"
                )
        leaf = path[-1]
        existing = node.get(leaf)
        if existing is not None and not isinstance(existing, dict):
            if type(value) is not type(existing):
                if isinstance(existing, float) and type(value) is int:
                    value = float(value)
                else:
                    raise TypeError(
                        f"sweep parameter {key!r} expects {type(existing).__name__} "
                        f"(default {existing!r}), got {type(value).__name__} ({value!r})"
                    )
        node[leaf] = value
    return Config(data)


def train_member(env=None, workers: int | None = None) -> dict:
    """Train this member's run and return its result dictionary."""
    if env is None:
        env = os.environ
    index, params = resolve_params(env)
    cfg = apply_params(load_config(single_train.DEFAULTS, env=env), params)
    base_run_id = env.get("WT_RUN_ID", "default")
    run = Run(env.get("WT_CKPT_DIR", "/data/checkpoints"), run_id=f"{base_run_id}-{index}")
    print(f"[sweep] member {index} params {json.dumps(params)} run {run.run_id}", flush=True)
    result = single_train.train_loop(cfg, run, workers=workers)
    result["sweep_index"] = index
    result["params"] = params
    return result


def serve_result(result: dict, port: int, host: str = "0.0.0.0",
                 token: str | None = None):
    """Serve ``result`` as JSON on ``GET /result``; returns the server."""
    body = json.dumps(result).encode()

    def handler(_request_body: bytes) -> tuple[int, bytes, str]:
        return 200, body, "application/json"

    return serve({("GET", "/result"): handler}, port, host=host, token=token)


def main() -> None:
    # Enumerated ${VAR} passthrough in wendy.json can deliver unset variables
    # as empty strings; treat empty as unset so every default applies.
    env = {k: v for k, v in os.environ.items() if v != ""}
    workers = int(env.get("WT_WORKERS", "0")) or None
    result = train_member(env=env, workers=workers)
    port = int(env.get("MESH_PORT", "8080"))
    server = serve_result(result, port, token=env.get("WT_FLEET_TOKEN"))
    print(
        f"[sweep] member {result['sweep_index']} finished, serving /result on "
        f"port {server.server_address[1]}: " + json.dumps(result),
        flush=True,
    )
    # Keep serving so the collector can fetch the result whenever it polls.
    while True:
        time.sleep(60)


if __name__ == "__main__":
    main()
