"""Tests for the sweep template: fan-out over independent runs.

The sweep and single templates both have a ``train.py``, so the sweep modules
are loaded from their file paths under unique module names; ``sweep_train``
resolves the single template's ``train_loop`` through its own fallback import.
Everything runs in-process on the loopback interface; no device, no Docker.
"""

import importlib.util
import json
import socket
import sys
from pathlib import Path

import numpy as np
import pytest

from wendytrain import Run, wire

TEMPLATE_DIR = Path(__file__).resolve().parent.parent


def _load_module(name: str, path: Path):
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


sweep_train = _load_module("sweep_train", TEMPLATE_DIR / "train.py")
sweep_collect = _load_module("sweep_collect", TEMPLATE_DIR / "collect.py")

PARAMS = [{"seed": 1}, {"seed": 2}]

FAST_ENV = {
    "WT_ES__POP": "4",
    "WT_ES__SIGMA": "0.1",
    "WT_ES__LR": "0.02",
    "WT_POLICY__HIDDEN": "[8]",
    "WT_RUN__MAX_ITERATIONS": "3",
    "WT_RUN__CHECKPOINT_EVERY": "3",
    "WT_SWEEP_PARAMS": json.dumps(PARAMS),
}


def member_env(tmp_path, index: int) -> dict:
    return {
        **FAST_ENV,
        "WT_CKPT_DIR": str(tmp_path),
        "WT_RUN_ID": "sweeptest",
        "WT_SWEEP_INDEX": str(index),
    }


def closed_port() -> int:
    """A loopback port that nothing is listening on."""
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class TestResolveParams:
    def test_picks_the_indexed_parameter_set(self):
        env = {"WT_SWEEP_INDEX": "1", "WT_SWEEP_PARAMS": json.dumps(PARAMS)}
        index, params = sweep_train.resolve_params(env)
        assert index == 1
        assert params == {"seed": 2}

    def test_defaults_to_index_zero_and_no_params(self):
        assert sweep_train.resolve_params({}) == (0, {})

    def test_index_out_of_range_raises(self):
        env = {"WT_SWEEP_INDEX": "2", "WT_SWEEP_PARAMS": json.dumps(PARAMS)}
        with pytest.raises(ValueError, match="out of range"):
            sweep_train.resolve_params(env)

    def test_invalid_json_raises(self):
        with pytest.raises(ValueError, match="JSON"):
            sweep_train.resolve_params({"WT_SWEEP_PARAMS": "not json"})

    def test_non_list_payload_raises(self):
        with pytest.raises(ValueError, match="list"):
            sweep_train.resolve_params({"WT_SWEEP_PARAMS": "{}"})


class TestApplyParams:
    def cfg(self):
        from wendytrain import load_config

        return load_config(sweep_train.single_train.DEFAULTS, env={})

    def test_bare_key_resolves_to_its_unique_section(self):
        cfg = sweep_train.apply_params(self.cfg(), {"seed": 7})
        assert cfg.run.seed == 7

    def test_dotted_key_descends_explicitly(self):
        cfg = sweep_train.apply_params(self.cfg(), {"es.lr": 0.05})
        assert cfg.es.lr == pytest.approx(0.05)

    def test_integer_promotes_to_float_default(self):
        cfg = sweep_train.apply_params(self.cfg(), {"es.lr": 1})
        assert cfg.es.lr == 1.0

    def test_unknown_bare_key_raises(self):
        with pytest.raises(ValueError, match="dotted"):
            sweep_train.apply_params(self.cfg(), {"velocity": 3})

    def test_ambiguous_bare_key_raises(self):
        from wendytrain.config import Config

        ambiguous = Config({"a": {"seed": 1}, "b": {"seed": 2}})
        with pytest.raises(ValueError, match="ambiguous"):
            sweep_train.apply_params(ambiguous, {"seed": 3})

    def test_type_mismatch_raises(self):
        with pytest.raises(TypeError, match="pop"):
            sweep_train.apply_params(self.cfg(), {"es.pop": "many"})

    def test_dotted_key_may_extend_the_schema(self):
        cfg = sweep_train.apply_params(self.cfg(), {"run.note_tag": "a"})
        assert cfg.run.note_tag == "a"


