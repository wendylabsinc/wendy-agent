"""ppo-fleet template: a Proximal Policy Optimization (PPO) learner with actors.

One learner owns the policy, the value network, and the optimizer; any number
of actors pull weights, collect rollouts in the built-in cart-pole
environment, and post them back. Communication uses the wendytrain wire codec
over the mesh network.

Endpoints served by the learner:
    GET  /weights   wire blob: flat ``policy`` and ``value`` parameter
                    vectors; metadata carries ``version``, ``architecture``
                    (the hidden sizes list), and ``log_std``. Receivers build
                    networks from the metadata, never from parameter counts.
    POST /rollout   wire blob: ``obs``, ``actions``, ``logprobs``,
                    ``rewards``, ``dones``, ``values``; metadata carries
                    ``weights_version``, ``tail_bootstrap``, and
                    ``episode_returns``. Rollouts older than the staleness
                    budget are rejected with a JSON body naming the reason.
    GET  /status    JSON: version, update and rollout counters including
                    ``stale_rollouts`` (rejections are visible, never silently
                    dropped), and the last mean episode return.

Checkpoints carry the policy, the value network, the log standard deviation,
and the full torch optimizer state dictionary serialized into a uint8 wire
array; a restart resumes at the saved version with optimizer step counts
intact.

Roles come from the layer-0 contract (``WT_ROLE``, ``MESH_SELF``,
``MESH_PEERS``): the derived ``coordinator`` maps to the learner and
``worker`` maps to an actor. Environment overrides: ``PPO_STEPS`` (steps per
rollout), ``PPO_MAX_STALENESS`` (accepted version lag), ``PPO_LEARNER``
(explicit learner ``host:port`` when roles cannot be derived).
"""

from __future__ import annotations

import json
import os
import queue
import threading
import time
from typing import Mapping

import numpy as np

from wendytrain import load_config, mesh, rl, wire
from wendytrain.run import Run
from wendytrain.service import serve

import cartpole  # staged next to this file in the deployed image
import nets

DEFAULTS = {
    "ppo": {
        "steps": 512,
        "epochs": 4,
        "minibatch": 128,
        "clip": 0.2,
        "gamma": 0.99,
        "lam": 0.95,
        "lr": 3e-4,
        "entropy_coef": 0.01,
        "value_coef": 0.5,
        "max_staleness": 2,
        "total_versions": 200,
        "log_std_init": -0.5,
    },
    "policy": {"hidden": [32]},
    "run": {"checkpoint_every": 10, "keep_last": 5},
}

_LOG_2PI = float(np.log(2.0 * np.pi))


def _episode_stats(returns: list[float]) -> float | None:
    return float(np.mean(returns)) if returns else None


