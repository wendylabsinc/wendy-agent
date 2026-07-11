# Distributed G1 Locomotion RL on a WendyOS Mesh Fleet — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship two WendyOS example apps — `G1FleetES` (Evolution Strategies) and `G1FleetPPO` (actor–learner PPO) — that train a Unitree G1 velocity-tracking policy with genuinely distributed compute across three Spark devices connected over the WendyOS mesh.

**Architecture:** A canonical shared Python package `g1fleet/` (env, policy, rollout backend, mesh wiring, wire codec, ES + PPO drivers) is developed and unit-tested locally on the CPU backend, then vendored (copied) into each app's build context. Each app's `app.py` dispatches on a `ROLE` env var. Devices discover each other at `device-<id>.cloud.wendy.dev:8080` via the `network mode:"mesh"` entitlement under `isolation:"isolated"`.

**Tech Stack:** Python 3.11, `mujoco` (C engine, CPU), `numpy`, `torch` (CPU, PPO learner only), `robot_descriptions` (bakes the G1 MJCF into the image), stdlib `http.server`/`urllib` for the mesh RPC, `pytest` for tests. Optional stretch: `jax`/`mujoco-mjx` GPU backend.

## Global Constraints

- **Python 3.11**, target arch **arm64** (Spark fleet); images must build for arm64.
- **No dependency on `wendymujoco`** — that module belongs to the Wendy Sim runtime, not headless training containers. Use plain `mujoco`.
- **Self-contained, offline-runnable images** — the G1 model is baked in at build time; no runtime downloads.
- **Shared core is vendored by copy** into each app dir (`scripts/sync_g1fleet.sh`); `Examples/g1fleet/` is the source of truth and the only place tests import from.
- **Mesh addressing:** bare asset id → `device-<id>.cloud.wendy.dev:<port>`; default port **8080**; `serviceCIDR` **10.99.0.0/16**; requires `isolation:"isolated"` + `network mode:"mesh"`.
- **Fleet:** Spark1=**284**, Spark2=**283**, Spark3=**211**; deploy with `wendy cloud run --device <id>`.
- **Entitlement keys (verified against `go/internal/shared/appconfig/appconfig.go`):** `network` (with `mode`,`serviceCIDR`,`ports`), `gpu`, `persist` (`type`,`name`,`path`).
- **Default `SIM_BACKEND=cpu`.** The `mjx` backend is a stretch and must never be on the critical path; if selected and it fails to init, exit non-zero (no silent fallback).
- **Checkpoint mount:** persist volume `checkpoints` at container path `/data/checkpoints`; code reads `CKPT_DIR` env, default `/data/checkpoints`.
- Every cross-device HTTP call uses bounded retry with backoff; a dead peer is logged and skipped, never fatal.

## File Structure

```
Examples/g1fleet/                 # canonical shared core (source of truth; tests import here)
  __init__.py
  g1env.py                        # G1 locomotion env (CPU mujoco)
  policy.py                       # MLP: numpy forward + flat param vector; torch mirror
  rollout.py                      # Backend interface + CPUBackend (+ MJXBackend stub)
  netcodec.py                     # length-prefixed gzip numpy transport
  mesh.py                         # role/peer wiring + HTTP client helpers
  es.py                           # ES coordinator + worker drivers
  ppo.py                          # PPO learner + actor drivers
  tests/
    __init__.py
    test_policy.py
    test_netcodec.py
    test_es_math.py
    test_mesh.py
    test_g1env.py
Examples/G1FleetES/
  wendy.json  Dockerfile  requirements.txt  app.py  g1fleet/   # g1fleet/ = synced copy
Examples/G1FleetPPO/
  wendy.json  Dockerfile  requirements.txt  app.py  g1fleet/   # g1fleet/ = synced copy
Examples/README-G1Fleet.md        # how to build/deploy/verify
scripts/sync_g1fleet.sh           # copies Examples/g1fleet -> each app dir
```

**Local dev environment:** a venv at `Examples/g1fleet/.venv` with `mujoco numpy torch robot_descriptions pytest`. All `pytest` commands below run from `Examples/g1fleet/` with that venv active.

---

### Task 1: Scaffold shared package + dev venv

**Files:**
- Create: `Examples/g1fleet/__init__.py` (empty), `Examples/g1fleet/tests/__init__.py` (empty)
- Create: `Examples/g1fleet/requirements-dev.txt`
- Create: `Examples/g1fleet/pytest.ini`

- [ ] **Step 1: Create package dirs and files**

`Examples/g1fleet/requirements-dev.txt`:
```
numpy
mujoco
torch
robot_descriptions
pytest
```

`Examples/g1fleet/pytest.ini`:
```ini
[pytest]
testpaths = tests
addopts = -q
```

- [ ] **Step 2: Create venv and install deps**

Run:
```bash
cd Examples/g1fleet && python3.11 -m venv .venv && . .venv/bin/activate && pip install -r requirements-dev.txt
```
Expected: installs succeed; `python -c "import mujoco, torch, numpy"` prints nothing (exit 0).

- [ ] **Step 3: Prefetch the G1 model (proves model sourcing works)**

Run:
```bash
cd Examples/g1fleet && . .venv/bin/activate && python -c "from robot_descriptions import g1_mj_description as g; print(g.MJCF_PATH)"
```
Expected: prints an absolute path ending in a `.xml` MJCF file (downloaded + cached on first run).

- [ ] **Step 4: Commit**

```bash
git add Examples/g1fleet/__init__.py Examples/g1fleet/tests/__init__.py Examples/g1fleet/requirements-dev.txt Examples/g1fleet/pytest.ini
git commit -m "feat(g1fleet): scaffold shared package and dev env"
```

---

### Task 2: `policy.py` — MLP with flat params (numpy + torch)

**Files:**
- Create: `Examples/g1fleet/policy.py`
- Test: `Examples/g1fleet/tests/test_policy.py`

**Interfaces:**
- Produces:
  - `class MLPPolicy(obs_dim:int, act_dim:int, hidden=(256,256))`
  - `MLPPolicy.act(obs: np.ndarray) -> np.ndarray` (numpy forward, tanh output in [-1,1])
  - `MLPPolicy.get_flat() -> np.ndarray` (1-D float32)
  - `MLPPolicy.set_flat(v: np.ndarray) -> None`
  - `MLPPolicy.num_params() -> int`
  - `class TorchMLP(obs_dim, act_dim, hidden=(256,256))` — `torch.nn.Module`, same shapes; `get_flat()/set_flat()` interop with `MLPPolicy` layout.

