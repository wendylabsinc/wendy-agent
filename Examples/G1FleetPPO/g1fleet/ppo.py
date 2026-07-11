"""Actor-learner PPO. The learner holds torch policy (mean net + log-std) and
value heads and serves GET /weights, POST /rollout, GET /status. Actors pull
weights, roll out an episode segment on the CPU mujoco backend using the same
torch heads (so logprob/value are locally consistent with the pulled
weights), and post the trajectory back."""
from __future__ import annotations
import os, threading, time, json, itertools
import numpy as np
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from .mesh import MeshConfig, http_get, http_post
from .netcodec import encode_named, decode_named
from .policy import TorchMLP
from .rollout import Trajectory
from .g1env import G1Env


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


def _hidden_from_env(default=(256, 256)):
    raw = os.environ.get("POLICY_HIDDEN", "")
    if not raw.strip():
        return tuple(default)
    return tuple(int(x) for x in raw.split(",") if x.strip())


def _env_int(name, default):
    return int(os.environ.get(name, default))


def _env_float(name, default):
    return float(os.environ.get(name, default))


def _make_value_net(obs_dim, hidden):
    import torch.nn as nn
    dims = [obs_dim, *hidden, 1]
    layers = []
    for i in range(len(dims) - 1):
        layers.append(nn.Linear(dims[i], dims[i + 1]))
        if i < len(dims) - 2:
            layers.append(nn.Tanh())
    return nn.Sequential(*layers)


def _seq_get_flat(net):
    import torch
    parts = []
    for m in net:
        if hasattr(m, "weight"):
            parts.append(m.weight.detach().T.reshape(-1))
            parts.append(m.bias.detach().reshape(-1))
    return torch.cat(parts).cpu().numpy().astype(np.float32)


def _seq_set_flat(net, v):
    import torch
    v = torch.as_tensor(v, dtype=torch.float32); i = 0
    with torch.no_grad():
        for m in net:
            if hasattr(m, "weight"):
                nin, nout = m.weight.shape[1], m.weight.shape[0]
                n = nin * nout
                m.weight.copy_(v[i:i + n].reshape(nin, nout).T); i += n
                n = m.bias.numel()
                m.bias.copy_(v[i:i + n].reshape(m.bias.shape)); i += n


class TorchActor:
    """Gaussian policy (tanh-bounded mean + learned log-std) + a value head.
    Actions are sampled, clamped to [-1,1] for env execution, and logprob is
    evaluated at that same clamped point on both the actor and learner side
    so importance ratios stay self-consistent."""

    def __init__(self, obs_dim: int, act_dim: int, hidden=(256, 256)):
        import torch
        self._torch = torch
        self.obs_dim, self.act_dim, self.hidden = obs_dim, act_dim, tuple(hidden)
        self.mean_net = TorchMLP(obs_dim, act_dim, hidden=hidden)
        self.log_std = torch.zeros(act_dim, requires_grad=True)
        self.value_net = _make_value_net(obs_dim, hidden)

    def parameters(self):
        return itertools.chain(self.mean_net.parameters(), [self.log_std], self.value_net.parameters())

    def act_logprob_value(self, obs: np.ndarray):
        t = self._torch
        with t.no_grad():
            x = t.from_numpy(np.asarray(obs, np.float32))[None]
            mean = self.mean_net(x)[0]
            std = t.exp(self.log_std).clamp(min=1e-4)
            dist = t.distributions.Normal(mean, std)
            raw = dist.sample()
            action = t.clamp(raw, -1.0, 1.0)
            logp = dist.log_prob(action).sum()
            value = self.value_net(x)[0, 0]
        return (action.numpy().astype(np.float32), float(logp.item()), float(value.item()))

    def logprob_value_entropy(self, obs, actions):
        """Batched, differentiable: obs (B,obs_dim), actions (B,act_dim) torch tensors."""
        t = self._torch
        mean = self.mean_net(obs)
        std = t.exp(self.log_std).clamp(min=1e-4)
        dist = t.distributions.Normal(mean, std)
        logp = dist.log_prob(actions).sum(-1)
        entropy = dist.entropy().sum(-1)
        value = self.value_net(obs).squeeze(-1)
        return logp, value, entropy


