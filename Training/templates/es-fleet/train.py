"""es-fleet: mirrored-sampling Evolution Strategies (ES) across a device fleet.

One coordinator and any number of workers train a small tanh Multi-Layer
Perceptron (MLP) policy on the built-in cartpole environment. Workers pull the
current parameters over HyperText Transfer Protocol (HTTP), evaluate their
disjoint slice of the mirrored population, and post scalar returns back; only
seeds and returns cross the wire, never perturbation vectors.

The coordinator owns the durable run: every checkpoint carries the policy
parameters and the full Adam state, so a restart continues the run instead of
silently restarting it. Generations that time out advance with whatever
arrived, and the metrics line and the /status endpoint say so through
``n_contributed`` and ``population``; a status field that lies is worse than
none. The coordinator also runs a loopback worker thread so its own population
slice is evaluated rather than waiting out the timeout every generation.

Endpoints served by the coordinator:

    GET  /params   wire blob: ``theta`` plus metadata ``generation``,
                   ``seed_base``, ``architecture`` (hidden layer sizes),
                   ``population``, ``sigma``, ``done``
    POST /returns  wire blob: ``seeds``, ``returns_plus``, ``returns_minus``,
                   metadata ``generation``; stale generations are dropped and
                   counted
    GET  /status   JavaScript Object Notation (JSON): generation, mean and
                   best return, ``n_contributed``, ``population``,
                   ``stale_posts``, ``pending_contributions``, ``done``

Roles come from the layer-0 contract (``MESH_SELF``, ``MESH_PEERS``,
``WT_ROLE``): the lowest numeric asset id coordinates, everyone else works.
"""

import json
import os
import sys
import threading
import time
from pathlib import Path
from typing import Mapping

import numpy as np

from wendytrain import Run, es, load_config, mesh, optim, wire
from wendytrain.config import Config
from wendytrain.manifest import write_manifest
from wendytrain.service import serve

from cartpole import CartPole

OBS_DIM = CartPole.obs_dim
ACT_DIM = CartPole.act_dim
OBSERVATION_LAYOUT = (
    "cart position (m), cart velocity (m/s), pole angle (rad), "
    "pole angular velocity (rad/s)"
)

DEFAULTS = {
    "es": {"pop": 32, "sigma": 0.1, "lr": 0.02, "gen_timeout_s": 30.0},
    "run": {"max_generations": 200, "checkpoint_every": 10, "seed": 0},
    "policy": {"hidden": [32]},
}

# Direct environment overrides for the most commonly tuned knobs. The layered
# WT_<SECTION>__<KEY> form also works when a deployment can forward arbitrary
# variables; these named ones exist because wendy.json enumerates its
# passthrough variables explicitly.
_ES_ENV_OVERRIDES = {
    "ES_POP": ("es", "pop", int),
    "ES_SIGMA": ("es", "sigma", float),
    "ES_LR": ("es", "lr", float),
    "ES_GEN_TIMEOUT_S": ("es", "gen_timeout_s", float),
    "ES_MAX_GENERATIONS": ("run", "max_generations", int),
    "ES_CHECKPOINT_EVERY": ("run", "checkpoint_every", int),
    "ES_SEED": ("run", "seed", int),
}


# --- Policy: a tanh MLP over flat parameters ---------------------------------


def layer_sizes(hidden: list) -> list[int]:
    """Full layer size list for the policy: observation, hidden layers, action."""
    return [OBS_DIM, *[int(h) for h in hidden], ACT_DIM]


def param_count(sizes: list[int]) -> int:
    """Number of flat parameters (weights plus biases) for ``sizes``."""
    return sum((fan_in + 1) * fan_out for fan_in, fan_out in zip(sizes, sizes[1:]))


def init_theta(sizes: list[int], seed: int) -> np.ndarray:
    """Deterministic float32 initialization, scaled by 1/sqrt(fan_in)."""
    rng = np.random.default_rng(seed)
    parts = []
    for fan_in, fan_out in zip(sizes, sizes[1:]):
        weights = rng.standard_normal((fan_in, fan_out)) / np.sqrt(fan_in)
        parts.append(weights.astype(np.float32).ravel())
        parts.append(np.zeros(fan_out, dtype=np.float32))
    return np.concatenate(parts)