class Learner:
    """Owns the networks and the optimizer; applies one PPO update per rollout."""

    def __init__(self, cfg, run: Run, env: Mapping[str, str] | None = None, seed: int = 0):
        torch = nets.torch_module()
        env = os.environ if env is None else env
        self.cfg = cfg
        self.run = run
        self.hidden = [int(h) for h in cfg.policy.hidden]
        self.max_staleness = int(env.get("PPO_MAX_STALENESS", cfg.ppo.max_staleness))
        torch.manual_seed(seed)
        self.policy = nets.build_policy(cartpole.CartPole.obs_dim, cartpole.CartPole.act_dim, self.hidden)
        self.value = nets.build_value(cartpole.CartPole.obs_dim, self.hidden)
        self.log_std = torch.nn.Parameter(
            torch.full((cartpole.CartPole.act_dim,), float(cfg.ppo.log_std_init))
        )
        self.optimizer = torch.optim.Adam(
            list(self.policy.parameters()) + list(self.value.parameters()) + [self.log_std],
            lr=float(cfg.ppo.lr),
        )
        self.version = 0
        self.updates = 0
        self.accepted = 0
        self.stale = 0
        self.done = False
        self.last_mean_return: float | None = None
        self.return_history: list[float | None] = []
        self._queue: queue.Queue = queue.Queue(maxsize=8)
        self._lock = threading.Lock()
        self._restore()
        self._weights_blob = self._encode_weights()

    # -- persistence ---------------------------------------------------------

    def _restore(self) -> None:
        loaded = self.run.load_latest()
        if loaded is None:
            return
        torch = nets.torch_module()
        arrays, meta, iteration = loaded
        saved_hidden = [int(h) for h in meta.get("architecture", [])]
        if saved_hidden != self.hidden:
            raise RuntimeError(
                f"checkpoint architecture {saved_hidden} does not match the "
                f"configured hidden sizes {self.hidden}; change policy.hidden "
                "or start a new run id"
            )
        nets.set_flat_params(self.policy, arrays["policy"])
        nets.set_flat_params(self.value, arrays["value"])
        with torch.no_grad():
            self.log_std.copy_(torch.as_tensor(arrays["log_std"].astype(np.float32)))
        self.optimizer.load_state_dict(nets.deserialize_state_dict(arrays["optimizer"]))
        self.version = iteration
        steps = [int(state["step"]) for state in self.optimizer.state_dict()["state"].values()]
        print(
            f"[ppo-fleet] resumed version={self.version} "
            f"adam_steps={steps[0] if steps else 0}",
            flush=True,
        )

    def checkpoint(self) -> None:
        """Write the full learner state, optimizer included, atomically."""
        arrays = {
            "policy": nets.flat_params(self.policy),
            "value": nets.flat_params(self.value),
            "log_std": self.log_std.detach().cpu().numpy().astype(np.float32),
            "optimizer": nets.serialize_state_dict(self.optimizer.state_dict()),
        }
        self.run.save_checkpoint(arrays, {"architecture": self.hidden}, self.version)

    # -- wire ----------------------------------------------------------------

    def _encode_weights(self) -> bytes:
        return wire.encode(
            {
                "policy": nets.flat_params(self.policy),
                "value": nets.flat_params(self.value),
            },
            meta={
                "version": self.version,
                "architecture": self.hidden,
                "log_std": [float(v) for v in self.log_std.detach().cpu().numpy()],
            },
        )

    def routes(self):
        return {
            ("GET", "/weights"): self._handle_weights,
            ("POST", "/rollout"): self._handle_rollout,
            ("GET", "/status"): self._handle_status,
        }

    def _handle_weights(self, body: bytes):
        with self._lock:
            blob = self._weights_blob
        return 200, blob, "application/octet-stream"

    def _handle_rollout(self, body: bytes):
        arrays, meta = wire.decode(body)
        weights_version = int(meta.get("weights_version", -1))
        with self._lock:
            current = self.version
            if self.done:
                reply = {"accepted": False, "reason": "complete", "version": current}
                return 200, json.dumps(reply).encode(), "application/json"
            if current - weights_version > self.max_staleness:
                self.stale += 1
                reply = {
                    "accepted": False,
                    "reason": "stale",
                    "weights_version": weights_version,
                    "version": current,
                    "max_staleness": self.max_staleness,
                }
                return 200, json.dumps(reply).encode(), "application/json"
            self.accepted += 1
        self._queue.put((arrays, meta))
        return 200, json.dumps({"accepted": True, "version": current}).encode(), "application/json"

    def _handle_status(self, body: bytes):
        with self._lock:
            reply = {
                "role": "learner",
                "run_id": self.run.run_id,
                "version": self.version,
                "updates": self.updates,
                "accepted_rollouts": self.accepted,
                "stale_rollouts": self.stale,
                "queue_depth": self._queue.qsize(),
                "mean_return": self.last_mean_return,
                "complete": self.done,
            }
        return 200, json.dumps(reply).encode(), "application/json"

    # -- training ------------------------------------------------------------

    def process_one(self, timeout: float | None = None) -> bool:
        """Apply one PPO update from the queue; False when none arrived."""
        try:
            arrays, meta = self._queue.get(timeout=timeout)
        except queue.Empty:
            return False
        self._update(arrays, meta)
        return True

    def run_updates(self, total_versions: int, poll_timeout: float = 1.0) -> None:
        """Process rollouts until ``total_versions`` updates have been applied."""
        checkpoint_every = int(self.cfg.run.checkpoint_every)
        while self.version < total_versions:
            if not self.process_one(timeout=poll_timeout):
                continue
            if self.version % checkpoint_every == 0:
                self.checkpoint()
        with self._lock:
            self.done = True
        self.checkpoint()

    def _update(self, arrays: dict[str, np.ndarray], meta: dict) -> None:
        torch = nets.torch_module()
        cfg = self.cfg.ppo
        rewards = arrays["rewards"].astype(np.float64)
        values = arrays["values"].astype(np.float64)
        dones = arrays["dones"].astype(np.float64)
        advantages, value_targets = rl.gae(
            rewards, values, dones, gamma=float(cfg.gamma), lam=float(cfg.lam)
        )
        keep = len(rewards) - 1 if meta.get("tail_bootstrap") else len(rewards)

        obs = torch.as_tensor(arrays["obs"][:keep].astype(np.float32))
        actions = torch.as_tensor(arrays["actions"][:keep].astype(np.float32))
        old_logprobs = torch.as_tensor(arrays["logprobs"][:keep].astype(np.float32))
        adv = advantages[:keep]
        adv = (adv - adv.mean()) / (adv.std() + 1e-8)
        adv_t = torch.as_tensor(adv.astype(np.float32))
        targets = torch.as_tensor(value_targets[:keep].astype(np.float32))

        n = keep
        minibatch = min(int(cfg.minibatch), n)
        for _ in range(int(cfg.epochs)):
            order = torch.randperm(n)
            for start in range(0, n, minibatch):
                idx = order[start : start + minibatch]
                mean = self.policy(obs[idx])
                log_std = self.log_std
                logprobs = (
                    -0.5 * (((actions[idx] - mean) / log_std.exp()) ** 2)
                    - log_std
                    - 0.5 * _LOG_2PI
                ).sum(dim=-1)
                ratio = (logprobs - old_logprobs[idx]).exp()
                clipped = torch.clamp(ratio, 1.0 - float(cfg.clip), 1.0 + float(cfg.clip))
                policy_loss = -torch.min(ratio * adv_t[idx], clipped * adv_t[idx]).mean()
                value_loss = ((self.value(obs[idx]).squeeze(-1) - targets[idx]) ** 2).mean()
                entropy = (0.5 * (1.0 + _LOG_2PI) + log_std).sum()
                loss = (
                    policy_loss
                    + float(cfg.value_coef) * value_loss
                    - float(cfg.entropy_coef) * entropy
                )
                self.optimizer.zero_grad()
                loss.backward()
                self.optimizer.step()

        mean_return = _episode_stats([float(r) for r in meta.get("episode_returns", [])])
        with self._lock:
            self.version += 1
            self.updates += 1
            self.last_mean_return = mean_return
            self._weights_blob = self._encode_weights()
        self.return_history.append(mean_return)
        self.run.log_metrics(
            {
                "iteration": self.version,
                "version": self.version,
                "mean_return": mean_return,
                "n_episodes": len(meta.get("episode_returns", [])),
                "n_steps": n,
                "weights_version": int(meta.get("weights_version", -1)),
                "accepted_rollouts": self.accepted,
                "stale_rollouts": self.stale,
            }
        )