def _collect_torch_trajectory(env: G1Env, actor: TorchActor, n_steps: int, seed=None) -> Trajectory:
    obs = env.reset(seed=seed) if seed is not None else env.reset()
    O, A, LP, R, V, D = [], [], [], [], [], []
    for _ in range(n_steps):
        a, logp, val = actor.act_logprob_value(obs)
        O.append(obs); A.append(a); LP.append(logp); V.append(val)
        obs, r, done, _ = env.step(a)
        R.append(r); D.append(done)
        if done:
            obs = env.reset()
    z = lambda L: np.asarray(L, np.float32)
    return Trajectory(z(O), z(A), z(LP), z(R), z(V), z(D))


# ---------------------------------- learner -----------------------------------

def run_learner(cfg: MeshConfig, obs_dim: int, act_dim: int) -> None:
    import torch
    import torch.nn.functional as F

    hidden = _hidden_from_env()
    train_batch = _env_int("TRAIN_BATCH", 4096)
    max_staleness = _env_int("MAX_STALENESS", 3)
    ppo_epochs = _env_int("PPO_EPOCHS", 4)
    minibatch_size = _env_int("PPO_MINIBATCH", 1024)
    clip = _env_float("PPO_CLIP", 0.2)
    lr = _env_float("PPO_LR", 3e-4)
    gamma = _env_float("PPO_GAMMA", 0.99)
    lam = _env_float("PPO_LAM", 0.95)
    entropy_coef = _env_float("PPO_ENTROPY_COEF", 0.0)

    actor = TorchActor(obs_dim, act_dim, hidden=hidden)
    opt = torch.optim.Adam(actor.parameters(), lr=lr)

    lock = threading.Lock()
    state = {
        "version": 0,
        "buffer": [],
        "buffer_steps": 0,
        "mean_return": float("nan"),
        "kl": float("nan"),
        "policy_loss": float("nan"),
        "value_loss": float("nan"),
    }

    def _checkpoint():
        try:
            os.makedirs(cfg.ckpt_dir, exist_ok=True)
            np.savez(
                os.path.join(cfg.ckpt_dir, "ppo_policy.npz"),
                theta=actor.mean_net.get_flat(),
                log_std=actor.log_std.detach().numpy().astype(np.float32),
                value_theta=_seq_get_flat(actor.value_net),
            )
        except OSError as e:
            print(f"[g1fleet-ppo] WARN checkpoint failed: {e}", flush=True)

    def _train_locked():
        buf = state["buffer"]
        obs = np.concatenate([b["obs"] for b in buf])
        actions = np.concatenate([b["actions"] for b in buf])
        old_logp = np.concatenate([b["logprobs"] for b in buf])
        adv_list, ret_list = [], []
        for b in buf:
            a, r = compute_gae(b["rewards"], b["values"], b["dones"], gamma=gamma, lam=lam)
            adv_list.append(a); ret_list.append(r)
        adv = np.concatenate(adv_list); ret = np.concatenate(ret_list)
        adv = (adv - adv.mean()) / (adv.std() + 1e-8)
        mean_return = float(np.mean([np.sum(b["rewards"]) for b in buf]))

        obs_t = torch.as_tensor(obs, dtype=torch.float32)
        act_t = torch.as_tensor(actions, dtype=torch.float32)
        old_logp_t = torch.as_tensor(old_logp, dtype=torch.float32)
        adv_t = torch.as_tensor(adv, dtype=torch.float32)
        ret_t = torch.as_tensor(ret, dtype=torch.float32)

        n = obs_t.shape[0]
        last_policy_loss = last_value_loss = last_kl = float("nan")
        for _epoch in range(ppo_epochs):
            perm = torch.randperm(n)
            for start in range(0, n, minibatch_size):
                idx = perm[start:start + minibatch_size]
                new_logp, value_pred, entropy = actor.logprob_value_entropy(obs_t[idx], act_t[idx])
                ratio = torch.exp(new_logp - old_logp_t[idx])
                surr1 = ratio * adv_t[idx]
                surr2 = torch.clamp(ratio, 1 - clip, 1 + clip) * adv_t[idx]
                policy_loss = -torch.min(surr1, surr2).mean() - entropy_coef * entropy.mean()
                value_loss = F.mse_loss(value_pred, ret_t[idx])
                loss = policy_loss + 0.5 * value_loss
                opt.zero_grad(); loss.backward(); opt.step()
                with torch.no_grad():
                    last_kl = float((old_logp_t[idx] - new_logp).mean().item())
                last_policy_loss = float(policy_loss.item())
                last_value_loss = float(value_loss.item())

        state["mean_return"] = mean_return
        state["kl"] = last_kl
        state["policy_loss"] = last_policy_loss
        state["value_loss"] = last_value_loss
        state["version"] += 1
        state["buffer"] = []
        state["buffer_steps"] = 0
        _checkpoint()
        print(f"[g1fleet-ppo] version={state['version']} mean_return={mean_return:.4f} "
              f"kl={last_kl:.5f} policy_loss={last_policy_loss:.5f} "
              f"value_loss={last_value_loss:.5f}", flush=True)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            pass

        def do_GET(self):
            if self.path == "/weights":
                with lock:
                    body = encode_named({
                        "theta": actor.mean_net.get_flat(),
                        "log_std": actor.log_std.detach().numpy().astype(np.float32),
                        "value_theta": _seq_get_flat(actor.value_net),
                        "version": np.array([state["version"]], dtype=np.int64),
                    })
                self.send_response(200)
                self.send_header("Content-Type", "application/octet-stream")
                self.end_headers()
                self.wfile.write(body)
            elif self.path == "/status":
                with lock:
                    payload = json.dumps({
                        "version": state["version"],
                        "mean_return": state["mean_return"],
                        "kl": state["kl"],
                        "policy_loss": state["policy_loss"],
                        "value_loss": state["value_loss"],
                    }).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(payload)
            else:
                self.send_response(404); self.end_headers()

        def do_POST(self):
            if self.path == "/rollout":
                length = int(self.headers.get("Content-Length", 0))
                body = self.rfile.read(length)
                try:
                    d = decode_named(body)
                    weights_version = int(d["weights_version"][0])
                    with lock:
                        if state["version"] - weights_version > max_staleness:
                            print(f"[g1fleet-ppo] dropping stale rollout "
                                  f"(version={state['version']}, weights_version={weights_version})",
                                  flush=True)
                        else:
                            state["buffer"].append({
                                "obs": d["obs"], "actions": d["actions"],
                                "logprobs": d["logprobs"], "rewards": d["rewards"],
                                "values": d["values"], "dones": d["dones"],
                            })
                            state["buffer_steps"] += len(d["rewards"])
                            if state["buffer_steps"] >= train_batch:
                                _train_locked()
                    self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
                except Exception as e:
                    print(f"[g1fleet-ppo] WARN bad /rollout payload: {e}", flush=True)
                    self.send_response(400); self.end_headers()
            else:
                self.send_response(404); self.end_headers()

    httpd = ThreadingHTTPServer(("0.0.0.0", cfg.port), Handler)
    httpd.serve_forever()