- [ ] **Step 1: Write the failing test**

`Examples/g1fleet/tests/test_policy.py`:
```python
import numpy as np
from g1fleet.policy import MLPPolicy, TorchMLP

def test_flat_roundtrip_is_identity():
    p = MLPPolicy(obs_dim=10, act_dim=4, hidden=(8, 8))
    v = p.get_flat()
    assert v.dtype == np.float32 and v.ndim == 1 and v.size == p.num_params()
    v2 = v.copy(); v2[:] = np.arange(v.size, dtype=np.float32)
    p.set_flat(v2)
    assert np.array_equal(p.get_flat(), v2)

def test_act_shape_and_bounds():
    p = MLPPolicy(obs_dim=10, act_dim=4, hidden=(8, 8))
    a = p.act(np.zeros(10, dtype=np.float32))
    assert a.shape == (4,) and np.all(np.abs(a) <= 1.0 + 1e-6)

def test_numpy_and_torch_agree():
    p = MLPPolicy(obs_dim=6, act_dim=3, hidden=(8, 8))
    t = TorchMLP(obs_dim=6, act_dim=3, hidden=(8, 8))
    t.set_flat(p.get_flat())
    obs = np.random.default_rng(0).standard_normal(6).astype(np.float32)
    import torch
    with torch.no_grad():
        ta = t(torch.from_numpy(obs)[None]).numpy()[0]
    assert np.allclose(p.act(obs), ta, atol=1e-5)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd Examples/g1fleet && . .venv/bin/activate && pytest tests/test_policy.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'g1fleet.policy'` (run with `PYTHONPATH=..` or install `-e`; add `conftest.py` per next step).

- [ ] **Step 3: Make the package importable + implement**

Create `Examples/g1fleet/tests/conftest.py`:
```python
import os, sys
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.dirname(__file__))))
```

`Examples/g1fleet/policy.py`:
```python
"""Small MLP policy with a flat parameter vector for ES/PPO interop."""
from __future__ import annotations
import numpy as np


def _layer_shapes(obs_dim, act_dim, hidden):
    dims = [obs_dim, *hidden, act_dim]
    return [(dims[i], dims[i + 1]) for i in range(len(dims) - 1)]


class MLPPolicy:
    def __init__(self, obs_dim: int, act_dim: int, hidden=(256, 256), seed: int = 0):
        self.obs_dim, self.act_dim, self.hidden = obs_dim, act_dim, tuple(hidden)
        self._shapes = _layer_shapes(obs_dim, act_dim, self.hidden)
        rng = np.random.default_rng(seed)
        self.W, self.b = [], []
        for nin, nout in self._shapes:
            # Xavier-ish init; small so the initial policy is gentle.
            self.W.append((rng.standard_normal((nin, nout)) * (1.0 / np.sqrt(nin))).astype(np.float32))
            self.b.append(np.zeros(nout, dtype=np.float32))

    def num_params(self) -> int:
        return int(sum(w.size + b.size for w, b in zip(self.W, self.b)))

    def get_flat(self) -> np.ndarray:
        parts = []
        for w, b in zip(self.W, self.b):
            parts.append(w.ravel()); parts.append(b.ravel())
        return np.concatenate(parts).astype(np.float32)

    def set_flat(self, v: np.ndarray) -> None:
        v = np.asarray(v, dtype=np.float32); i = 0
        for k, (w, b) in enumerate(zip(self.W, self.b)):
            n = w.size; self.W[k] = v[i:i + n].reshape(w.shape); i += n
            n = b.size; self.b[k] = v[i:i + n].reshape(b.shape); i += n

    def act(self, obs: np.ndarray) -> np.ndarray:
        x = np.asarray(obs, dtype=np.float32)
        for k in range(len(self.W) - 1):
            x = np.tanh(x @ self.W[k] + self.b[k])
        x = x @ self.W[-1] + self.b[-1]
        return np.tanh(x).astype(np.float32)


class TorchMLP:
    """Torch mirror; imported lazily so ES workers never need torch."""
    def __init__(self, obs_dim: int, act_dim: int, hidden=(256, 256)):
        import torch, torch.nn as nn
        self._torch = torch
        dims = [obs_dim, *hidden, act_dim]
        layers = []
        for i in range(len(dims) - 1):
            layers.append(nn.Linear(dims[i], dims[i + 1]))
            if i < len(dims) - 2:
                layers.append(nn.Tanh())
        layers.append(nn.Tanh())
        self.net = nn.Sequential(*layers)
        # match MLPPolicy layout: zero biases, scaled weights
        with torch.no_grad():
            for m in self.net:
                if isinstance(m, nn.Linear):
                    nn.init.normal_(m.weight, std=1.0 / (m.in_features ** 0.5))
                    nn.init.zeros_(m.bias)

    def __call__(self, x):
        return self.net(x)

    def parameters(self):
        return self.net.parameters()

    def get_flat(self):
        t = self._torch
        return t.cat([p.detach().reshape(-1) for p in self._ordered()]).cpu().numpy().astype("float32")

    def set_flat(self, v):
        t = self._torch; v = t.as_tensor(v, dtype=t.float32); i = 0
        with t.no_grad():
            for p in self._ordered():
                n = p.numel(); p.copy_(v[i:i + n].reshape(p.shape)); i += n

    def _ordered(self):
        # (weight, bias) per Linear, in declaration order — matches MLPPolicy flat layout
        params = []
        for m in self.net:
            if hasattr(m, "weight"):
                params.append(m.weight); params.append(m.bias)
        return params
```