def policy_action(theta: np.ndarray, sizes: list[int], obs: np.ndarray) -> np.ndarray:
    """Forward pass; tanh everywhere keeps the force inside [-1, 1]."""
    x = np.asarray(obs, dtype=np.float32)
    offset = 0
    for fan_in, fan_out in zip(sizes, sizes[1:]):
        weights = theta[offset : offset + fan_in * fan_out].reshape(fan_in, fan_out)
        offset += fan_in * fan_out
        bias = theta[offset : offset + fan_out]
        offset += fan_out
        x = np.tanh(x @ weights + bias)
    return x


def episode_return(theta: np.ndarray, sizes: list[int], seed: int) -> float:
    """One cartpole episode's total return, deterministic under ``seed``.

    Both sides of a mirrored pair pass the same seed, so plus and minus
    evaluations face identical episode randomness (common random numbers).
    """
    env = CartPole(seed)
    obs = env.reset(seed=seed)
    total = 0.0
    done = False
    while not done:
        obs, reward, done, _ = env.step(policy_action(theta, sizes, obs))
        total += reward
    return float(total)


def sizes_from_meta(meta: dict, num_params: int) -> list[int]:
    """Build the layer size list from wire metadata, refusing ever to infer.

    Raises ``ValueError`` when the metadata carries no architecture or when
    the declared architecture does not account for ``num_params`` parameters.
    """
    if "architecture" not in meta:
        raise ValueError(
            "wire metadata carries no 'architecture'; refusing to infer the "
            "network shape from parameter counts"
        )
    sizes = layer_sizes(meta["architecture"])
    expected = param_count(sizes)
    if expected != num_params:
        raise ValueError(
            f"architecture {list(meta['architecture'])} implies {expected} "
            f"parameters but the theta array has {num_params}"
        )
    return sizes


# --- Coordinator --------------------------------------------------------------