class TestTrainMember:
    def test_two_members_with_different_seeds_produce_distinct_results(self, tmp_path):
        result_0 = sweep_train.train_member(member_env(tmp_path, 0), workers=1)
        result_1 = sweep_train.train_member(member_env(tmp_path, 1), workers=1)
        assert result_0["run_id"] == "sweeptest-0"
        assert result_1["run_id"] == "sweeptest-1"
        assert result_0["params"] == {"seed": 1}
        assert result_1["params"] == {"seed": 2}
        assert result_0["sweep_index"] == 0
        assert result_1["sweep_index"] == 1
        assert result_0["iterations"] == 3
        theta_0 = wire.decode((Run(tmp_path, "sweeptest-0").dir / "policy.wtw").read_bytes())[0]["theta"]
        theta_1 = wire.decode((Run(tmp_path, "sweeptest-1").dir / "policy.wtw").read_bytes())[0]["theta"]
        assert not np.array_equal(theta_0, theta_1)

    def test_finished_member_serves_its_result(self, tmp_path):
        result = sweep_train.train_member(member_env(tmp_path, 0), workers=1)
        server = sweep_train.serve_result(result, port=0, host="127.0.0.1")
        try:
            port = server.server_address[1]
            payload = sweep_collect.fetch_result(f"127.0.0.1:{port}")
            assert payload["run_id"] == "sweeptest-0"
            assert payload["params"] == {"seed": 1}
            assert payload["iterations"] == 3
            assert isinstance(payload["final_mean_return"], float)
        finally:
            server.shutdown()


class TestCollect:
    def serve(self, result):
        return sweep_train.serve_result(result, port=0, host="127.0.0.1")

    def test_aggregates_and_sorts_by_score(self):
        low = {"run_id": "r-low", "final_mean_return": 10.0, "iterations": 3, "params": {"seed": 1}}
        high = {"run_id": "r-high", "final_mean_return": 20.0, "iterations": 3, "params": {"seed": 2}}
        server_low, server_high = self.serve(low), self.serve(high)
        try:
            targets = [
                f"127.0.0.1:{server_low.server_address[1]}",
                f"127.0.0.1:{server_high.server_address[1]}",
            ]
            rows = sweep_collect.collect(targets, timeout_s=10.0, poll_interval_s=0.05)
            assert [row["status"] for row in rows] == ["ok", "ok"]
            assert [row["run_id"] for row in rows] == ["r-high", "r-low"]
        finally:
            server_low.shutdown()
            server_high.shutdown()

    def test_unreachable_member_is_recorded_not_fatal(self):
        alive = {"run_id": "r-alive", "final_mean_return": 5.0, "iterations": 3, "params": {}}
        server = self.serve(alive)
        try:
            dead_target = f"127.0.0.1:{closed_port()}"
            targets = [dead_target, f"127.0.0.1:{server.server_address[1]}"]
            rows = sweep_collect.collect(targets, timeout_s=1.0, poll_interval_s=0.1)
            assert len(rows) == 2
            assert rows[0]["status"] == "ok"
            assert rows[0]["run_id"] == "r-alive"
            assert rows[1] == {"target": dead_target, "status": "unreachable"}
        finally:
            server.shutdown()

    def test_write_results_persists_the_sorted_table(self, tmp_path):
        rows = [
            {"target": "a:1", "status": "ok", "run_id": "r", "final_mean_return": 1.0},
            {"target": "b:2", "status": "unreachable"},
        ]
        path = sweep_collect.write_results(rows, tmp_path / "results.json")
        assert json.loads(path.read_text()) == rows
