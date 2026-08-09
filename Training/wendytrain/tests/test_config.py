"""Tests for the layered configuration loader."""

import sys

import pytest

from wendytrain.config import Config, load_config

DEFAULTS = {
    "es": {"pop": 32, "sigma": 0.1, "lr": 0.02},
    "run": {"max_iterations": 200, "resume": True, "name": "default"},
}


def test_defaults_only():
    cfg = load_config(DEFAULTS, env={})
    assert cfg.es.pop == 32
    assert cfg["es"]["sigma"] == 0.1
    assert cfg.as_dict() == DEFAULTS


def test_file_overrides_defaults(tmp_path):
    path = tmp_path / "config.toml"
    path.write_text("[es]\npop = 48\n")
    cfg = load_config(DEFAULTS, path=str(path), env={})
    assert cfg.es.pop == 48
    assert cfg.es.sigma == 0.1  # untouched default survives a partial section


def test_env_overrides_file_overrides_defaults(tmp_path):
    path = tmp_path / "config.toml"
    path.write_text("[es]\npop = 48\nsigma = 0.3\n")
    cfg = load_config(DEFAULTS, path=str(path), env={"WT_ES__POP": "64"})
    assert cfg.es.pop == 64
    assert isinstance(cfg.es.pop, int)
    assert cfg.es.sigma == 0.3


def test_wt_config_env_variable_names_the_file(tmp_path):
    path = tmp_path / "config.toml"
    path.write_text("[run]\nmax_iterations = 5\n")
    cfg = load_config(DEFAULTS, env={"WT_CONFIG": str(path)})
    assert cfg.run.max_iterations == 5


def test_env_value_parsing_bool_float_string():
    cfg = load_config(
        DEFAULTS,
        env={
            "WT_RUN__RESUME": "false",
            "WT_ES__SIGMA": "0.5",
            "WT_RUN__NAME": "sweep-a",
        },
    )
    assert cfg.run.resume is False
    assert cfg.es.sigma == 0.5
    assert cfg.run.name == "sweep-a"


def test_env_prefix_is_respected():
    cfg = load_config(DEFAULTS, env={"OTHER_ES__POP": "99"})
    assert cfg.es.pop == 32


def test_unknown_keys_are_allowed(tmp_path):
    path = tmp_path / "config.toml"
    path.write_text("[custom]\nknob = 7\n")
    cfg = load_config(DEFAULTS, path=str(path), env={"WT_ES__EXTRA": "1"})
    assert cfg.custom.knob == 7
    assert cfg.es.extra == 1


def test_type_mismatch_against_default_raises_type_error():
    with pytest.raises(TypeError):
        load_config(DEFAULTS, env={"WT_ES__POP": "not-a-number"})


def test_type_mismatch_in_file_raises_type_error(tmp_path):
    path = tmp_path / "config.toml"
    path.write_text('[es]\npop = "large"\n')
    with pytest.raises(TypeError):
        load_config(DEFAULTS, path=str(path), env={})


def test_int_is_accepted_where_default_is_float():
    cfg = load_config(DEFAULTS, env={"WT_ES__SIGMA": "1"})
    assert cfg.es.sigma == 1.0


def test_missing_yaml_dependency_raises_naming_the_package(tmp_path, monkeypatch):
    path = tmp_path / "config.yaml"
    path.write_text("es:\n  pop: 64\n")
    monkeypatch.setitem(sys.modules, "yaml", None)
    with pytest.raises(RuntimeError, match="PyYAML"):
        load_config(DEFAULTS, path=str(path), env={})


def test_config_is_a_config_instance_with_nested_sections():
    cfg = load_config(DEFAULTS, env={})
    assert isinstance(cfg, Config)
    assert isinstance(cfg.es, Config)
    assert cfg.as_dict()["es"] == {"pop": 32, "sigma": 0.1, "lr": 0.02}
