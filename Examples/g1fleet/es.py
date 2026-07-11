from __future__ import annotations
import os, threading, time, json
import numpy as np
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from .mesh import MeshConfig, worker_index, http_get, http_post
from .netcodec import encode_named, decode_named
from .rollout import CPUBackend
from .policy import MLPPolicy


def _rank_normalize(x: np.ndarray) -> np.ndarray:
    order = np.argsort(np.argsort(x))
    r = order.astype(np.float32) / (len(x) - 1 + 1e-9) - 0.5
    return r


def es_gradient(returns_plus, returns_minus, seeds, num_params, sigma) -> np.ndarray:
    rp = _rank_normalize(np.asarray(returns_plus, np.float32))
    rm = _rank_normalize(np.asarray(returns_minus, np.float32))
    g = np.zeros(num_params, np.float32)
    for k, s in enumerate(seeds):
        eps = np.random.default_rng(int(s)).standard_normal(num_params).astype(np.float32)
        g += (rp[k] - rm[k]) * eps
    return g / (len(seeds) * sigma + 1e-9)


def adam_step(theta, grad, state, lr=0.02, b1=0.9, b2=0.999, eps=1e-8):
    theta = np.asarray(theta, np.float32)
    if state is None:
        state = {"m": np.zeros_like(theta), "v": np.zeros_like(theta), "t": 0}
    state["t"] += 1
    state["m"] = b1 * state["m"] + (1 - b1) * grad
    state["v"] = b2 * state["v"] + (1 - b2) * grad * grad
    mhat = state["m"] / (1 - b1 ** state["t"]); vhat = state["v"] / (1 - b2 ** state["t"])
    theta = theta + lr * mhat / (np.sqrt(vhat) + eps)   # + => gradient ASCENT
    return theta.astype(np.float32), state


def _hidden_from_env(default=(256, 256)):
    raw = os.environ.get("POLICY_HIDDEN", "")
    if not raw.strip():
        return tuple(default)
    return tuple(int(x) for x in raw.split(",") if x.strip())


def _env_int(name, default):
    return int(os.environ.get(name, default))


def _env_float(name, default):
    return float(os.environ.get(name, default))


# ------------------------------- coordinator --------------------------------

def run_coordinator(cfg: MeshConfig, obs_dim: int, act_dim: int) -> None:
    """Serves /params (GET), /returns (POST), /status (GET). Aggregates mirrored
    ES returns from workers per generation, applies the ES gradient + Adam step,
    checkpoints theta.npy, and advances the generation. A dead/slow peer never
    blocks progress forever: a generation is force-advanced with whatever
    returns arrived once ES_GEN_TIMEOUT_S elapses (logged, not fatal)."""
    hidden = _hidden_from_env()
    pop = _env_int("ES_POP", 60)
    sigma = _env_float("ES_SIGMA", 0.05)
    lr = _env_float("ES_LR", 0.02)
    gen_timeout = _env_float("ES_GEN_TIMEOUT_S", 30)

    policy = MLPPolicy(obs_dim, act_dim, hidden=hidden)
    lock = threading.Lock()
    state = {
        "generation": 0,
        "theta": policy.get_flat(),
        "adam": None,
        "mean_return": float("nan"),
        "best_return": float("-inf"),
        "returns_plus": {},
        "returns_minus": {},
        "gen_start": time.time(),
    }

    def _checkpoint(theta: np.ndarray) -> None:
        try:
            os.makedirs(cfg.ckpt_dir, exist_ok=True)
            np.save(os.path.join(cfg.ckpt_dir, "theta.npy"), theta)
        except OSError as e:
            print(f"[g1fleet-es] WARN checkpoint failed: {e}", flush=True)

    def _advance_locked() -> None:
        """Caller must hold lock."""
        seeds_sorted = sorted(state["returns_plus"].keys())
        if not seeds_sorted:
            return
        rp = np.array([state["returns_plus"][s] for s in seeds_sorted], np.float32)
        rm = np.array([state["returns_minus"][s] for s in seeds_sorted], np.float32)
        g = es_gradient(rp, rm, seeds_sorted, state["theta"].size, sigma)
        new_theta, adam_state = adam_step(state["theta"], g, state["adam"], lr=lr)
        all_returns = np.concatenate([rp, rm])
        mean_return = float(np.mean(all_returns))
        best_return = float(max(state["best_return"], float(np.max(all_returns))))
        state["theta"] = new_theta
        state["adam"] = adam_state
        state["mean_return"] = mean_return
        state["best_return"] = best_return
        state["generation"] += 1
        state["returns_plus"] = {}
        state["returns_minus"] = {}
        state["gen_start"] = time.time()
        _checkpoint(new_theta)
        print(f"[g1fleet-es] gen={state['generation']} mean_return={mean_return:.4f} "
              f"best={best_return:.4f} n_seeds={len(seeds_sorted)}/{pop}", flush=True)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            pass  # quiet; app-level logging happens in _advance_locked

        def do_GET(self):
            if self.path == "/params":
                with lock:
                    body = encode_named({
                        "theta": state["theta"],
                        "generation": np.array([state["generation"]], dtype=np.int64),
                        "seed_base": np.array([state["generation"] * pop], dtype=np.int64),
                    })
                self.send_response(200)
                self.send_header("Content-Type", "application/octet-stream")
                self.end_headers()
                self.wfile.write(body)
            elif self.path == "/status":
                with lock:
                    payload = json.dumps({
                        "generation": state["generation"],
                        "mean_return": state["mean_return"],
                        "best_return": state["best_return"],
                    }).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(payload)
            else:
                self.send_response(404); self.end_headers()

        def do_POST(self):
            if self.path == "/returns":
                length = int(self.headers.get("Content-Length", 0))
                body = self.rfile.read(length)
                try:
                    d = decode_named(body)
                    gen = int(d["generation"][0])
                    seeds = [int(s) for s in d["seeds"]]
                    rp = d["returns_plus"]; rm = d["returns_minus"]
                    with lock:
                        if gen == state["generation"]:
                            for s, p, m in zip(seeds, rp, rm):
                                state["returns_plus"][s] = float(p)
                                state["returns_minus"][s] = float(m)
                            if len(state["returns_plus"]) >= pop:
                                _advance_locked()
                        # else: stale post from a previous generation; drop silently.
                    self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
                except Exception as e:
                    print(f"[g1fleet-es] WARN bad /returns payload: {e}", flush=True)
                    self.send_response(400); self.end_headers()
            else:
                self.send_response(404); self.end_headers()

    def _ticker():
        # Forces progress if some workers never report (dead peer tolerance).
        while True:
            time.sleep(1.0)
            with lock:
                stale = (time.time() - state["gen_start"]) > gen_timeout and state["returns_plus"]
                if stale:
                    print(f"[g1fleet-es] WARN gen={state['generation']} timed out with "
                          f"{len(state['returns_plus'])}/{pop} returns; force-advancing", flush=True)
                    _advance_locked()

    threading.Thread(target=_ticker, daemon=True).start()
    httpd = ThreadingHTTPServer(("0.0.0.0", cfg.port), Handler)
    httpd.serve_forever()


