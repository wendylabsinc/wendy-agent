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
        parts = []
        for w, b in self._ordered():
            parts.append(w.detach().T.reshape(-1))
            parts.append(b.detach().reshape(-1))
        return t.cat(parts).cpu().numpy().astype("float32")

    def set_flat(self, v):
        t = self._torch; v = t.as_tensor(v, dtype=t.float32); i = 0
        with t.no_grad():
            for w, b in self._ordered():
                nin, nout = w.shape[1], w.shape[0]
                n = nin * nout
                w.copy_(v[i:i + n].reshape(nin, nout).T); i += n
                n = b.numel()
                b.copy_(v[i:i + n].reshape(b.shape)); i += n

    def _ordered(self):
        # (weight, bias) per Linear, in declaration order — matches MLPPolicy flat layout.
        # weight is torch's (nout,nin); MLPPolicy stores (nin,nout), so callers
        # transpose when moving data across the boundary.
        params = []
        for m in self.net:
            if hasattr(m, "weight"):
                params.append((m.weight, m.bias))
        return params