# ---------------------------------- actor -----------------------------------

def run_actor(cfg: MeshConfig, obs_dim: int, act_dim: int) -> None:
    hidden = _hidden_from_env()
    n_steps = _env_int("PPO_ROLLOUT_STEPS", 2048)

    if not cfg.peers:
        print("[g1fleet-ppo] actor has no peers configured; nothing to do", flush=True)
        return
    learner_url = f"http://{cfg.peers[0]}"

    actor = TorchActor(obs_dim, act_dim, hidden=hidden)
    env = G1Env(seed=abs(hash(cfg.self_id)) % (2 ** 31) if cfg.self_id else 0)

    while True:
        try:
            body = http_get(f"{learner_url}/weights")
        except Exception as e:
            print(f"[g1fleet-ppo] WARN learner unreachable: {e}", flush=True)
            time.sleep(2); continue

        d = decode_named(body)
        actor.mean_net.set_flat(np.asarray(d["theta"], np.float32))
        import torch
        actor.log_std = torch.as_tensor(d["log_std"], dtype=torch.float32)
        _seq_set_flat(actor.value_net, np.asarray(d["value_theta"], np.float32))
        version = int(d["version"][0])

        traj = _collect_torch_trajectory(env, actor, n_steps)
        payload = encode_named({
            "obs": traj.obs, "actions": traj.actions, "logprobs": traj.logprobs,
            "rewards": traj.rewards, "values": traj.values, "dones": traj.dones,
            "weights_version": np.array([version], dtype=np.int64),
        })
        try:
            http_post(f"{learner_url}/rollout", payload)
        except Exception as e:
            print(f"[g1fleet-ppo] WARN failed to post rollout: {e}", flush=True)
