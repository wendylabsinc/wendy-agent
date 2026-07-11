"""Local two-process (two-thread) mesh smoke tests. These are the gate proving
the mesh HTTP protocol (encode_named/decode_named wire format, endpoint
contracts) works before touching real fleet hardware (Task 11)."""
import socket, threading, time, urllib.request, json
import numpy as np


def _tiny_dims():
    from g1fleet.g1env import G1Env
    e = G1Env()
    return e.obs_dim, e.act_dim


def _free_port():
    """Grab an OS-assigned free port. Avoids collisions with leftover
    daemon-thread servers (these tests' HTTP servers run serve_forever and only
    die on process exit), so back-to-back runs never hit 'Address already in use'."""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def test_es_local_smoke(monkeypatch):
    import g1fleet.g1env as ge
    monkeypatch.setattr(ge, "EPISODE_STEPS", 20, raising=False)
    monkeypatch.setenv("ES_POP", "8")
    monkeypatch.setenv("POLICY_HIDDEN", "16,16")
    # ES_WORKERS=1 keeps evaluation in-process: a ProcessPoolExecutor would
    # spawn fresh interpreters (default 'spawn' start method on macOS) that
    # would not see the monkeypatched EPISODE_STEPS above.
    monkeypatch.setenv("ES_WORKERS", "1")
    monkeypatch.setenv("ES_GEN_TIMEOUT_S", "20")

    from g1fleet.mesh import MeshConfig
    from g1fleet import es

    obs_dim, act_dim = _tiny_dims()
    coord_port = _free_port()
    coord_cfg = MeshConfig(role="coordinator", self_id="1", learner_id="1",
                           peers=[], port=coord_port, backend="cpu", ckpt_dir="/tmp/ck1")
    t = threading.Thread(target=es.run_coordinator, args=(coord_cfg, obs_dim, act_dim), daemon=True)
    t.start(); time.sleep(2)

    worker_cfg = MeshConfig(role="worker", self_id="2", learner_id="1",
                            peers=[f"127.0.0.1:{coord_port}"], port=_free_port(), backend="cpu", ckpt_dir="/tmp/ck2")
    wt = threading.Thread(target=es.run_worker, args=(worker_cfg, obs_dim, act_dim), daemon=True)
    wt.start()

    deadline = time.time() + 90; gen = 0
    while time.time() < deadline:
        try:
            s = json.loads(urllib.request.urlopen(f"http://127.0.0.1:{coord_port}/status", timeout=2).read())
            gen = s.get("generation", 0)
            if gen >= 1 and np.isfinite(s.get("mean_return", float("nan"))):
                break
        except Exception:
            pass
        time.sleep(2)
    assert gen >= 1


def test_ppo_local_smoke(monkeypatch):
    import g1fleet.g1env as ge
    monkeypatch.setattr(ge, "EPISODE_STEPS", 20, raising=False)
    monkeypatch.setenv("POLICY_HIDDEN", "16,16")
    monkeypatch.setenv("TRAIN_BATCH", "64")
    monkeypatch.setenv("PPO_MINIBATCH", "32")
    monkeypatch.setenv("PPO_EPOCHS", "1")
    monkeypatch.setenv("PPO_ROLLOUT_STEPS", "64")
    monkeypatch.setenv("MAX_STALENESS", "10")

    from g1fleet.mesh import MeshConfig
    from g1fleet import ppo

    obs_dim, act_dim = _tiny_dims()
    learner_port = _free_port()
    learner_cfg = MeshConfig(role="learner", self_id="1", learner_id="1",
                             peers=[], port=learner_port, backend="cpu", ckpt_dir="/tmp/ck3")
    lt = threading.Thread(target=ppo.run_learner, args=(learner_cfg, obs_dim, act_dim), daemon=True)
    lt.start(); time.sleep(2)

    actor_cfg = MeshConfig(role="actor", self_id="2", learner_id="1",
                           peers=[f"127.0.0.1:{learner_port}"], port=_free_port(), backend="cpu", ckpt_dir="/tmp/ck4")
    at = threading.Thread(target=ppo.run_actor, args=(actor_cfg, obs_dim, act_dim), daemon=True)
    at.start()

    deadline = time.time() + 90; version = 0
    while time.time() < deadline:
        try:
            s = json.loads(urllib.request.urlopen(f"http://127.0.0.1:{learner_port}/status", timeout=2).read())
            version = s.get("version", 0)
            if version >= 1 and np.isfinite(s.get("mean_return", float("nan"))):
                break
        except Exception:
            pass
        time.sleep(2)
    assert version >= 1