class Actor:
    """Pulls weights, collects one rollout per version, posts it back."""

    def __init__(self, cfg, learner: str, env: Mapping[str, str] | None = None, seed: int = 0):
        nets.torch_module()  # fail fast with the actionable message
        env = os.environ if env is None else env
        self.cfg = cfg
        self.learner = learner
        self.steps = int(env.get("PPO_STEPS", cfg.ppo.steps))
        self.env = cartpole.CartPole(seed=seed)
        self.obs = self.env.reset(seed=seed)
        self.rng = np.random.default_rng(seed)
        self.policy = None
        self.value = None
        self.hidden: list[int] | None = None
        self.log_std = np.zeros(cartpole.CartPole.act_dim, dtype=np.float32)
        self.weights_version = -1
        self._episode_acc = 0.0
        self._pending_returns: list[float] = []

    def pull_weights(self) -> None:
        """Fetch current weights; build networks from the metadata architecture."""
        blob = mesh.http_get(f"http://{self.learner}/weights")
        arrays, meta = wire.decode(blob)
        hidden = [int(h) for h in meta["architecture"]]
        if self.policy is None or hidden != self.hidden:
            self.hidden = hidden
            self.policy = nets.build_policy(
                cartpole.CartPole.obs_dim, cartpole.CartPole.act_dim, hidden
            )
            self.value = nets.build_value(cartpole.CartPole.obs_dim, hidden)
        nets.set_flat_params(self.policy, arrays["policy"])
        nets.set_flat_params(self.value, arrays["value"])
        self.log_std = np.asarray(meta["log_std"], dtype=np.float32)
        self.weights_version = int(meta["version"])

    def collect(self) -> tuple[dict[str, np.ndarray], dict]:
        """Collect ``self.steps`` environment steps with the current policy.

        The library's Generalized Advantage Estimation bootstraps 0 past the
        end of the batch, so when the final step does not end its episode the
        actor appends the synthetic bootstrap step itself: reward and value
        both equal the value estimate of the final observation and the step
        is marked done, which leaves every real advantage intact and gives
        the tail its non-zero bootstrap.
        """
        torch = nets.torch_module()
        std = np.exp(self.log_std)
        obs_l, act_l, logp_l, rew_l, done_l, val_l = [], [], [], [], [], []
        for _ in range(self.steps):
            obs_t = torch.as_tensor(self.obs, dtype=torch.float32)
            with torch.no_grad():
                mean = self.policy(obs_t).numpy()
                value = float(self.value(obs_t).numpy()[0])
            noise = self.rng.standard_normal(mean.shape).astype(np.float32)
            action = mean + std * noise
            logprob = float(
                (-0.5 * ((action - mean) / std) ** 2 - self.log_std - 0.5 * _LOG_2PI).sum()
            )
            obs_l.append(self.obs)
            next_obs, reward, done, _ = self.env.step(action)
            act_l.append(action.astype(np.float32))
            logp_l.append(logprob)
            rew_l.append(reward)
            done_l.append(done)
            val_l.append(value)
            self._episode_acc += reward
            if done:
                self._pending_returns.append(self._episode_acc)
                self._episode_acc = 0.0
                next_obs = self.env.reset()
            self.obs = next_obs

        tail_bootstrap = not done_l[-1]
        if tail_bootstrap:
            obs_t = torch.as_tensor(self.obs, dtype=torch.float32)
            with torch.no_grad():
                tail_value = float(self.value(obs_t).numpy()[0])
            obs_l.append(self.obs)
            act_l.append(np.zeros(cartpole.CartPole.act_dim, dtype=np.float32))
            logp_l.append(0.0)
            rew_l.append(tail_value)
            done_l.append(True)
            val_l.append(tail_value)

        arrays = {
            "obs": np.asarray(obs_l, dtype=np.float32),
            "actions": np.asarray(act_l, dtype=np.float32),
            "logprobs": np.asarray(logp_l, dtype=np.float32),
            "rewards": np.asarray(rew_l, dtype=np.float32),
            "dones": np.asarray(done_l, dtype=np.uint8),
            "values": np.asarray(val_l, dtype=np.float32),
        }
        meta = {
            "weights_version": self.weights_version,
            "tail_bootstrap": tail_bootstrap,
            "episode_returns": list(self._pending_returns),
        }
        return arrays, meta

    def run_once(self) -> dict:
        """One pull-collect-post cycle; returns the learner's JSON reply."""
        self.pull_weights()
        arrays, meta = self.collect()
        body = mesh.http_post(f"http://{self.learner}/rollout", wire.encode(arrays, meta))
        reply = json.loads(body)
        if reply.get("accepted"):
            # Episode returns already counted by the learner; a rejected post
            # keeps them pending so the next accepted one reports them.
            del self._pending_returns[: len(meta["episode_returns"])]
        return reply

    def run_forever(self, poll_interval: float = 1.0) -> None:
        while True:
            try:
                reply = self.run_once()
            except Exception as exc:  # the learner may be restarting
                print(f"[ppo-fleet] actor error, retrying: {exc}", flush=True)
                time.sleep(poll_interval)
                continue
            if not reply.get("accepted"):
                time.sleep(poll_interval)


