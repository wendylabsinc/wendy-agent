"""Layered configuration: defaults, then a file, then environment variables.

Later layers win. The file is Tom's Obvious Minimal Language (TOML) via the
standard library ``tomllib``; YAML Ain't Markup Language (YAML) files are
accepted when PyYAML is importable, and a clear ``RuntimeError`` names the
missing package otherwise. Environment overrides use the pattern
``WT_<SECTION>__<KEY>``; each double underscore descends one level, and values
are parsed as TOML literals with a fallback to plain strings.

Unknown keys in the file or environment that do not exist in the defaults are
allowed, so templates may extend the schema; a type mismatch against an
existing default raises ``TypeError``.
"""

import tomllib
from collections.abc import Mapping
from typing import Any


class Config:
    """Read-only view of a nested configuration dictionary.

    Supports attribute access (``cfg.es.pop``), item access
    (``cfg["es"]["pop"]``), and ``.as_dict()`` for a plain nested dictionary.
    """

    def __init__(self, data: dict[str, Any]):
        object.__setattr__(self, "_data", dict(data))

    def __getattr__(self, name: str) -> Any:
        try:
            return self[name]
        except KeyError:
            raise AttributeError(f"config has no key {name!r}") from None

    def __getitem__(self, name: str) -> Any:
        value = self._data[name]
        if isinstance(value, dict):
            return Config(value)
        return value

    def __contains__(self, name: str) -> bool:
        return name in self._data

    def __repr__(self) -> str:
        return f"Config({self._data!r})"

    def as_dict(self) -> dict[str, Any]:
        """Return the configuration as a plain nested dictionary."""

        def unwrap(value: Any) -> Any:
            if isinstance(value, dict):
                return {k: unwrap(v) for k, v in value.items()}
            return value

        return unwrap(self._data)


def _load_file(path: str) -> dict[str, Any]:
    """Load a TOML or YAML configuration file into a nested dictionary."""
    if path.endswith((".yaml", ".yml")):
        try:
            import yaml
        except ImportError as exc:
            raise RuntimeError(
                f"config file {path!r} is YAML but the PyYAML package is not "
                "installed; install PyYAML or use a .toml file"
            ) from exc
        with open(path, "rb") as fh:
            return yaml.safe_load(fh) or {}
    with open(path, "rb") as fh:
        return tomllib.load(fh)


def _parse_env_value(raw: str) -> Any:
    """Parse an environment value as a TOML literal, falling back to string."""
    try:
        return tomllib.loads(f"v = {raw}")["v"]
    except tomllib.TOMLDecodeError:
        return raw


def _check_type(existing: Any, new: Any, dotted: str) -> Any:
    """Validate ``new`` against the type of an existing default value.

    Returns the value to store; an integer is promoted where the default is a
    float. Raises ``TypeError`` on any other type mismatch.
    """
    if type(new) is type(existing):
        return new
    if isinstance(existing, float) and type(new) is int:
        return float(new)
    raise TypeError(
        f"config key {dotted!r} expects {type(existing).__name__} "
        f"(default {existing!r}), got {type(new).__name__} ({new!r})"
    )


def _merge(base: dict[str, Any], overlay: Mapping[str, Any], prefix: str = "") -> None:
    """Recursively merge ``overlay`` into ``base``, later wins, in place."""
    for key, value in overlay.items():
        dotted = f"{prefix}{key}"
        if key in base:
            existing = base[key]
            if isinstance(existing, dict) and isinstance(value, Mapping):
                _merge(existing, value, prefix=f"{dotted}.")
                continue
            if isinstance(existing, dict) or isinstance(value, Mapping):
                raise TypeError(
                    f"config key {dotted!r} mixes a section with a scalar value"
                )
            base[key] = _check_type(existing, value, dotted)
        else:
            base[key] = _copy_nested(value)


def _copy_nested(value: Any) -> Any:
    """Deep-copy nested mappings so layers never alias the caller's dicts."""
    if isinstance(value, Mapping):
        return {k: _copy_nested(v) for k, v in value.items()}
    return value


def _env_overlay(env: Mapping[str, str], env_prefix: str) -> dict[str, Any]:
    """Build a nested overlay from ``<prefix><SECTION>__<KEY>`` variables.

    Only variables containing a double underscore after the prefix are
    configuration overrides; single-level contract variables such as
    ``WT_RUN_ID`` and ``WT_CONFIG`` are left to their own consumers.
    """
    overlay: dict[str, Any] = {}
    for name, raw in env.items():
        if not name.startswith(env_prefix):
            continue
        rest = name[len(env_prefix):]
        if "__" not in rest:
            continue
        parts = [p.lower() for p in rest.split("__")]
        node = overlay
        for part in parts[:-1]:
            node = node.setdefault(part, {})
        node[parts[-1]] = _parse_env_value(raw)
    return overlay


def load_config(
    defaults: dict,
    path: str | None = None,
    env: Mapping[str, str] | None = None,
    env_prefix: str = "WT_",
) -> Config:
    """Load configuration with layering; later layers win.

    Layers, in order: ``defaults``; the file at ``path`` (or at
    ``env["WT_CONFIG"]`` when ``path`` is None); environment variables of the
    form ``<env_prefix><SECTION>__<KEY>``. When ``env`` is None the process
    environment is used.
    """
    if env is None:
        import os

        env = os.environ
    merged: dict[str, Any] = {}
    _merge(merged, defaults)
    if path is None:
        path = env.get("WT_CONFIG") or None
    if path:
        _merge(merged, _load_file(path))
    _merge(merged, _env_overlay(env, env_prefix))
    return Config(merged)