class Coordinator:
    """Owns the run: parameters, Adam state, generations, and the HTTP service.

    ``n_nodes`` is the total number of nodes in the fleet including this one;
    the coordinator's loopback worker evaluates slice 0 and remote workers
    take their rank-ordered slices. On construction the newest checkpoint, if
    any, restores theta, the Adam moments and step count, and the generation
    counter; the architecture recorded in the checkpoint wins over the
    configuration so a resumed run can never silently change shape.
    """

    def __init__(
        self,
        cfg: Config,
        run: Run,
        *,
        n_nodes: int,
        host: str = "0.0.0.0",
        port: int = mesh.DEFAULT_MESH_PORT,
        loopback: bool = True,
    ):
        if n_nodes < 1:
            raise ValueError(f"n_nodes must be at least 1, got {n_nodes}")
        self.run = run
        self.population = int(cfg.es.pop)
        self.sigma = float(cfg.es.sigma)
        self.lr = float(cfg.es.lr)
        self.gen_timeout_s = float(cfg.es.gen_timeout_s)
        self.max_generations = int(cfg.run.max_generations)
        self.checkpoint_every = int(cfg.run.checkpoint_every)
        self.hidden = [int(h) for h in cfg.policy.hidden]
        self.n_nodes = n_nodes
        self._host = host
        self._port_requested = port
        self._loopback = loopback

        restored = run.load_latest()
        if restored is not None:
            arrays, meta, iteration = restored
            checkpoint_hidden = [int(h) for h in meta.get("architecture", self.hidden)]
            if checkpoint_hidden != self.hidden:
                print(
                    f"[es-fleet] checkpoint architecture {checkpoint_hidden} "
                    f"overrides configured {self.hidden}",
                    file=sys.stderr,
                    flush=True,
                )
                self.hidden = checkpoint_hidden
            self.theta = np.asarray(arrays["theta"], dtype=np.float32)
            if "adam_t" in arrays:
                self.adam_state = {
                    "m": arrays["adam_m"],
                    "v": arrays["adam_v"],
                    "t": arrays["adam_t"],
                }
            else:
                self.adam_state = None
            self.generation = iteration + 1
            self.resumed = True
        else:
            self.theta = init_theta(layer_sizes(self.hidden), int(cfg.run.seed))
            self.adam_state = None
            self.generation = 0
            self.resumed = False

        self.sizes = layer_sizes(self.hidden)
        self.num_params = param_count(self.sizes)
        if self.theta.size != self.num_params:
            raise ValueError(
                f"checkpoint theta has {self.theta.size} parameters but the "
                f"architecture {self.hidden} implies {self.num_params}"
            )

        self._cond = threading.Condition()
        self._contrib: dict[int, tuple[float, float]] = {}
        self._stale_posts = 0
        self._last_n = 0
        self._last_mean: float | None = None
        self._last_best: float | None = None
        self._last_saved = self.generation - 1 if self.resumed else -1
        self._done = False
        self._stop = threading.Event()
        self._server = None
        self._loopback_thread = None
        self.port: int | None = None

    # -- service ---------------------------------------------------------------

    def start(self) -> None:
        """Serve the endpoints and start the loopback worker thread."""
        routes = {
            ("GET", "/params"): self._handle_params,
            ("POST", "/returns"): self._handle_returns,
            ("GET", "/status"): self._handle_status,
        }
        self._server = serve(routes, self._port_requested, host=self._host)
        self.port = self._server.server_address[1]
        if self._loopback:
            self._loopback_thread = threading.Thread(
                target=worker_loop,
                args=("in-process", 0, self.n_nodes),
                kwargs={
                    "stop": self._stop,
                    # Direct calls into the same handlers the HTTP routes use;
                    # see worker_loop's docstring for why this is not HTTP.
                    "get": lambda: self._handle_params(b"")[1],
                    "post": lambda body: self._handle_returns(body)[1],
                },
                name="es-fleet-loopback-worker",
                daemon=True,
            )
            self._loopback_thread.start()

    def stop(self) -> None:
        """Stop the loopback worker and shut the HTTP service down."""
        self._stop.set()
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
        if self._loopback_thread is not None:
            self._loopback_thread.join(timeout=10.0)

    def _seed_base(self, generation: int) -> int:
        return generation * self.population

    def _handle_params(self, body: bytes) -> tuple[int, bytes, str]:
        with self._cond:
            blob = wire.encode(
                {"theta": self.theta},
                meta={
                    "generation": self.generation,
                    "seed_base": self._seed_base(self.generation),
                    "architecture": list(self.hidden),
                    "population": self.population,
                    "sigma": self.sigma,
                    "done": self._done,
                },
            )
        return 200, blob, "application/octet-stream"

    def _handle_returns(self, body: bytes) -> tuple[int, bytes, str]:
        arrays, meta = wire.decode(body)
        generation = int(meta.get("generation", -1))
        seeds = np.asarray(arrays["seeds"], dtype=np.int64)
        returns_plus = np.asarray(arrays["returns_plus"], dtype=np.float32)
        returns_minus = np.asarray(arrays["returns_minus"], dtype=np.float32)
        if not (seeds.size == returns_plus.size == returns_minus.size):
            raise ValueError(
                f"returns post is malformed: {seeds.size} seeds, "
                f"{returns_plus.size} plus returns, {returns_minus.size} minus returns"
            )
        with self._cond:
            if self._done or generation != self.generation:
                self._stale_posts += 1
                response = {"accepted": False, "generation": self.generation}
            else:
                base = self._seed_base(self.generation)
                for seed, plus, minus in zip(
                    seeds.tolist(), returns_plus.tolist(), returns_minus.tolist()
                ):
                    if base <= seed < base + self.population:
                        self._contrib[seed] = (float(plus), float(minus))
                self._cond.notify_all()
                response = {"accepted": True, "n_contributed": len(self._contrib)}
        return 200, json.dumps(response).encode(), "application/json"

    def _handle_status(self, body: bytes) -> tuple[int, bytes, str]:
        with self._cond:
            status = {
                "generation": self.generation,
                "population": self.population,
                "n_contributed": self._last_n,
                "mean_return": self._last_mean,
                "best_return": self._last_best,
                "stale_posts": self._stale_posts,
                "pending_contributions": len(self._contrib),
                "done": self._done,
            }
        return 200, json.dumps(status).encode(), "application/json"

    # -- training ---------------------------------------------------------------

    def train(self, max_generations: int | None = None) -> None:
        """Run generations until ``max_generations``, then mark the run done."""
        total = self.max_generations if max_generations is None else max_generations
        while self.generation < total:
            self._run_generation()
        self._save_checkpoint()
        with self._cond:
            self._done = True

    def _run_generation(self) -> None:
        generation = self.generation
        deadline = time.monotonic() + self.gen_timeout_s
        with self._cond:
            while len(self._contrib) < self.population:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    break
                self._cond.wait(timeout=min(remaining, 0.1))
            contributions = dict(self._contrib)
            stale_posts = self._stale_posts
        n_contributed = len(contributions)

        if n_contributed > 0:
            seeds = sorted(contributions)  # deterministic regardless of arrival order
            returns_plus = np.array([contributions[s][0] for s in seeds], np.float32)
            returns_minus = np.array([contributions[s][1] for s in seeds], np.float32)
            gradient = es.gradient(
                returns_plus, returns_minus, seeds, self.num_params, self.sigma
            )
            new_theta, new_adam = optim.adam_step(
                self.theta, gradient, self.adam_state, lr=self.lr, maximize=True
            )
            all_returns = np.concatenate([returns_plus, returns_minus])
            mean_return = float(all_returns.mean())
            best_return = float(all_returns.max())
        else:
            new_theta, new_adam = self.theta, self.adam_state
            mean_return = None
            best_return = None
            print(
                f"[es-fleet] generation {generation}: no contributions arrived "
                f"within {self.gen_timeout_s}s; skipping the update",
                file=sys.stderr,
                flush=True,
            )

        # The same honesty on standard output: metrics.jsonl lives on a device
        # volume, and a coordinator that only logs there is silent in every
        # log viewer. A generation with zero contributions looked exactly like
        # a hung process on real hardware.
        mean_text = "none" if mean_return is None else f"{mean_return:.2f}"
        print(
            f"[es-fleet] generation {generation}: n_contributed="
            f"{n_contributed}/{self.population} mean_return={mean_text}",
            flush=True,
        )
        # Honest metrics: partial generations say exactly how partial they
        # were, and the line is attributed to its generation explicitly.
        self.run.log_metrics(
            {
                "iteration": generation,
                "generation": generation,
                "mean_return": mean_return,
                "best_return": best_return,
                "n_contributed": n_contributed,
                "population": self.population,
                "stale_posts": stale_posts,
            }
        )

        with self._cond:
            self.theta = new_theta
            self.adam_state = new_adam
            self._last_n = n_contributed
            self._last_mean = mean_return
            self._last_best = best_return
            self.generation = generation + 1
            self._contrib = {}
            self._cond.notify_all()

        if self.generation % self.checkpoint_every == 0:
            self._save_checkpoint()

    def _save_checkpoint(self) -> None:
        iteration = self.generation - 1
        if iteration < 0 or iteration == self._last_saved:
            return
        arrays = {"theta": self.theta}
        if self.adam_state is not None:
            arrays["adam_m"] = self.adam_state["m"]
            arrays["adam_v"] = self.adam_state["v"]
            arrays["adam_t"] = self.adam_state["t"]
        self.run.save_checkpoint(
            arrays, {"architecture": list(self.hidden)}, iteration
        )
        self._last_saved = iteration