def learner_target(env: Mapping[str, str]) -> str:
    """Resolve the learner's ``host:port`` for an actor.

    ``PPO_LEARNER`` wins when set. Otherwise the learner is the lowest
    numeric asset id named by ``MESH_PEERS``; a non-numeric peer list raises
    and asks for ``PPO_LEARNER`` explicitly.
    """
    explicit = env.get("PPO_LEARNER", "").strip()
    default_port = int(env.get("MESH_PORT", str(mesh.DEFAULT_MESH_PORT)))
    if explicit:
        return explicit if ":" in explicit else f"{explicit}:{default_port}"
    candidates: dict[int, str] = {}
    for item in env.get("MESH_PEERS", "").split(","):
        item = item.strip()
        if not item:
            continue
        head, _, _ = item.partition(":")
        if not head.isdigit():
            raise ValueError(
                f"cannot locate the learner: MESH_PEERS entry {item!r} is not a "
                "numeric asset id; set PPO_LEARNER to host:port explicitly"
            )
        candidates[int(head)] = item
    if not candidates:
        raise ValueError("cannot locate the learner: MESH_PEERS is empty; set PPO_LEARNER")
    lowest = candidates[min(candidates)]
    (target,) = mesh.parse_peers(lowest, default_port=default_port)
    return target


