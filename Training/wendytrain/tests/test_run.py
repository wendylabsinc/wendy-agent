"""Tests for durable runs: atomic checkpoints, resume, honest metrics."""

import json

import numpy as np

from wendytrain.run import Run


def arrays_for(i: int) -> dict[str, np.ndarray]:
    return {"theta": np.full(4, float(i), dtype=np.float32), "t": np.array(i, dtype=np.int64)}


def test_fresh_directory_returns_none_and_iteration_minus_one(tmp_path):
    run = Run(tmp_path, run_id="r1")
    assert run.load_latest() is None
    assert run.iteration == -1


def test_save_load_round_trip_returns_iteration(tmp_path):
    run = Run(tmp_path, run_id="r1")
    path = run.save_checkpoint(arrays_for(3), {"note": "hi"}, iteration=3)
    assert path.name == "step_000000000003.wtw"
    loaded = run.load_latest()
    assert loaded is not None
    arrays, meta, iteration = loaded
    assert iteration == 3
    assert meta["note"] == "hi"
    assert meta["iteration"] == 3
    assert np.array_equal(arrays["theta"], arrays_for(3)["theta"])
    assert run.iteration == 3


def test_second_run_on_same_directory_resumes(tmp_path):
    Run(tmp_path, run_id="r1").save_checkpoint(arrays_for(7), {}, iteration=7)
    resumed = Run(tmp_path, run_id="r1")
    assert resumed.iteration == 7
    _, _, iteration = resumed.load_latest()
    assert iteration == 7


def test_run_ids_are_isolated(tmp_path):
    Run(tmp_path, run_id="a").save_checkpoint(arrays_for(1), {}, iteration=1)
    assert Run(tmp_path, run_id="b").load_latest() is None


def test_corrupt_latest_pointer_falls_back_to_newest_valid(tmp_path):
    run = Run(tmp_path, run_id="r1")
    for i in (1, 2, 3):
        run.save_checkpoint(arrays_for(i), {}, iteration=i)
    (run.dir / "latest").write_text("step_does_not_exist.wtw")
    arrays, _, iteration = Run(tmp_path, run_id="r1").load_latest()
    assert iteration == 3
    assert np.array_equal(arrays["theta"], arrays_for(3)["theta"])


def test_torn_newest_checkpoint_costs_at_most_one_checkpoint(tmp_path):
    run = Run(tmp_path, run_id="r1")
    for i in (1, 2, 3):
        run.save_checkpoint(arrays_for(i), {}, iteration=i)
    # Simulate a torn write of the newest checkpoint.
    newest = run.dir / "step_000000000003.wtw"
    newest.write_bytes(newest.read_bytes()[:10])
    _, _, iteration = Run(tmp_path, run_id="r1").load_latest()
    assert iteration == 2


def test_pruning_keeps_exactly_keep_last(tmp_path):
    run = Run(tmp_path, run_id="r1", keep_last=3)
    for i in range(8):
        run.save_checkpoint(arrays_for(i), {}, iteration=i)
    kept = sorted(p.name for p in run.dir.glob("step_*.wtw"))
    assert kept == [f"step_{i:012d}.wtw" for i in (5, 6, 7)]
    _, _, iteration = run.load_latest()
    assert iteration == 7


def test_save_is_atomic_no_tmp_left_behind(tmp_path):
    run = Run(tmp_path, run_id="r1")
    for i in range(4):
        run.save_checkpoint(arrays_for(i), {}, iteration=i)
    leftovers = [p.name for p in run.dir.iterdir() if ".tmp" in p.name]
    assert leftovers == []


def test_metrics_lines_are_valid_json_with_injected_keys(tmp_path):
    run = Run(tmp_path, run_id="r1")
    run.save_checkpoint(arrays_for(5), {}, iteration=5)
    run.log_metrics({"mean_return": 12.5, "n_contributed": 40, "population": 60})
    run.log_metrics({"mean_return": 13.0})
    lines = (run.dir / "metrics.jsonl").read_text().splitlines()
    assert len(lines) == 2
    first = json.loads(lines[0])
    assert first["mean_return"] == 12.5
    assert first["n_contributed"] == 40
    assert first["iteration"] == 5
    assert isinstance(first["time"], float)


def test_from_env_reads_documented_variables(tmp_path):
    env = {"WT_CKPT_DIR": str(tmp_path), "WT_RUN_ID": "abc"}
    run = Run.from_env(env)
    assert run.dir == tmp_path / "abc"
    run.save_checkpoint(arrays_for(1), {}, iteration=1)
    assert Run.from_env(env).iteration == 1


def test_from_env_defaults():
    run = Run.from_env({"WT_CKPT_DIR": "/data/checkpoints"})
    assert str(run.dir) == "/data/checkpoints/default"


def test_no_checkpoints_at_all_after_corruption_returns_none(tmp_path):
    run = Run(tmp_path, run_id="r1")
    run.save_checkpoint(arrays_for(1), {}, iteration=1)
    for p in run.dir.glob("step_*.wtw"):
        p.write_bytes(b"garbage")
    assert Run(tmp_path, run_id="r1").load_latest() is None