# --- Worker -------------------------------------------------------------------


def worker_loop(
    coordinator: str,
    index: int,
    count: int,
    stop: threading.Event | None = None,
    poll_interval: float = 0.05,
    get=None,
    post=None,
) -> None:
    """Pull parameters, evaluate this worker's slice, post returns; repeat.

    ``coordinator`` is a ``host:port`` target. The policy is constructed from
    the architecture in the wire metadata, never inferred from parameter
    counts. Transient failures are logged and retried forever; a fleet must
    not die because the coordinator boots more slowly than its workers.

    ``get`` and ``post`` override the transport. The coordinator's loopback
    worker passes direct in-process calls: on WendyOS the host firewall can
    reject even a dial to 127.0.0.1 on the published port (observed as
    "No route to host" on real hardware), and a worker that shares the
    coordinator's process has no reason to speak HTTP to it at all.
    """
    if stop is None:
        stop = threading.Event()
    if get is None:
        def get():
            return mesh.http_get(f"http://{coordinator}/params", timeout=5.0, retries=2)
    if post is None:
        def post(body):
            return mesh.http_post(f"http://{coordinator}/returns", body, retries=3)
    last_generation = None
    while not stop.is_set():
        try:
            blob = get()
        except Exception as exc:
            print(
                f"[es-fleet] worker {index}: cannot reach coordinator "
                f"{coordinator}: {exc}",
                file=sys.stderr,
                flush=True,
            )
            stop.wait(1.0)
            continue
        arrays, meta = wire.decode(blob)
        if meta.get("done"):
            stop.wait(max(poll_interval, 0.2))
            continue
        generation = int(meta["generation"])
        if generation == last_generation:
            stop.wait(poll_interval)
            continue
        theta = np.asarray(arrays["theta"], dtype=np.float32)
        sizes = sizes_from_meta(meta, theta.size)
        sigma = float(meta["sigma"])
        population = int(meta["population"])
        seed_base = int(meta["seed_base"])

        indices = mesh.worker_slice(index, count, population)
        seeds = [seed_base + i for i in indices]
        returns_plus = []
        returns_minus = []
        for seed in seeds:
            eps = es.perturbation(seed, theta.size)
            returns_plus.append(episode_return(theta + sigma * eps, sizes, seed))
            returns_minus.append(episode_return(theta - sigma * eps, sizes, seed))

        body = wire.encode(
            {
                "seeds": np.asarray(seeds, dtype=np.int64),
                "returns_plus": np.asarray(returns_plus, dtype=np.float32),
                "returns_minus": np.asarray(returns_minus, dtype=np.float32),
            },
            meta={"generation": generation},
        )
        try:
            post(body)
        except Exception as exc:
            print(
                f"[es-fleet] worker {index}: posting returns for generation "
                f"{generation} failed: {exc}",
                file=sys.stderr,
                flush=True,
            )
            continue  # recompute against whatever generation is current now
        last_generation = generation