def main() -> None:
    # Enumerated ${VAR} passthrough in wendy.json can deliver unset variables
    # as empty strings; treat empty as unset so every default applies.
    env = {k: v for k, v in os.environ.items() if v != ""}
    cfg = load_config(DEFAULTS, env=env)
    fleet = mesh.Fleet.from_env(env)
    role = {"coordinator": "learner", "worker": "actor"}.get(fleet.role, fleet.role)
    if role not in ("learner", "actor"):
        raise ValueError(f"unknown role {role!r}; set WT_ROLE to learner, actor, or auto")
    print(
        f"[ppo-fleet] role={role} self={fleet.self_id or '(unset)'} "
        f"peers={fleet.peers} run_id={fleet.run_id}",
        flush=True,
    )
    if role == "learner":
        run = Run(fleet.ckpt_dir, run_id=fleet.run_id, keep_last=int(cfg.run.keep_last))
        learner = Learner(cfg, run, env=env)
        server = serve(learner.routes(), port=fleet.port)
        print(f"[ppo-fleet] learner serving on port {server.server_address[1]}", flush=True)
        learner.run_updates(int(cfg.ppo.total_versions))
        print(f"[ppo-fleet] training complete at version {learner.version}", flush=True)
        while True:  # keep /weights and /status reachable for the fleet
            time.sleep(60)
    else:
        seed = int(fleet.self_id) if fleet.self_id.isdigit() else os.getpid()
        actor = Actor(cfg, learner_target(env), env=env, seed=seed)
        actor.run_forever()


if __name__ == "__main__":
    main()