# ---------------------------------- worker -----------------------------------

def run_worker(cfg: MeshConfig, obs_dim: int, act_dim: int) -> None:
    """Pulls params, evaluates this worker's disjoint slice of the mirrored ES
    population via CPUBackend, and posts returns back to the coordinator."""
    hidden = _hidden_from_env()
    pop = _env_int("ES_POP", 60)
    sigma = _env_float("ES_SIGMA", 0.05)
    proc_workers = _env_int("ES_WORKERS", min(4, os.cpu_count() or 2))

    if not cfg.peers:
        print("[g1fleet-es] worker has no peers configured; nothing to do", flush=True)
        return
    coord = cfg.peers[0]
    coord_url = f"http://{coord}"

    raw_peers = os.environ.get("MESH_PEERS", "")
    idx, count = worker_index(cfg.self_id, raw_peers)

    backend = CPUBackend(obs_dim, act_dim, workers=proc_workers, hidden=hidden)
    last_gen = -1

    while True:
        try:
            body = http_get(f"{coord_url}/params")
        except Exception as e:
            print(f"[g1fleet-es] WARN coordinator unreachable: {e}", flush=True)
            time.sleep(2); continue

        d = decode_named(body)
        theta = np.asarray(d["theta"], np.float32)
        gen = int(d["generation"][0])
        seed_base = int(d["seed_base"][0])

        if gen == last_gen:
            time.sleep(0.5); continue

        per = max(1, pop // max(count, 1))
        lo = idx * per
        hi = pop if idx >= count - 1 else (idx + 1) * per
        lo = min(lo, pop); hi = min(max(hi, lo), pop)
        seeds = list(range(seed_base + lo, seed_base + hi))
        if not seeds:
            seeds = [seed_base + (idx % max(pop, 1))]

        plus_vecs, minus_vecs = [], []
        for s in seeds:
            eps = np.random.default_rng(s).standard_normal(theta.size).astype(np.float32)
            plus_vecs.append(theta + sigma * eps)
            minus_vecs.append(theta - sigma * eps)

        try:
            returns_plus = backend.evaluate_returns(plus_vecs, seeds)
            returns_minus = backend.evaluate_returns(minus_vecs, seeds)
            payload = encode_named({
                "generation": np.array([gen], dtype=np.int64),
                "seeds": np.array(seeds, dtype=np.int64),
                "returns_plus": returns_plus.astype(np.float32),
                "returns_minus": returns_minus.astype(np.float32),
            })
            http_post(f"{coord_url}/returns", payload)
        except Exception as e:
            print(f"[g1fleet-es] WARN failed to evaluate/post generation {gen}: {e}", flush=True)

        last_gen = gen