# --- Configuration and topology glue -------------------------------------------


def load_settings(env: Mapping[str, str] | None = None, path: str | None = None) -> Config:
    """Layered configuration plus the direct ES_* environment overrides."""
    if env is None:
        env = os.environ
    cfg = load_config(DEFAULTS, path=path, env=env)
    data = cfg.as_dict()
    for variable, (section, key, cast) in _ES_ENV_OVERRIDES.items():
        raw = env.get(variable, "").strip()
        if raw:
            data[section][key] = cast(raw)
    return Config(data)


def resolve_topology(env: Mapping[str, str]) -> tuple[str, int, int]:
    """Resolve ``(coordinator host:port, worker index, node count)``.

    With numeric asset ids the topology is fully deterministic: ids sorted
    ascending, the lowest coordinates, and each node's worker index is its
    rank in that order (the coordinator's loopback worker is rank 0).
    The launcher's generic ``WT_COORDINATOR``, ``WT_NODE_INDEX`` and
    ``WT_NODE_COUNT`` are honored next. Non-numeric setups without either must
    set ``ES_COORDINATOR``, ``ES_WORKER_INDEX``, and ``ES_WORKER_COUNT``
    explicitly; this function never guesses.
    """
    coordinator = env.get("ES_COORDINATOR", "").strip()
    index_raw = env.get("ES_WORKER_INDEX", "").strip()
    count_raw = env.get("ES_WORKER_COUNT", "").strip()
    if coordinator and index_raw and count_raw:
        return coordinator, int(index_raw), int(count_raw)

    # The launcher's generic topology contract: preferred over numeric
    # derivation because it works for hostname peers (the lan transport).
    coordinator = env.get("WT_COORDINATOR", "").strip()
    index_raw = env.get("WT_NODE_INDEX", "").strip()
    count_raw = env.get("WT_NODE_COUNT", "").strip()
    if coordinator and index_raw and count_raw:
        return coordinator, int(index_raw), int(count_raw)

    self_id = env.get("MESH_SELF", "").strip()
    peers_raw = env.get("MESH_PEERS", "")
    port = int(env.get("MESH_PORT", "").strip() or mesh.DEFAULT_MESH_PORT)
    if not self_id.isdigit():
        raise ValueError(
            f"MESH_SELF {self_id!r} is not a numeric asset id; set "
            "ES_COORDINATOR, ES_WORKER_INDEX, and ES_WORKER_COUNT explicitly"
        )
    ids = {int(self_id)}
    targets: dict[int, str] = {}
    for item in peers_raw.split(","):
        item = item.strip()
        if not item:
            continue
        head, _, tail = item.partition(":")
        if not head.isdigit():
            raise ValueError(
                f"MESH_PEERS entry {item!r} is not a numeric asset id; set "
                "ES_COORDINATOR, ES_WORKER_INDEX, and ES_WORKER_COUNT explicitly"
            )
        ids.add(int(head))
        targets[int(head)] = f"device-{head}.cloud.wendy.dev:{tail or port}"
    ordered = sorted(ids)
    coordinator_id = ordered[0]
    coordinator = targets.get(
        coordinator_id, f"device-{coordinator_id}.cloud.wendy.dev:{port}"
    )
    return coordinator, ordered.index(int(self_id)), len(ordered)


