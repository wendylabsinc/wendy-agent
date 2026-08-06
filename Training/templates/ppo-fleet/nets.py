"""Policy and value networks for the ppo-fleet template, plus wire helpers.

PyTorch is imported lazily inside functions so that importing this module
never requires torch; the first call that needs it raises an actionable
``RuntimeError`` naming the missing package when it is absent.

Networks travel over the wire as flat float32 parameter vectors plus the
hidden sizes list in the blob metadata; receivers rebuild the architecture
from that list and never infer it from parameter counts.
"""

from __future__ import annotations

import io

import numpy as np

TORCH_INSTALL_HINT = "pip install torch --index-url https://download.pytorch.org/whl/cpu"


def torch_module():
    """Import and return torch, or raise a ``RuntimeError`` naming the fix."""
    try:
        import torch
    except ImportError as exc:
        raise RuntimeError(
            "the ppo-fleet template needs the torch package, which is not "
            f"installed; install it with: {TORCH_INSTALL_HINT}"
        ) from exc
    return torch


def _build_mlp(in_dim: int, out_dim: int, hidden: list[int]):
    """A tanh Multi-Layer Perceptron mapping ``in_dim`` to ``out_dim``."""
    torch = torch_module()
    layers = []
    last = in_dim
    for width in hidden:
        layers.append(torch.nn.Linear(last, int(width)))
        layers.append(torch.nn.Tanh())
        last = int(width)
    layers.append(torch.nn.Linear(last, out_dim))
    return torch.nn.Sequential(*layers)


def build_policy(obs_dim: int, act_dim: int, hidden: list[int]):
    """Policy network: observation to action mean."""
    return _build_mlp(obs_dim, act_dim, hidden)


def build_value(obs_dim: int, hidden: list[int]):
    """Value network: observation to a scalar state value."""
    return _build_mlp(obs_dim, 1, hidden)


def flat_params(module) -> np.ndarray:
    """Concatenate a module's parameters into one float32 vector."""
    torch = torch_module()
    with torch.no_grad():
        flat = torch.cat([p.reshape(-1) for p in module.parameters()])
    return flat.cpu().numpy().astype(np.float32)


def set_flat_params(module, flat: np.ndarray) -> None:
    """Load a flat float32 vector back into a module's parameters."""
    torch = torch_module()
    tensor = torch.as_tensor(np.asarray(flat, dtype=np.float32).reshape(-1))
    offset = 0
    with torch.no_grad():
        for parameter in module.parameters():
            count = parameter.numel()
            if offset + count > tensor.numel():
                break
            parameter.copy_(tensor[offset : offset + count].reshape(parameter.shape))
            offset += count
    if offset != tensor.numel():
        raise ValueError(
            f"flat parameter vector has {tensor.numel()} values but the module "
            f"needs {sum(p.numel() for p in module.parameters())}; the "
            "architecture in the blob metadata does not match this network"
        )


def serialize_state_dict(state_dict) -> np.ndarray:
    """Serialize a torch state dictionary into a uint8 wire array."""
    torch = torch_module()
    buffer = io.BytesIO()
    torch.save(state_dict, buffer)
    return np.frombuffer(buffer.getvalue(), dtype=np.uint8)


def deserialize_state_dict(blob: np.ndarray):
    """Restore a torch state dictionary from a uint8 wire array."""
    torch = torch_module()
    raw = np.asarray(blob, dtype=np.uint8).tobytes()
    return torch.load(io.BytesIO(raw), weights_only=True)
