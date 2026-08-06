# Fleet Training Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One-command training on one or many WendyOS devices: a framework-agnostic core library (`wendytrain`), five templates, a fleet launcher, documentation, and hardware verification on three Sparks.

**Architecture:** Layered peel-back model described in `specs/2026-08-06-fleet-training-design.md` (read it first, it is the specification). A pure-NumPy core library owns runs/resume, config, mesh roles, a self-describing wire codec, and artifact manifests. Templates are complete Wendy projects built on it. A launcher computes per-device roles and drives the `wendy` Command Line Interface.

**Tech Stack:** Python 3.11+, NumPy (core's only dependency), PyTorch (ppo-fleet template only, optional), pytest, tomllib (stdlib), the `wendy` CLI, WendyOS mesh entitlements.

## Global Constraints

- No emojis anywhere: code, comments, commits, docs.
- No co-authored-by attribution in commits.
- Abbreviations written in full on first use in every document and docstring.
- No double dashes (--) in prose; use a comma or semicolon.
- `wendytrain` core imports only the standard library and NumPy. Torch may be imported only inside `templates/ppo-fleet/`, lazily.
- Every checkpoint includes optimizer state. Resume restores it. This is the headline defect being fixed; no template may violate it.
- Honest metrics: any update computed from partial fleet results must record `n_contributed` and `population` in the same metrics line.
- Nothing may be Spark-specific or framework-specific in library or launcher code. Device hostnames and asset ids appear only in `fleet.toml` examples and integration tests.
- Follow `Examples/HelloMesh` conventions for `MESH_PEERS`, `MESH_SELF`, `MESH_PORT` parsing semantics exactly (bare id expands to `device-<id>.cloud.wendy.dev:<MESH_PORT>`).
- Environment variable prefix for the library is `WT_`.
- Tests: pytest, files under the package's `tests/`. Run with `python3 -m pytest Training/wendytrain/tests -q`. Commit at the end of every task with a descriptive message.
- All template Dockerfiles base on `python:3.12-slim` (multi-arch; devices are arm64, laptops are arm64/amd64) unless the template needs torch (`ppo-fleet` uses `python:3.12-slim` plus `pip install torch --index-url https://download.pytorch.org/whl/cpu`).

## File Structure

```
Training/
  README.md                          (Task 14)
  wendytrain/
    pyproject.toml                   (Task 1)
    wendytrain/__init__.py           (Task 1)
    wendytrain/wire.py               (Task 2)
    wendytrain/config.py             (Task 3)
    wendytrain/run.py                (Task 4)
    wendytrain/mesh.py               (Task 5)
    wendytrain/manifest.py           (Task 6)
    wendytrain/service.py            (Task 7)
    wendytrain/optim.py              (Task 8)
    wendytrain/es.py                 (Task 8)
    wendytrain/rl.py                 (Task 8)
    tests/test_*.py                  (Tasks 2-8)
  templates/
    single/  (cartpole.py train.py Dockerfile wendy.json config.toml README.md)   (Task 9)
    sweep/   (train.py collect.py Dockerfile wendy.json README.md)                (Task 10)
    es-fleet/ (train.py Dockerfile wendy.json config.toml README.md)              (Task 11)
    ppo-fleet/ (train.py nets.py Dockerfile wendy.json config.toml README.md)     (Task 12)
    byo/     (main.py Dockerfile wendy.json README.md)                            (Task 13)
  launch/
    fleet.py fleet.toml.example tests/test_fleet.py                               (Task 13)
  tests/integration/
    checklist.md run_resume_test.sh run_es_fleet_test.sh run_sweep_test.sh        (Task 15)
```

Stage map: Stage A = Tasks 1-8 (core library, one agent). Stage B = Tasks 9-13
(templates and launcher, four agents in parallel worktrees). Stage C = Task 14
(documentation). Stage D = Task 15 (hardware verification, three Sparks).

---

### Task 1: Package skeleton

**Files:**
- Create: `Training/wendytrain/pyproject.toml`, `Training/wendytrain/wendytrain/__init__.py`, `Training/wendytrain/tests/__init__.py`

**Interfaces:**
- Produces: installable package `wendytrain`, version `0.1.0`, `requires-python >=3.11`, dependency `numpy>=1.24`. `__init__.py` re-exports the public names listed in each later task once they exist.

- [ ] **Step 1:** Write `pyproject.toml` (hatchling or setuptools backend, name `wendytrain`, the constraints above) and empty `__init__.py`.
- [ ] **Step 2:** `python3 -m venv .venv && .venv/bin/pip install -e Training/wendytrain numpy pytest` succeeds; `python -c "import wendytrain"` passes.
- [ ] **Step 3:** Commit: `feat(training): wendytrain package skeleton`.

### Task 2: Wire codec (`wire.py`)

**Files:**
- Create: `Training/wendytrain/wendytrain/wire.py`, `Training/wendytrain/tests/test_wire.py`

**Interfaces:**
- Produces:
  - `encode(arrays: dict[str, np.ndarray], meta: dict | None = None) -> bytes`
  - `decode(blob: bytes) -> tuple[dict[str, np.ndarray], dict]`
  - `MAGIC = b"WTW1"`

Format: `MAGIC + struct.pack("<I", len(header_json)) + header_json + gzip(payload)`.
Header JSON: `{"meta": {...}, "arrays": [{"name", "dtype", "shape", "offset", "nbytes"}, ...]}`.
Payload is the concatenation of each array's `tobytes()` in header order.
`decode` must reject: wrong magic, truncated header, header/payload length
mismatch, unknown dtype. Arrays round-trip exactly (dtype, shape, contents,
including zero-dimensional and empty arrays and `uint8` blobs used to carry
opaque bytes such as serialized torch state).

- [ ] **Step 1:** Failing tests: round-trip of float32/int64/uint8/empty/scalar arrays with nested meta; corrupted-magic raises `ValueError`; truncated blob raises `ValueError`; meta defaults to `{}`.
- [ ] **Step 2:** Run, verify failures. **Step 3:** Implement. **Step 4:** Tests pass. **Step 5:** Commit: `feat(training): self-describing wire codec`.

### Task 3: Layered config (`config.py`)

**Files:**
- Create: `Training/wendytrain/wendytrain/config.py`, `Training/wendytrain/tests/test_config.py`

**Interfaces:**
- Produces:
  - `load_config(defaults: dict, path: str | None = None, env: Mapping[str, str] | None = None, env_prefix: str = "WT_") -> Config`
  - `Config`: attribute and item access, `.as_dict()`.

Layering, later wins: `defaults`, then file at `path` (or `env["WT_CONFIG"]` when
`path` is None): `.toml` via `tomllib`, `.yaml`/`.yml` via PyYAML only if
importable (clear `RuntimeError` naming the missing package otherwise), then
environment overrides `WT_<SECTION>__<KEY>` (double underscore descends one
level; values parsed as TOML literals, falling back to string). Unknown keys in
file or environment that do not exist in `defaults` are allowed (templates may
extend), but type mismatches against a default raise `TypeError`.

- [ ] **Step 1:** Failing tests: env overrides file overrides defaults; `WT_ES__POP=64` sets `cfg.es.pop == 64` as int; bool/float parsing; missing YAML dependency raises with the package name; type mismatch raises.
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Commit: `feat(training): layered config loader`.

### Task 4: Durable runs (`run.py`)

**Files:**
- Create: `Training/wendytrain/wendytrain/run.py`, `Training/wendytrain/tests/test_run.py`

**Interfaces:**
- Produces:
  - `Run(root: str | Path, run_id: str = "default", keep_last: int = 5)` (or `Run.from_env(env)` using `WT_CKPT_DIR`, `WT_RUN_ID`)
  - `.save_checkpoint(arrays: dict[str, np.ndarray], meta: dict, iteration: int) -> Path`
  - `.load_latest() -> tuple[dict[str, np.ndarray], dict, int] | None`
  - `.log_metrics(record: dict) -> None` (appends one JSON line with `time` and `iteration` keys added)
  - `.iteration: int` property (last saved, -1 if none)

Checkpoint file: `step_{iteration:012d}.wtw` written with `wire.encode(arrays, meta | {"iteration": iteration})` to a `.tmp` sibling then `os.replace`d; a `latest` text file containing the filename is replaced after; files beyond `keep_last` pruned oldest-first, never pruning the `latest` target. `load_latest` follows `latest`, and if that file is corrupt or missing falls back to the newest parseable checkpoint (a torn write must cost at most one checkpoint, never the run).

- [ ] **Step 1:** Failing tests: save/load round-trip returns iteration; fresh dir returns None; a second `Run` on the same dir resumes at the saved iteration; corrupt `latest` pointer falls back to newest valid file; pruning keeps exactly `keep_last`; metrics lines are valid JSON with injected keys; save is atomic (no `.tmp` remains).
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Commit: `feat(training): durable runs with atomic checkpoints and resume`.

### Task 5: Mesh and roles (`mesh.py`)

**Files:**
- Create: `Training/wendytrain/wendytrain/mesh.py`, `Training/wendytrain/tests/test_mesh.py`

**Interfaces:**
- Produces:
  - `parse_peers(raw: str, self_id: str = "", default_port: int = 8080) -> list[str]` (HelloMesh semantics: bare id -> `device-<id>.cloud.wendy.dev:<port>`, `id:port`, `host[:port]`; dedupe preserving order; skip self by id)
  - `derive_role(self_id: str, peers_raw: str, explicit: str = "auto") -> str` (returns `explicit` unless `auto`; `auto`: numeric ids sorted ascending, lowest is `"coordinator"`, others `"worker"`; a node absent from the list or non-numeric ids present: raise `ValueError` telling the user to set `WT_ROLE` explicitly, never guess)
  - `worker_slice(index: int, count: int, population: int) -> range` (disjoint, covers `[0, population)`, remainder to the last worker)
  - `Fleet.from_env(env) -> Fleet` with fields `role, self_id, peers, port, run_id, ckpt_dir`
  - `http_get(url, timeout=5.0, retries=5) -> bytes`, `http_post(url, body, timeout=10.0, retries=5) -> bytes` (exponential backoff, `urllib`)

- [ ] **Step 1:** Failing tests: peer parsing matrix (bare, ported, hostname, mixed, dedupe, self-skip); role derivation (lowest id coordinator; explicit wins; ambiguity raises); `worker_slice` covers population exactly for uneven splits (property-style loop over pop 1..50, workers 1..7); Fleet.from_env reads the documented variables with defaults.
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Commit: `feat(training): mesh peers, deterministic roles, worker slicing`.

### Task 6: Artifact manifest (`manifest.py`)

**Files:**
- Create: `Training/wendytrain/wendytrain/manifest.py`, `Training/wendytrain/tests/test_manifest.py`

**Interfaces:**
- Produces:
  - `write_manifest(directory, *, files: list[str | Path], inputs: dict, outputs: dict, layout: str, framework: str, extra: dict | None = None) -> Path` (writes `manifest.json` with per-file SHA-256, sizes, the shape dicts, an ISO timestamp)
  - `verify_manifest(directory) -> None` (raises `ManifestError` naming the first mismatching or missing file)

- [ ] **Step 1:** Failing tests: write then verify passes; flipping one byte in a listed file makes verify raise and the message names the file; missing file named; manifest is deterministic apart from the timestamp.
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Commit: `feat(training): artifact manifest with checksum verification`.

### Task 7: Service helper (`service.py`)

**Files:**
- Create: `Training/wendytrain/wendytrain/service.py`, `Training/wendytrain/tests/test_service.py`

**Interfaces:**
- Produces: `serve(routes: dict[tuple[str, str], Callable[[bytes], tuple[int, bytes, str]]], port: int, host: str = "0.0.0.0") -> ThreadingHTTPServer` (key is `(method, path)`; handler gets the request body, returns status, body, content type; server started on a daemon thread and returned so callers can `.shutdown()`; unknown route 404; handler exception 500 with the message logged, not swallowed).

- [ ] **Step 1:** Failing tests against an ephemeral port (port 0, read back the bound port): GET and POST dispatch, 404, handler exception yields 500 and the server survives.
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Commit: `feat(training): threaded http service helper`.

### Task 8: Algorithm math (`optim.py`, `es.py`, `rl.py`)

**Files:**
- Create: `Training/wendytrain/wendytrain/optim.py`, `es.py`, `rl.py`, plus `tests/test_optim.py`, `tests/test_es.py`, `tests/test_rl.py`

**Interfaces:**
- Produces:
  - `optim.adam_step(theta, grad, state: dict | None, lr=1e-2, b1=0.9, b2=0.999, eps=1e-8, maximize=False) -> tuple[np.ndarray, dict]`; state dict has `m`, `v`, `t` and round-trips through `wire` arrays (`t` as a 0-d int64 array is acceptable; document the convention).
  - `es.perturbation(seed: int, n: int) -> np.ndarray` (`default_rng(seed).standard_normal(n)`, float32)
  - `es.rank_normalize_combined(returns_plus, returns_minus) -> tuple[np.ndarray, np.ndarray]`: rank over the concatenated set, scale to `[-0.5, 0.5]`, split back. This fixes the per-set normalization defect in pull request #1423.
  - `es.gradient(returns_plus, returns_minus, seeds, num_params, sigma) -> np.ndarray` using the combined normalization and mirrored differences.
  - `rl.gae(rewards, values, dones, gamma=0.99, lam=0.95) -> tuple[np.ndarray, np.ndarray]` (advantages, returns; bootstrap 0 after done).

Key test (the regression that pins the fix):

```python
def test_rank_normalization_is_over_the_combined_set():
    rp = np.array([1.0, 2.0], np.float32)
    rm = np.array([100.0, 200.0], np.float32)
    nrp, nrm = rank_normalize_combined(rp, rm)
    # Combined ranks are [0,1,2,3] -> [-0.5,-1/6,1/6,0.5]; per-set
    # normalization would wrongly give both sets the same values.
    assert np.allclose(nrp, [-0.5, -1/6], atol=1e-6)
    assert np.allclose(nrm, [1/6, 0.5], atol=1e-6)

def test_es_gradient_climbs_a_quadratic():
    # theta near 0, f(x) = -|x|^2, ES gradient must point toward 0.
    rng = np.random.default_rng(0)
    theta = rng.standard_normal(8).astype(np.float32)
    seeds = list(range(64)); sigma = 0.1
    rp = [-(np.linalg.norm(theta + sigma * perturbation(s, 8)) ** 2) for s in seeds]
    rm = [-(np.linalg.norm(theta - sigma * perturbation(s, 8)) ** 2) for s in seeds]
    g = gradient(np.array(rp), np.array(rm), seeds, 8, sigma)
    assert np.dot(g, -theta) > 0  # ascent direction reduces |theta|
```

GAE test: three-step hand-computed example asserted to 1e-6, plus `dones` cutting the recursion.

- [ ] **Steps 1-4:** Red, implement, green (include an Adam convergence test on a quadratic, 200 steps, `maximize=False`). **Step 5:** Commit: `feat(training): es, adam, gae math with combined rank normalization`.

### Task 9: `single` template plus built-in environment

**Files:**
- Create: `Training/templates/single/cartpole.py`, `train.py`, `config.toml`, `Dockerfile`, `wendy.json`, `README.md`, `tests/test_single.py`

**Interfaces:**
- Consumes: `wendytrain` (`Run`, `load_config`, `es`, `optim`, `manifest`, `wire`).
- Produces: `cartpole.py` exporting `CartPole(seed)` with `reset(seed=None) -> np.ndarray(4,)`, `step(action: np.ndarray) -> (obs, reward, done, info)`, `obs_dim = 4`, `act_dim = 1`, deterministic under seed, pure NumPy (standard cart-pole dynamics, continuous force in [-1, 1], 500-step limit). Other templates import this file by copy at build time; keep it dependency-free.

`train.py`: loads config (defaults: `es.pop=32, es.sigma=0.1, es.lr=0.02, run.max_iterations=200, policy.hidden=[32]`), builds a small tanh Multi-Layer Perceptron as flat parameters, resumes from `Run.load_latest()` including Adam state, ES-trains locally (a `ProcessPool` of `WT_WORKERS` processes, default `os.cpu_count()`), logs metrics every generation, checkpoints every `run.checkpoint_every` (default 10), writes an artifact manifest on completion. `wendy.json`: single service, entitlements persist (`wt-single-ckpt` at `/data/checkpoints`) and network host is not needed; no gpu, no mesh. Environment passthrough for `WT_*`.

- [ ] **Step 1:** Failing tests: cartpole determinism (same seed, same 10-step trajectory); a 20-generation in-process training run improves mean return over generation 0; kill-resume in-process (train 10 generations, reopen `Run`, assert iteration 10 and Adam `t` restored, train 10 more).
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Build check: `docker build Training/templates/single` succeeds (skip gracefully if no Docker locally; the integration stage covers it). **Step 6:** Commit: `feat(training): single-device template with resume proof`.

### Task 10: `sweep` template

**Files:**
- Create: `Training/templates/sweep/train.py`, `collect.py`, `Dockerfile`, `wendy.json`, `README.md`, `tests/test_sweep.py`

**Interfaces:**
- Consumes: `single`'s trainer logic (import the same `train_loop(cfg, run) -> dict` function; refactor `single/train.py` into `train_loop` plus `main` if Task 9 did not already expose it).
- Produces: each sweep member reads `WT_SWEEP_INDEX` and `WT_SWEEP_PARAMS` (JSON list of parameter dicts baked by the launcher; member picks `params[WT_SWEEP_INDEX]`), merges into config, trains, serves `GET /result` (JSON: run id, params, final mean return, iterations) via `wendytrain.service` on `MESH_PORT`, and keeps serving after finishing. `collect.py`: given `host:port` list, polls `/result` until all respond or timeout, writes `results.json` table sorted by score.

- [ ] **Step 1:** Failing tests: two in-process members with different seeds produce distinct results; `collect` aggregates and sorts; a member that is down leaves a recorded `"unreachable"` row rather than aborting the table.
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Commit: `feat(training): sweep template, fan-out over independent runs`.

### Task 11: `es-fleet` template

**Files:**
- Create: `Training/templates/es-fleet/train.py`, `config.toml`, `Dockerfile`, `wendy.json`, `README.md`, `tests/test_es_fleet.py`

**Interfaces:**
- Consumes: `wendytrain` everything; `cartpole.py` (copied at build; the Dockerfile copies it from `templates/single/` via the build-context rule in Task 13).
- Produces: coordinator serves `GET /params` (wire blob: `theta` plus meta `{generation, seed_base, architecture}`), `POST /returns` (wire blob: `seeds`, `returns_plus`, `returns_minus`, meta `{generation}`), `GET /status` (JSON with generation, mean and best return, `n_contributed`, `population`); workers pull, evaluate their `worker_slice`, post back. Coordinator also runs a loopback worker thread so its own slice is evaluated (defect fixed in the draft: without this every generation waits for the timeout).

Correctness requirements, each with a test:
1. Resume: on start the coordinator loads `Run.load_latest()`; `theta`, Adam `m/v/t`, and generation continue. Kill and restart in-process proves it.
2. Honest partial generations: on `ES_GEN_TIMEOUT_S` the coordinator advances with what arrived, and the metrics line and `/status` carry `n_contributed < population`.
3. Combined rank normalization via `wendytrain.es.gradient` only (no local reimplementation).
4. Architecture travels in `meta["architecture"]` (hidden sizes list); workers construct the policy from it, never inferred from parameter counts.
5. Stale posts (`generation != current`) are dropped and counted in `/status` as `stale_posts`.

`wendy.json`: mesh entitlement (`serviceCIDR 10.99.0.0/16`, port 8080), persist `wt-es-ckpt` at `/data/checkpoints`, optional gpu, `isolation: "isolated"`, env passthrough `MESH_*`, `WT_*`, `ES_*`.

- [ ] **Step 1:** Failing tests: end-to-end in-process (coordinator thread plus two worker threads on 127.0.0.1 ephemeral ports, 5 generations, all slices contributed, mean return finite and generally rising on cartpole); restart-resume test; timeout partial-generation test with a deliberately dead worker (metrics record `n_contributed`); stale post dropped.
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Commit: `feat(training): es-fleet template, resume and honest partial generations`.

### Task 12: `ppo-fleet` template

**Files:**
- Create: `Training/templates/ppo-fleet/train.py`, `nets.py`, `config.toml`, `Dockerfile`, `wendy.json`, `README.md`, `tests/test_ppo_fleet.py`

**Interfaces:**
- Consumes: `wendytrain` (`Run`, `wire`, `mesh`, `service`, `rl.gae`), torch (lazy import with an actionable error if absent), `cartpole.py`.
- Produces: learner serves `GET /weights` (wire: flat policy and value parameters, meta `{version, architecture, log_std}`), `POST /rollout` (wire: obs, actions, logprobs, rewards, dones, values, meta `{weights_version}`); actors pull weights, collect `PPO_STEPS` steps, post. Learner updates with clipped-surrogate PPO plus GAE from `wendytrain.rl`, entropy bonus, minibatch epochs; rejects rollouts older than `PPO_MAX_STALENESS` versions (counted in `/status`, not silently dropped). Checkpoint carries policy, value net, and the torch optimizer `state_dict` serialized with `torch.save` into a `uint8` wire array; resume restores all three.

- [ ] **Step 1:** Failing tests (skip cleanly with a message if torch missing; torch is in the dev requirements so they run in Continuous Integration and locally): learner plus one in-process actor improves cartpole return over 15 versions; resume restores optimizer state (`state_dict` step counts equal after reload); staleness rejection counted.
- [ ] **Steps 2-4:** Red, implement, green. **Step 5:** Commit: `feat(training): ppo-fleet template with durable optimizer state`.

### Task 13: `byo` template and the fleet launcher

**Files:**
- Create: `Training/templates/byo/main.py`, `Dockerfile`, `wendy.json`, `README.md`
- Create: `Training/launch/fleet.py`, `Training/launch/fleet.toml.example`, `Training/launch/tests/test_fleet.py`

**Interfaces:**
- Consumes: the `wendy` CLI (`wendy --device <host> run --deploy -y`, `--dockerfile`), templates' `wendy.json` env passthrough.
- Produces:
  - `byo/main.py`: fifty lines. Prints the resolved contract (role, peers, run dir), serves `/healthz`, and documents in its README how to wire `torch.distributed` (TCPStore rendezvous on the coordinator's mesh address) or any other stack on top of nothing but the layer-0 contract.
  - `fleet.py` subcommands: `up`, `status`, `logs`, `down`, `render`. Reads `fleet.toml`:

```toml
[fleet]
template = "es-fleet"            # or a path to any directory with a wendy.json
devices = ["spark-3011.local", "spark-48fd.local", "spark-edeb.local"]
# asset ids resolved via `wendy cloud device list --json`; overridable per device:
# devices = [{ host = "spark-3011.local", asset_id = 334, role = "coordinator" }]

[env]                            # forwarded to every device
WT_RUN_ID = "demo-1"
ES_POP = "64"

[sweep]                          # only read by the sweep template
params = [{ seed = 1 }, { seed = 2 }, { seed = 3 }]
```

  - `up`: resolve asset ids (CLI query, cached in `.fleet-state.json`), compute per-device `MESH_SELF`, `MESH_PEERS` (all ids), `WT_ROLE` (auto rule from `wendytrain.mesh.derive_role`, or per-device override), `WT_SWEEP_INDEX`/`WT_SWEEP_PARAMS` for sweeps, then run the deploy command per device sequentially with the env exported. `render`: print the exact commands and environments without executing (this is what unit tests assert against, and what a cautious user reads first). `status`: poll each device's `/status` or `/healthz` and print a table. `down`: stop only containers whose app id matches the template's `wendy.json` (never anything else on the device).
  - Build-context rule: the launcher stages a build context in a temp dir: the template directory, plus `Training/wendytrain/wendytrain/` copied to `wendytrain/`, plus `templates/single/cartpole.py` when the template's Dockerfile references it. It writes a `stage-manifest.json` of SHA-256 sums into the context; template Dockerfiles copy everything with plain `COPY . /app/`. One library copy in git; vendoring is a launch-time build step verified by checksums, never a committed copy.

- [ ] **Step 1:** Failing tests for `render`: given a fixture `fleet.toml` with three fake devices and stubbed asset ids, the emitted plan assigns exactly one coordinator (lowest id), passes complete peer lists including self, forwards `[env]`, produces the sweep index/params only for the sweep template, and the staged context contains `wendytrain/` with checksums matching the source tree.
- [ ] **Steps 2-4:** Red, implement, green (subprocess calls behind an injectable runner so tests never touch the CLI). **Step 5:** Commit: `feat(training): byo template and one-command fleet launcher`.

### Task 14: Documentation

**Files:**
- Create: `Training/README.md`
- Modify: root `README.md` (one line in the repository map pointing at `Training/`, matching its existing tone)

**Interfaces:**
- Consumes: everything above; the design document.

`Training/README.md` sections, each concrete with copy-paste commands: What this is (three sentences); Quickstart single device (three commands); Quickstart fleet (`fleet.toml` plus `fleet.py up`, expected output shown); The layers, with a table of what you keep when you peel each one off; The environment variable contract (the table from the design document); Topology guidance including when not to distribute, with the honest G1 numbers; Framework notes: PyTorch, JAX, TensorFlow each get a paragraph stating exactly which layer they plug into (all of them: layer 0 always works; layer 1 works for any of them since checkpoints are named arrays plus a bytes blob; `rl.gae`/`es` are NumPy and framework-neutral); Checkpoint durability contract; Wire format specification (copyable into another language); Hardware notes (any WendyOS device; entitlements table; gpu optional).

- [ ] **Step 1:** Write it. **Step 2:** Every command in the doc is executed once locally where executable (config render, launcher `render`, pytest) and outputs pasted truthfully. **Step 3:** Commit: `docs(training): fleet training guide`.

### Task 15: Hardware verification (three Sparks)

**Files:**
- Create: `Training/tests/integration/checklist.md`, `run_resume_test.sh`, `run_es_fleet_test.sh`, `run_sweep_test.sh`

Constraints: use only `spark-3011.local` (334), `spark-48fd.local` (211), `spark-edeb.local` (283). Record `wendy` container lists before and after per device; the diff may contain only apps deployed by these tests. Never stop or remove any pre-existing application.

- [ ] **Step 1:** Resume proof on spark-edeb: `single` template, let it reach iteration >= 20, `container stop`, `container start`, assert from logs that the first line after restart reports `resumed iteration=<n>` with n >= 20 and Adam `t` matching; then let it finish and pull `manifest.json`, verify checksums locally.
- [ ] **Step 2:** Fleet proof: `fleet.py up` with es-fleet on all three; run >= 10 generations; assert via `/status` that `n_contributed == population` for at least 8 of 10 and every asset id appears in the coordinator's contribution log; `fleet.py down`; confirm non-interference diff.
- [ ] **Step 3:** Fan-out proof: sweep with three seeds, one per device; `collect.py` gathers three rows; results table committed under `Training/tests/integration/results/`.
- [ ] **Step 4:** Write actual outcomes (including failures and their fixes) into `checklist.md`. Honest reporting: what ran, what did not, with logs.
- [ ] **Step 5:** Commit: `test(training): hardware verification on three sparks`.

---

## Self-Review

- Spec coverage: one or multiple devices (Tasks 9, 11, 12, 13, 15); mesh communication (Tasks 5, 11, 12); one click via wendy.json plus env (Tasks 11-13); established config formats and templates (Tasks 3, 9-13); seasoned-engineer freedom and peel-back layering (byo template, layer model, Task 14); documentation (Task 14); no vendor or framework specificity (global constraints; framework notes in Task 14). Covered.
- Known decision point deferred to Task 13 deliberately: whether `wendy.json` build contexts may reference parent directories. The launcher's staged-context rule works either way; if parent contexts are supported the staging step becomes a no-op optimization. The Task 13 agent must check `go/` build-context handling in this repository and use the simpler mechanism if it exists.
- Type consistency: `wire.encode/decode`, `Run.save_checkpoint/load_latest`, `derive_role`, `worker_slice`, `es.gradient` signatures are referenced identically across Tasks 8-13.