def export_artifact(coordinator: Coordinator, run: Run) -> Path:
    """Write the final parameters and a verifiable manifest into the run directory."""
    blob = wire.encode(
        {"theta": coordinator.theta},
        meta={
            "architecture": list(coordinator.hidden),
            "generation": coordinator.generation,
            "algorithm": "evolution-strategies",
        },
    )
    (run.dir / "params.wtw").write_bytes(blob)
    return write_manifest(
        run.dir,
        files=["params.wtw"],
        inputs={"obs": [OBS_DIM]},
        outputs={"action": [ACT_DIM]},
        layout=OBSERVATION_LAYOUT,
        framework="numpy",
        extra={
            "algorithm": "evolution-strategies",
            "architecture": list(coordinator.hidden),
            "generations": coordinator.generation,
        },
    )


def main() -> None:
    # Enumerated ${VAR} passthrough in wendy.json turns unset variables into
    # empty strings; drop those so every default applies as documented.
    env = {k: v for k, v in os.environ.items() if v != ""}
    config_path = env.get("WT_CONFIG")
    if config_path is None:
        baked = Path(__file__).resolve().parent / "config.toml"
        config_path = str(baked) if baked.is_file() else None
    cfg = load_settings(env, path=config_path)
    fleet = mesh.Fleet.from_env(env)

    if fleet.role == "coordinator":
        _, _, count = resolve_topology(env)
        run = Run(fleet.ckpt_dir, fleet.run_id)
        coordinator = Coordinator(cfg, run, n_nodes=count, port=fleet.port)
        if coordinator.resumed:
            adam_t = (
                int(coordinator.adam_state["t"])
                if coordinator.adam_state is not None
                else 0
            )
            print(
                f"resumed generation={coordinator.generation} adam_t={adam_t}",
                flush=True,
            )
        else:
            print(f"starting fresh run id={fleet.run_id}", flush=True)
        coordinator.start()
        print(
            f"coordinator serving on port {coordinator.port} with "
            f"{count} node(s), population {coordinator.population}",
            flush=True,
        )
        coordinator.train()
        manifest_path = export_artifact(coordinator, run)
        print(
            f"training done at generation {coordinator.generation}; "
            f"artifact manifest at {manifest_path}",
            flush=True,
        )
        threading.Event().wait()  # keep /status and /params up for the fleet
    else:
        coordinator_target, index, count = resolve_topology(env)
        print(
            f"worker index={index} count={count} coordinator={coordinator_target}",
            flush=True,
        )
        worker_loop(coordinator_target, index, count)


if __name__ == "__main__":
    main()