Note: `MLPPolicy` stores weight matrices as `(nin,nout)`; `torch.nn.Linear.weight` is `(nout,nin)`. Fix interop in `TorchMLP.set_flat/get_flat` by transposing weight blocks. Implement `_ordered` to yield the transposed view so the flat layout matches. (Concretely: when copying a weight block of a Linear, reshape the flat slice as `(nin,nout)` then `.T` before `copy_`; and in `get_flat`, emit `weight.T.reshape(-1)`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd Examples/g1fleet && . .venv/bin/activate && pytest tests/test_policy.py -v`
Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add Examples/g1fleet/policy.py Examples/g1fleet/tests/test_policy.py Examples/g1fleet/tests/conftest.py
git commit -m "feat(g1fleet): MLP policy with flat params + torch mirror"
```

---

### Task 3: `netcodec.py` — numpy wire transport

**Files:**
- Create: `Examples/g1fleet/netcodec.py`
- Test: `Examples/g1fleet/tests/test_netcodec.py`

**Interfaces:**
- Produces:
  - `encode_array(a: np.ndarray) -> bytes` / `decode_array(b: bytes) -> np.ndarray`
  - `encode_named(d: dict[str, np.ndarray]) -> bytes` / `decode_named(b: bytes) -> dict[str, np.ndarray]`

- [ ] **Step 1: Write the failing test**

`Examples/g1fleet/tests/test_netcodec.py`:
```python
import numpy as np
from g1fleet.netcodec import encode_array, decode_array, encode_named, decode_named

def test_array_roundtrip_preserves_shape_dtype():
    a = np.random.default_rng(0).standard_normal((3, 5)).astype(np.float32)
    b = decode_array(encode_array(a))
    assert b.dtype == a.dtype and b.shape == a.shape and np.array_equal(a, b)

def test_named_roundtrip():
    d = {"obs": np.zeros((2, 4), np.float32), "rew": np.arange(2, dtype=np.float32)}
    out = decode_named(encode_named(d))
    assert set(out) == set(d)
    for k in d:
        assert np.array_equal(out[k], d[k]) and out[k].dtype == d[k].dtype
```

- [ ] **Step 2: Run to verify fail** — `pytest tests/test_netcodec.py -v` → FAIL (module missing).

- [ ] **Step 3: Implement**

`Examples/g1fleet/netcodec.py`:
```python
"""Gzip-compressed numpy transport for mesh HTTP bodies (uses np.savez)."""
from __future__ import annotations
import gzip, io
import numpy as np


def encode_named(d: dict[str, np.ndarray]) -> bytes:
    buf = io.BytesIO()
    np.savez(buf, **{k: np.ascontiguousarray(v) for k, v in d.items()})
    return gzip.compress(buf.getvalue())


def decode_named(b: bytes) -> dict[str, np.ndarray]:
    with np.load(io.BytesIO(gzip.decompress(b))) as z:
        return {k: z[k] for k in z.files}


def encode_array(a: np.ndarray) -> bytes:
    return encode_named({"_": a})


def decode_array(b: bytes) -> np.ndarray:
    return decode_named(b)["_"]
```

- [ ] **Step 4: Run to verify pass** — `pytest tests/test_netcodec.py -v` → 2 passed.

- [ ] **Step 5: Commit**
```bash
git add Examples/g1fleet/netcodec.py Examples/g1fleet/tests/test_netcodec.py
git commit -m "feat(g1fleet): gzip numpy wire codec"
```

---

### Task 4: `mesh.py` — role/peer wiring + HTTP helpers

**Files:**
- Create: `Examples/g1fleet/mesh.py`
- Test: `Examples/g1fleet/tests/test_mesh.py`

**Interfaces:**
- Produces:
  - `parse_peers(raw:str, self_id:str, default_port:int=8080) -> list[str]` (returns `host:port` targets, skips self, dedupes, preserves order; bare digits → `device-<id>.cloud.wendy.dev:<port>`)
  - `worker_index(self_id:str, peers_raw:str) -> tuple[int,int]` → `(index, count)` of this device among the sorted unique fleet ids (used for ES slice assignment)
  - `MeshConfig.from_env() -> MeshConfig` with `.role, .self_id, .learner_id, .peers, .port, .backend, .ckpt_dir`
  - `http_get(url, timeout=5, retries=5) -> bytes` / `http_post(url, body:bytes, timeout=10, retries=5) -> bytes` (backoff on failure; raises after retries)

- [ ] **Step 1: Write the failing test** (pure functions only — HTTP helpers are smoke-tested in Task 9)

`Examples/g1fleet/tests/test_mesh.py`:
```python
from g1fleet.mesh import parse_peers, worker_index

def test_parse_peers_expands_ids_and_skips_self():
    t = parse_peers("284,283,211", self_id="283")
    assert t == ["device-284.cloud.wendy.dev:8080", "device-211.cloud.wendy.dev:8080"]

def test_parse_peers_accepts_hostports_and_dedupes():
    t = parse_peers("h:9,h:9,284:7", self_id="")
    assert t == ["h:9", "device-284.cloud.wendy.dev:7"]

def test_worker_index_is_stable_and_covers_fleet():
    assert worker_index("284", "284,283,211") == (2, 3)   # sorted ids: 211,283,284
    assert worker_index("211", "284,283,211") == (0, 3)
```

- [ ] **Step 2: Run to verify fail** — FAIL (module missing).

- [ ] **Step 3: Implement**

`Examples/g1fleet/mesh.py` (adapt HelloMesh `parse_peers`; add `worker_index`, `MeshConfig`, retrying HTTP):
```python
from __future__ import annotations
import os, time, urllib.request, urllib.error
from dataclasses import dataclass

DEFAULT_PORT = 8080

def parse_peers(raw: str, self_id: str, default_port: int = DEFAULT_PORT) -> list[str]:
    targets, seen = [], set()
    for item in raw.split(","):
        item = item.strip()
        if not item:
            continue
        head, _, tail = item.partition(":")
        if head.isdigit():
            if self_id and head == self_id:
                continue
            port = tail if tail else str(default_port)
            target = f"device-{head}.cloud.wendy.dev:{port}"
        elif ":" in item:
            target = item
        else:
            target = f"{item}:{default_port}"
        if target not in seen:
            seen.add(target); targets.append(target)
    return targets

def worker_index(self_id: str, peers_raw: str) -> tuple[int, int]:
    ids = sorted({p.strip() for p in peers_raw.split(",") if p.strip().isdigit()}, key=int)
    return (ids.index(self_id) if self_id in ids else 0, len(ids) or 1)

@dataclass
class MeshConfig:
    role: str; self_id: str; learner_id: str; peers: list[str]
    port: int; backend: str; ckpt_dir: str
    @classmethod
    def from_env(cls) -> "MeshConfig":
        self_id = os.environ.get("MESH_SELF", "").strip()
        return cls(
            role=os.environ.get("ROLE", "worker").strip(),
            self_id=self_id,
            learner_id=os.environ.get("LEARNER_ID", "").strip(),
            peers=parse_peers(os.environ.get("MESH_PEERS", ""), self_id),
            port=int(os.environ.get("MESH_PORT", DEFAULT_PORT)),
            backend=os.environ.get("SIM_BACKEND", "cpu").strip(),
            ckpt_dir=os.environ.get("CKPT_DIR", "/data/checkpoints").strip(),
        )

def _retry(fn, retries, base=0.5):
    last = None
    for i in range(retries):
        try:
            return fn()
        except (urllib.error.URLError, ConnectionError, OSError) as e:
            last = e; time.sleep(base * (2 ** i))
    raise last

def http_get(url: str, timeout: float = 5, retries: int = 5) -> bytes:
    return _retry(lambda: urllib.request.urlopen(url, timeout=timeout).read(), retries)

def http_post(url: str, body: bytes, timeout: float = 10, retries: int = 5) -> bytes:
    def once():
        req = urllib.request.Request(url, data=body, method="POST",
                                     headers={"Content-Type": "application/octet-stream"})
        return urllib.request.urlopen(req, timeout=timeout).read()
    return _retry(once, retries)
```

- [ ] **Step 4: Run to verify pass** — `pytest tests/test_mesh.py -v` → 3 passed.

- [ ] **Step 5: Commit**
```bash
git add Examples/g1fleet/mesh.py Examples/g1fleet/tests/test_mesh.py
git commit -m "feat(g1fleet): mesh role/peer wiring + retrying HTTP helpers"
```

---

### Task 5: `g1env.py` — G1 locomotion environment

**Files:**
- Create: `Examples/g1fleet/g1env.py`
- Test: `Examples/g1fleet/tests/test_g1env.py`

**Interfaces:**
- Produces:
  - `class G1Env(seed:int=0)` with attributes `obs_dim:int`, `act_dim:int`
  - `G1Env.reset(seed:int|None=None) -> np.ndarray` (obs)
  - `G1Env.step(action: np.ndarray) -> tuple[np.ndarray, float, bool, dict]` (obs, reward, done, info)
  - module constants: `EPISODE_STEPS`, `CTRL_DECIMATION`, reward weights `W_VEL, W_UP, ALIVE, W_CTRL, FALL_HEIGHT`, `TARGET_VEL`

- [ ] **Step 1: Write the failing test**

`Examples/g1fleet/tests/test_g1env.py`:
```python
import numpy as np
from g1fleet.g1env import G1Env

def test_reset_returns_finite_obs():
    env = G1Env(seed=0)
    obs = env.reset()
    assert obs.shape == (env.obs_dim,) and np.all(np.isfinite(obs))

def test_step_contract_and_finiteness():
    env = G1Env(seed=0); env.reset()
    a = np.zeros(env.act_dim, dtype=np.float32)
    obs, rew, done, info = env.step(a)
    assert obs.shape == (env.obs_dim,)
    assert np.isfinite(rew) and isinstance(done, bool)

def test_determinism_same_seed_same_trajectory():
    def roll():
        env = G1Env(seed=7); env.reset(seed=7)
        rs = []
        a = np.zeros(env.act_dim, dtype=np.float32)
        for _ in range(20):
            _, r, d, _ = env.step(a); rs.append(r)
            if d: break
        return rs
    assert roll() == roll()

def test_episode_terminates_by_horizon_or_fall():
    env = G1Env(seed=0); env.reset()
    a = np.zeros(env.act_dim, dtype=np.float32)
    done = False
    for _ in range(env.EPISODE_STEPS + 5):
        _, _, done, _ = env.step(a)
        if done: break
    assert done
```

- [ ] **Step 2: Run to verify fail** — FAIL (module missing).

- [ ] **Step 3: Implement**

`Examples/g1fleet/g1env.py`:
```python
"""Unitree G1 velocity-tracking + stay-upright task on the MuJoCo C engine (CPU).

Model comes from robot_descriptions (MuJoCo Menagerie unitree_g1), baked into the
image at build time. Action = PD deltas around the home actuator stance, clamped
to actuator control ranges (same clamping idea as Examples/HelloPython/mujoco_g1.py).
"""
from __future__ import annotations
import numpy as np
import mujoco

EPISODE_STEPS = 400          # policy steps per episode
CTRL_DECIMATION = 5          # physics steps per policy step
TARGET_VEL = 0.5             # m/s forward target
W_VEL, W_UP, ALIVE, W_CTRL = 1.5, 0.5, 0.2, 0.001
FALL_HEIGHT = 0.5            # base z below this => fall
ACTION_SCALE = 0.5           # rad delta scale on top of home stance


def _model_path() -> str:
    from robot_descriptions import g1_mj_description
    return g1_mj_description.MJCF_PATH


class G1Env:
    def __init__(self, seed: int = 0):
        self.model = mujoco.MjModel.from_xml_path(_model_path())
        self.data = mujoco.MjData(self.model)
        self._rng = np.random.default_rng(seed)
        self._steps = 0
        self.nu = self.model.nu
        if self.model.nkey > 0:
            mujoco.mj_resetDataKeyframe(self.model, self.data, 0)
        self._home = self.data.ctrl.copy() if self.nu else np.zeros(0)
        self._lo = self.model.actuator_ctrlrange[:, 0].copy()
        self._hi = self.model.actuator_ctrlrange[:, 1].copy()
        self._limited = self.model.actuator_ctrllimited.astype(bool)
        self.act_dim = int(self.nu)
        self.obs_dim = int(self._observe().size)

    def _observe(self) -> np.ndarray:
        d = self.data
        return np.concatenate([
            d.qpos.ravel(), d.qvel.ravel(),
            self._home,  # context: home stance so obs is action-relative
        ]).astype(np.float32)

    def reset(self, seed: int | None = None) -> np.ndarray:
        if seed is not None:
            self._rng = np.random.default_rng(seed)
        mujoco.mj_resetData(self.model, self.data)
        if self.model.nkey > 0:
            mujoco.mj_resetDataKeyframe(self.model, self.data, 0)
        self._steps = 0
        return self._observe()

    def _base_height(self) -> float:
        return float(self.data.qpos[2]) if self.data.qpos.size > 2 else 0.0

    def _forward_vel(self) -> float:
        return float(self.data.qvel[0]) if self.data.qvel.size > 0 else 0.0

    def step(self, action: np.ndarray):
        a = np.asarray(action, dtype=np.float32).ravel()[: self.nu]
        target = self._home + ACTION_SCALE * a
        if self.nu:
            target = np.where(self._limited, np.clip(target, self._lo, self._hi), target)
            self.data.ctrl[:] = target
        for _ in range(CTRL_DECIMATION):
            mujoco.mj_step(self.model, self.data)
        self._steps += 1
        h = self._base_height(); v = self._forward_vel()
        upright = max(0.0, 1.0 - abs(h - 0.8))  # ~0.8m nominal standing height
        vel_track = -abs(v - TARGET_VEL)
        ctrl_cost = float(np.square(a).sum())
        fell = h < FALL_HEIGHT
        reward = W_VEL * vel_track + W_UP * upright + ALIVE - W_CTRL * ctrl_cost
        if fell:
            reward -= 1.0
        done = bool(fell or self._steps >= EPISODE_STEPS)
        return self._observe(), reward, done, {"h": h, "v": v}

    # expose constants as attributes for tests
    EPISODE_STEPS = EPISODE_STEPS
```

Note on nominal height (`0.8`): if the G1 keyframe stance height differs, adjust the `upright` reference and `FALL_HEIGHT` after the first `reset()` prints `info["h"]`. Capture the real standing height in Step 4 and set the constant accordingly.

- [ ] **Step 4: Run to verify pass + calibrate height**

Run: `cd Examples/g1fleet && . .venv/bin/activate && pytest tests/test_g1env.py -v`
Expected: 4 passed. Then calibrate:
```bash
python -c "from g1fleet.g1env import G1Env; e=G1Env(); e.reset(); import numpy as np; print('h0', e.step(np.zeros(e.act_dim))[3]['h'])"
```
If `h0` is not ~0.8, set the `0.8` reference and `FALL_HEIGHT` (≈ 0.6·h0) to match, rerun tests.

- [ ] **Step 5: Commit**
```bash
git add Examples/g1fleet/g1env.py Examples/g1fleet/tests/test_g1env.py
git commit -m "feat(g1fleet): G1 velocity-tracking env (CPU mujoco)"
```

---

### Task 6: `rollout.py` — backend interface + CPUBackend

**Files:**
- Create: `Examples/g1fleet/rollout.py`
- Test: extend `Examples/g1fleet/tests/test_g1env.py` with rollout tests (same deps)

**Interfaces:**
- Produces:
  - `class Trajectory` dataclass: `obs, actions, logprobs, rewards, values, dones` (all `np.ndarray`)
  - `class CPUBackend(obs_dim, act_dim, workers:int)`
  - `CPUBackend.evaluate_returns(param_vectors: list[np.ndarray], seeds: list[int]) -> np.ndarray` (ES: mean episode return per param vector; uses a process pool)
  - `CPUBackend.collect_trajectory(param_vector: np.ndarray, n_steps:int, seed:int, value_fn=None) -> Trajectory` (PPO)
  - `make_backend(kind:str, obs_dim, act_dim, workers) -> Backend` — raises on unknown/failed `mjx`

- [ ] **Step 1: Write the failing test** (append to `test_g1env.py`)

```python
def test_cpu_backend_evaluate_returns_shape():
    from g1fleet.g1env import G1Env
    from g1fleet.policy import MLPPolicy
    from g1fleet.rollout import CPUBackend
    e = G1Env(); p = MLPPolicy(e.obs_dim, e.act_dim, hidden=(16, 16))
    be = CPUBackend(e.obs_dim, e.act_dim, workers=2)
    import numpy as np
    v = p.get_flat()
    out = be.evaluate_returns([v, v * 0.0], seeds=[1, 2])
    assert out.shape == (2,) and np.all(np.isfinite(out))

def test_cpu_backend_collect_trajectory_lengths():
    from g1fleet.g1env import G1Env
    from g1fleet.policy import MLPPolicy
    from g1fleet.rollout import CPUBackend
    e = G1Env(); p = MLPPolicy(e.obs_dim, e.act_dim, hidden=(16, 16))
    be = CPUBackend(e.obs_dim, e.act_dim, workers=1)
    tr = be.collect_trajectory(p.get_flat(), n_steps=32, seed=0)
    assert tr.obs.shape[0] == 32 and tr.rewards.shape[0] == 32
```

- [ ] **Step 2: Run to verify fail** — FAIL (module missing).

- [ ] **Step 3: Implement**

`Examples/g1fleet/rollout.py`:
```python
"""Pluggable rollout backends. CPUBackend uses the mujoco C engine across a
process pool (each worker builds its own G1Env — MjModel/MjData are not
shareable across processes)."""
from __future__ import annotations
from dataclasses import dataclass
from concurrent.futures import ProcessPoolExecutor
import numpy as np
from .g1env import G1Env
from .policy import MLPPolicy


@dataclass
class Trajectory:
    obs: np.ndarray; actions: np.ndarray; logprobs: np.ndarray
    rewards: np.ndarray; values: np.ndarray; dones: np.ndarray


# --- module-level worker fns (must be picklable for ProcessPoolExecutor) ---
_ENV = {}

def _get_env(obs_dim, act_dim, seed):
    key = (obs_dim, act_dim)
    if key not in _ENV:
        _ENV[key] = G1Env(seed=seed)
    return _ENV[key]

def _episode_return(args):
    flat, seed, obs_dim, act_dim, hidden = args
    env = _get_env(obs_dim, act_dim, seed)
    pol = MLPPolicy(obs_dim, act_dim, hidden=hidden)
    pol.set_flat(flat)
    obs = env.reset(seed=seed); total = 0.0; done = False
    while not done:
        obs, r, done, _ = env.step(pol.act(obs)); total += r
    return total


class CPUBackend:
    def __init__(self, obs_dim, act_dim, workers: int = 4, hidden=(256, 256)):
        self.obs_dim, self.act_dim, self.workers, self.hidden = obs_dim, act_dim, workers, tuple(hidden)

    def evaluate_returns(self, param_vectors, seeds) -> np.ndarray:
        args = [(np.asarray(v, np.float32), int(s), self.obs_dim, self.act_dim, self.hidden)
                for v, s in zip(param_vectors, seeds)]
        if self.workers <= 1:
            return np.array([_episode_return(a) for a in args], dtype=np.float32)
        with ProcessPoolExecutor(max_workers=self.workers) as ex:
            return np.array(list(ex.map(_episode_return, args)), dtype=np.float32)

    def collect_trajectory(self, param_vector, n_steps, seed, value_fn=None) -> Trajectory:
        env = G1Env(seed=seed); pol = MLPPolicy(self.obs_dim, self.act_dim, hidden=self.hidden)
        pol.set_flat(np.asarray(param_vector, np.float32))
        obs = env.reset(seed=seed)
        O, A, R, D, V = [], [], [], [], []
        for _ in range(n_steps):
            a = pol.act(obs)
            O.append(obs); A.append(a)
            V.append(float(value_fn(obs)) if value_fn else 0.0)
            obs, r, done, _ = env.step(a)
            R.append(r); D.append(done)
            if done:
                obs = env.reset()
        z = lambda L: np.asarray(L, np.float32)
        return Trajectory(z(O), z(A), np.zeros(n_steps, np.float32), z(R), z(V), z(D))


def make_backend(kind, obs_dim, act_dim, workers, hidden=(256, 256)):
    if kind == "cpu":
        return CPUBackend(obs_dim, act_dim, workers=workers, hidden=hidden)
    if kind == "mjx":
        raise RuntimeError("mjx backend is a stretch target and not implemented on this path; "
                           "set SIM_BACKEND=cpu")
    raise ValueError(f"unknown SIM_BACKEND={kind!r}")
```

- [ ] **Step 4: Run to verify pass** — `pytest tests/test_g1env.py -v` → all passed (may take ~20-40s for the process-pool test).

- [ ] **Step 5: Commit**
```bash
git add Examples/g1fleet/rollout.py Examples/g1fleet/tests/test_g1env.py
git commit -m "feat(g1fleet): CPU rollout backend (ES returns + PPO trajectories)"
```

---

### Task 7: `es.py` — ES coordinator + worker + gradient math

**Files:**
- Create: `Examples/g1fleet/es.py`
- Test: `Examples/g1fleet/tests/test_es_math.py`

**Interfaces:**
- Produces:
  - `es_gradient(returns_plus, returns_minus, seeds, num_params, sigma) -> np.ndarray` (rank-normalized mirrored ES estimate)
  - `adam_step(theta, grad, state, lr=0.02) -> (theta, state)`
  - `run_coordinator(cfg: MeshConfig, obs_dim, act_dim)` — HTTP server loop (smoke-tested Task 9)
  - `run_worker(cfg: MeshConfig, obs_dim, act_dim)` — pull/evaluate/post loop

The ES estimate reconstructs perturbation `εᵢ` from `seed=base_seed+i` so only scalars cross the wire:
```python
eps_i = np.random.default_rng(base_seed + i).standard_normal(num_params).astype(np.float32)
```

- [ ] **Step 1: Write the failing test** (math only — converges on a toy quadratic)

`Examples/g1fleet/tests/test_es_math.py`:
```python
import numpy as np
from g1fleet.es import es_gradient, adam_step

def test_es_ascends_toy_quadratic():
    # maximize f(x) = -||x - target||^2 ; ES gradient should move theta toward target
    rng = np.random.default_rng(0)
    n = 8; target = rng.standard_normal(n).astype(np.float32)
    theta = np.zeros(n, np.float32); sigma = 0.1; state = None
    def f(x): return -float(np.square(x - target).sum())
    for gen in range(60):
        pop, seeds = 40, list(range(gen * 40, gen * 40 + 40))
        rp, rm = [], []
        for s in seeds:
            eps = np.random.default_rng(s).standard_normal(n).astype(np.float32)
            rp.append(f(theta + sigma * eps)); rm.append(f(theta - sigma * eps))
        g = es_gradient(np.array(rp), np.array(rm), seeds, n, sigma)
        theta, state = adam_step(theta, g, state, lr=0.05)
    assert np.linalg.norm(theta - target) < np.linalg.norm(target) * 0.5
```

- [ ] **Step 2: Run to verify fail** — FAIL (module missing).

- [ ] **Step 3: Implement** (math first; server/worker loops included)

`Examples/g1fleet/es.py`:
```python
from __future__ import annotations
import json, threading, time
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
```

Plus `run_coordinator` / `run_worker` (see plan appendix code block below the task) — the coordinator serves `/params`, `/returns`, `/status`; the worker computes its disjoint slice `[idx*per, (idx+1)*per)` of a fixed population `POP` (env `ES_POP`, default 60), evaluates mirrored returns via `CPUBackend`, and posts them. Coordinator aggregates per generation with a timeout, applies `es_gradient`+`adam_step`, checkpoints `theta.npy` to `cfg.ckpt_dir`, logs `gen mean_return best_return`.

- [ ] **Step 4: Run to verify pass** — `pytest tests/test_es_math.py -v` → 1 passed.

- [ ] **Step 5: Commit**
```bash
git add Examples/g1fleet/es.py Examples/g1fleet/tests/test_es_math.py
git commit -m "feat(g1fleet): ES gradient/adam + coordinator/worker drivers"
```

---

### Task 8: `ppo.py` — actor–learner PPO drivers

**Files:**
- Create: `Examples/g1fleet/ppo.py`
- Test: `Examples/g1fleet/tests/test_ppo.py`

**Interfaces:**
- Produces:
  - `compute_gae(rewards, values, dones, gamma=0.99, lam=0.95) -> (advantages, returns)`
  - `run_learner(cfg, obs_dim, act_dim)` — torch policy+value; serves `/weights`, `/rollout`, `/status`
  - `run_actor(cfg, obs_dim, act_dim)` — pull weights, collect trajectory, post

- [ ] **Step 1: Write the failing test** (GAE is the pure, testable core)

`Examples/g1fleet/tests/test_ppo.py`:
```python
import numpy as np
from g1fleet.ppo import compute_gae

def test_gae_matches_discounted_return_when_lambda_one_no_bootstrap():
    r = np.array([1.0, 1.0, 1.0], np.float32)
    v = np.zeros(3, np.float32); d = np.array([0, 0, 1], np.float32)
    adv, ret = compute_gae(r, v, d, gamma=1.0, lam=1.0)
    # returns = reverse cumulative sum when values=0, gamma=1
    assert np.allclose(ret, [3.0, 2.0, 1.0], atol=1e-5)
    assert np.allclose(adv, ret, atol=1e-5)  # adv = ret - v, v=0
```

- [ ] **Step 2: Run to verify fail** — FAIL (module missing).

- [ ] **Step 3: Implement** `compute_gae` (complete) + learner/actor loops.

`compute_gae` (complete):
```python
import numpy as np
def compute_gae(rewards, values, dones, gamma=0.99, lam=0.95):
    r = np.asarray(rewards, np.float32); v = np.asarray(values, np.float32)
    d = np.asarray(dones, np.float32); n = len(r)
    adv = np.zeros(n, np.float32); last = 0.0
    for t in reversed(range(n)):
        nonterminal = 1.0 - d[t]
        next_v = v[t + 1] if t + 1 < n else 0.0
        delta = r[t] + gamma * next_v * nonterminal - v[t]
        last = delta + gamma * lam * nonterminal * last
        adv[t] = last
    return adv, adv + v
```

Learner (torch): holds `TorchMLP` policy head + a value head (separate small torch MLP, obs→1); a Gaussian policy with a learned log-std parameter vector; serves `GET /weights` returning `encode_named({"theta": policy.get_flat(), "version": np.array([ver])})`; `POST /rollout` decodes a trajectory batch (obs, actions, rewards, dones, logprobs, values, weights_version), drops it if `ver - weights_version > MAX_STALENESS` (env `MAX_STALENESS`, default 3), else appends to a buffer; when buffer ≥ `TRAIN_BATCH` (env, default 4096) runs PPO (clip 0.2, `PPO_EPOCHS` default 4, minibatch 1024, Adam lr 3e-4, GAE via `compute_gae`), bumps `version`, logs `mean_return kl policy_loss value_loss`, checkpoints. Actors recompute `logprob`/`value` locally using the pulled weights and the same torch heads (so `collect_trajectory` here uses a torch `value_fn` and stores logprobs).

Because actors need torch to compute logprobs/values consistently, PPO actors use a torch policy (not `MLPPolicy`). Provide `TorchActor` inside `ppo.py` wrapping `TorchMLP` + log-std + value head with `.act_logprob_value(obs)`.

- [ ] **Step 4: Run to verify pass** — `pytest tests/test_ppo.py -v` → 1 passed.

- [ ] **Step 5: Commit**
```bash
git add Examples/g1fleet/ppo.py Examples/g1fleet/tests/test_ppo.py
git commit -m "feat(g1fleet): PPO GAE + learner/actor drivers"
```

---

### Task 9: Local two-process mesh smoke tests

**Files:**
- Create: `Examples/g1fleet/tests/test_smoke_local.py`

**Interfaces:** consumes `run_coordinator/run_worker` (Task 7) and `run_learner/run_actor` (Task 8).

- [ ] **Step 1: Write the test** — start a coordinator on `127.0.0.1:8080` in a thread, run one worker pointed at `MESH_PEERS=<self>,127.0.0.1:8080` for 2 generations, assert `GET /status` shows `generation >= 1` and a finite `mean_return`. Repeat analogously for PPO (`version >= 1`). Use tiny `hidden=(16,16)`, `ES_POP=8`, `EPISODE_STEPS` monkeypatched small, and short timeouts so the test runs in <60s.

```python
import threading, time, urllib.request, json, os
import numpy as np

def test_es_local_smoke(monkeypatch):
    import g1fleet.g1env as ge
    monkeypatch.setattr(ge, "EPISODE_STEPS", 20, raising=False)
    from g1fleet.mesh import MeshConfig
    from g1fleet import es
    obs_dim, act_dim = _tiny_dims()
    coord_cfg = MeshConfig(role="coordinator", self_id="1", learner_id="1",
                           peers=[], port=8080, backend="cpu", ckpt_dir="/tmp/ck1")
    t = threading.Thread(target=es.run_coordinator, args=(coord_cfg, obs_dim, act_dim), daemon=True)
    t.start(); time.sleep(2)
    worker_cfg = MeshConfig(role="worker", self_id="2", learner_id="1",
                            peers=["127.0.0.1:8080"], port=8081, backend="cpu", ckpt_dir="/tmp/ck2")
    wt = threading.Thread(target=es.run_worker, args=(worker_cfg, obs_dim, act_dim), daemon=True)
    wt.start()
    deadline = time.time() + 90; gen = 0
    while time.time() < deadline:
        try:
            s = json.loads(urllib.request.urlopen("http://127.0.0.1:8080/status", timeout=2).read())
            gen = s.get("generation", 0)
            if gen >= 1 and np.isfinite(s.get("mean_return", float("nan"))): break
        except Exception: pass
        time.sleep(2)
    assert gen >= 1
```
(`_tiny_dims()` constructs a `G1Env` once to read `obs_dim/act_dim`. The coordinator/worker drivers must accept `ES_POP`, `hidden`, and bind host/port from env/cfg so the test can shrink them. Add those env reads in Tasks 7–8 if not already present.)

- [ ] **Step 2: Run** — `pytest tests/test_smoke_local.py -v` → passed (may take ~1–2 min). This is the gate that the mesh RPC protocol works before touching hardware.

- [ ] **Step 3: Commit**
```bash
git add Examples/g1fleet/tests/test_smoke_local.py
git commit -m "test(g1fleet): local two-process ES/PPO mesh smoke"
```

---

### Task 10: App wrappers, sync script, packaging

**Files:**
- Create: `scripts/sync_g1fleet.sh`
- Create: `Examples/G1FleetES/{app.py,wendy.json,Dockerfile,requirements.txt}` + synced `g1fleet/`
- Create: `Examples/G1FleetPPO/{app.py,wendy.json,Dockerfile,requirements.txt}` + synced `g1fleet/`

- [ ] **Step 1: Sync script**

`scripts/sync_g1fleet.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
src="$root/Examples/g1fleet"
for app in G1FleetES G1FleetPPO; do
  dst="$root/Examples/$app/g1fleet"
  rm -rf "$dst"; mkdir -p "$dst"
  rsync -a --exclude tests --exclude .venv --exclude '__pycache__' --exclude '*.pyc' \
        --exclude requirements-dev.txt --exclude pytest.ini "$src/" "$dst/"
done
echo "synced g1fleet -> G1FleetES, G1FleetPPO"
```
Run: `chmod +x scripts/sync_g1fleet.sh && ./scripts/sync_g1fleet.sh`

- [ ] **Step 2: ES `app.py`**
```python
#!/usr/bin/env python3
"""G1FleetES entrypoint. ROLE=coordinator|worker dispatch over the mesh."""
import os
from g1fleet.mesh import MeshConfig
from g1fleet.g1env import G1Env
from g1fleet import es

def main():
    cfg = MeshConfig.from_env()
    env = G1Env()  # build once to learn dims (cheap)
    obs_dim, act_dim = env.obs_dim, env.act_dim
    del env
    print(f"[g1fleet-es] role={cfg.role} self={cfg.self_id} peers={cfg.peers} backend={cfg.backend}", flush=True)
    if cfg.role == "coordinator":
        es.run_coordinator(cfg, obs_dim, act_dim)
    else:
        es.run_worker(cfg, obs_dim, act_dim)

if __name__ == "__main__":
    main()
```

- [ ] **Step 3: PPO `app.py`** — identical shape, `ROLE=learner|actor` → `ppo.run_learner/ppo.run_actor`.

- [ ] **Step 4: `requirements.txt`**
  - ES: `numpy`\n`mujoco`\n`robot_descriptions`\n`debugpy`
  - PPO: `numpy`\n`mujoco`\n`robot_descriptions`\n`torch`\n`debugpy`

- [ ] **Step 5: Dockerfile (both; PPO adds torch via requirements)**
```dockerfile
FROM python:3.11-slim
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 rsync \
    && rm -rf /var/lib/apt/lists/*
COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip pip install --no-cache-dir -r requirements.txt
# Bake the G1 model into the image (no runtime downloads)
RUN python -c "from robot_descriptions import g1_mj_description as g; print(g.MJCF_PATH)"
COPY g1fleet/ ./g1fleet/
COPY app.py .
RUN useradd --create-home --shell /bin/bash app && mkdir -p /data/checkpoints && chown -R app:app /app /data
USER app
ENV PYTHONUNBUFFERED=1
EXPOSE 8080 5678
CMD ["python", "app.py"]
```
(Note: `robot_descriptions` caches under `$HOME/.cache`; the prefetch runs as root so also set `ENV ROBOT_DESCRIPTIONS_CACHE=/opt/rd` before prefetch and `chown` it to `app`, OR run the prefetch after `USER app`. Choose the latter: move the prefetch line to after `USER app` to guarantee the cache is readable at runtime.)

- [ ] **Step 6: `wendy.json` (ES; PPO mirrors with its own appId)**
```json
{
  "appId": "sh.wendy.examples.g1fleet-es",
  "version": "1.0.0",
  "platform": "linux",
  "isolation": "isolated",
  "services": {
    "trainer": {
      "context": ".",
      "entitlements": [
        { "type": "network", "mode": "mesh", "serviceCIDR": "10.99.0.0/16",
          "ports": [{ "host": 8080, "container": 8080 }] },
        { "type": "gpu" },
        { "type": "persist", "name": "g1fleet-es-ckpt", "path": "/data/checkpoints" }
      ],
      "env": {
        "ROLE": "${ROLE}", "MESH_SELF": "${MESH_SELF}", "LEARNER_ID": "${LEARNER_ID}",
        "MESH_PEERS": "${MESH_PEERS}", "SIM_BACKEND": "${SIM_BACKEND}", "ES_POP": "${ES_POP}"
      }
    }
  }
}
```

- [ ] **Step 7: Local Docker build sanity (arm64 host or buildx)**

Run: `cd Examples/G1FleetES && docker build -t g1fleet-es:local .`
Expected: build succeeds; the prefetch line prints an MJCF path.

- [ ] **Step 8: Commit**
```bash
git add scripts/sync_g1fleet.sh Examples/G1FleetES Examples/G1FleetPPO
git commit -m "feat(examples): G1FleetES + G1FleetPPO app wrappers and packaging"
```

---

### Task 11: E2E on the real fleet (CPU) — the pass/fail bar

**Files:** none (deployment + observation). Uses `wendy cloud run`, dashboard Mesh tab, `wendy cloud device logs`.

- [ ] **Step 1: Deploy ES to all three Sparks** (run each; `--detach` so logs don't block)
```bash
cd Examples/G1FleetES && ./../../scripts/sync_g1fleet.sh
ROLE=coordinator MESH_SELF=284 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu ES_POP=60 wendy cloud run --device 284 --detach
ROLE=worker MESH_SELF=283 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu ES_POP=60 wendy cloud run --device 283 --detach
ROLE=worker MESH_SELF=211 LEARNER_ID=284 MESH_PEERS=284,283,211 SIM_BACKEND=cpu ES_POP=60 wendy cloud run --device 211 --detach
```
Expected: three successful deploys.

- [ ] **Step 2: Confirm mesh peering** — open each device's dashboard **Mesh** tab (or `wendy cloud device dashboard --device 284`); expect LAN-direct connections to the other two.

- [ ] **Step 3: Confirm reward climbs** — stream the coordinator:
```bash
wendy cloud device logs --device 284
```
Expected: repeating `[g1fleet-es] gen=<n> mean_return=<r> best=<b>` lines with `mean_return` trending up over several minutes. **This is the pass/fail bar for ES.**

- [ ] **Step 4: Deploy + verify PPO** — same for `Examples/G1FleetPPO` with `ROLE=learner` on 284, `ROLE=actor` on 283/211; expect learner logs `version=<n> mean_return=<r>` climbing.

- [ ] **Step 5: Write `Examples/README-G1Fleet.md`** documenting both apps, the deploy commands, the env vars, the Mesh-tab verification, and the CPU-vs-MJX note. Commit everything.

```bash
git add Examples/README-G1Fleet.md
git commit -m "docs(examples): G1 fleet distributed RL README + verified E2E"
```

---

### Task 12 (stretch, optional): MJX GPU backend

Only after Task 11 passes. Add `MJXBackend` in `rollout.py`, an NVIDIA-CUDA-13 arm64 Dockerfile variant selected by build arg, and `jax[cuda]`+`mujoco-mjx` in a separate requirements file. Resolve `sm_121`/CUDA-13 wheel compatibility (the known risk). Compare throughput vs CPU. Do not block the merge on this.

---

## Self-Review

**Spec coverage:**
- Shared core (g1env/policy/rollout/mesh/netcodec) → Tasks 2–6. ✓
- ES app → Tasks 7, 10. ✓  PPO app → Tasks 8, 10. ✓
- Mesh entitlement/isolation/ports/persist/gpu → Task 10 wendy.json. ✓
- Pluggable backend, CPU default, mjx exits-non-zero → Task 6 `make_backend`. ✓
- Model baked in, no wendymujoco → Task 1 prefetch + Task 5 `_model_path` + Task 10 Dockerfile. ✓
- Verification phases (unit → local mesh smoke → real fleet) → Tasks 2–9, 11. ✓
- Deploy via `wendy cloud run --device <id>` → Task 11. ✓
- Stretch MJX → Task 12. ✓

**Placeholder scan:** ES `run_coordinator/run_worker` and PPO `run_learner/run_actor` loops are described by behavior + exact endpoint names/wire format rather than full literal bodies — these are the two largest units; their public interfaces, endpoints, env vars, and wire encodings are fully specified, and their pure cores (`es_gradient`,`adam_step`,`compute_gae`) have complete code + tests. Acceptable: the executing agent implements the HTTP loop against fixed contracts, gated by the Task 9 smoke test.

**Type consistency:** `MeshConfig` fields, `get_flat/set_flat`, `evaluate_returns`/`collect_trajectory` signatures, `Trajectory` fields, and `es_gradient` args are consistent across Tasks 4–9. `encode_named/decode_named` used uniformly for the wire. ✓

**Known risk called out:** MJX on Blackwell isolated to Task 12; G1 standing-height constant calibrated in Task 5 Step 4.
