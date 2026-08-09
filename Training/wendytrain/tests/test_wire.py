"""Tests for the WTW1 self-describing wire codec."""

import gzip
import json
import struct

import numpy as np
import pytest

from wendytrain import wire


def test_magic_constant():
    assert wire.MAGIC == b"WTW1"


def test_round_trip_mixed_dtypes_and_nested_meta():
    arrays = {
        "theta": np.arange(12, dtype=np.float32).reshape(3, 4),
        "counts": np.array([1, 2, 3], dtype=np.int64),
        "blob": np.frombuffer(b"opaque torch state", dtype=np.uint8),
    }
    meta = {"architecture": {"hidden": [32, 32]}, "generation": 7, "framework": "numpy"}
    blob = wire.encode(arrays, meta)
    out_arrays, out_meta = wire.decode(blob)
    assert out_meta == meta
    assert set(out_arrays) == set(arrays)
    for name, arr in arrays.items():
        out = out_arrays[name]
        assert out.dtype == arr.dtype
        assert out.shape == arr.shape
        assert np.array_equal(out, arr)


def test_round_trip_zero_dimensional_array():
    arrays = {"t": np.array(42, dtype=np.int64)}
    out_arrays, _ = wire.decode(wire.encode(arrays))
    assert out_arrays["t"].shape == ()
    assert out_arrays["t"].dtype == np.int64
    assert out_arrays["t"] == 42


def test_round_trip_empty_array():
    arrays = {"empty": np.zeros((0, 5), dtype=np.float64)}
    out_arrays, _ = wire.decode(wire.encode(arrays))
    assert out_arrays["empty"].shape == (0, 5)
    assert out_arrays["empty"].dtype == np.float64


def test_meta_defaults_to_empty_dict():
    _, meta = wire.decode(wire.encode({"x": np.ones(2, dtype=np.float32)}))
    assert meta == {}


def test_wrong_magic_raises_value_error():
    blob = wire.encode({"x": np.ones(2, dtype=np.float32)})
    corrupted = b"XXXX" + blob[4:]
    with pytest.raises(ValueError):
        wire.decode(corrupted)


def test_truncated_header_raises_value_error():
    blob = wire.encode({"x": np.ones(2, dtype=np.float32)})
    with pytest.raises(ValueError):
        wire.decode(blob[:6])


def test_truncated_payload_raises_value_error():
    blob = wire.encode({"x": np.ones(100, dtype=np.float64)})
    with pytest.raises(ValueError):
        wire.decode(blob[:-20])


def test_payload_length_mismatch_raises_value_error():
    # Rebuild a blob whose header promises more bytes than the payload has.
    arrays = {"x": np.ones(4, dtype=np.float32)}
    blob = wire.encode(arrays)
    header_len = struct.unpack("<I", blob[4:8])[0]
    header = json.loads(blob[8 : 8 + header_len])
    header["arrays"][0]["nbytes"] = 9999
    new_header = json.dumps(header).encode()
    forged = wire.MAGIC + struct.pack("<I", len(new_header)) + new_header + blob[8 + header_len :]
    with pytest.raises(ValueError):
        wire.decode(forged)


def test_unknown_dtype_raises_value_error():
    arrays = {"x": np.ones(4, dtype=np.float32)}
    blob = wire.encode(arrays)
    header_len = struct.unpack("<I", blob[4:8])[0]
    header = json.loads(blob[8 : 8 + header_len])
    header["arrays"][0]["dtype"] = "notadtype"
    new_header = json.dumps(header).encode()
    forged = wire.MAGIC + struct.pack("<I", len(new_header)) + new_header + blob[8 + header_len :]
    with pytest.raises(ValueError):
        wire.decode(forged)


def test_payload_is_gzipped_concatenation_in_header_order():
    a = np.arange(3, dtype=np.float32)
    b = np.arange(4, dtype=np.int64)
    blob = wire.encode({"a": a, "b": b})
    header_len = struct.unpack("<I", blob[4:8])[0]
    payload = gzip.decompress(blob[8 + header_len :])
    assert payload == a.tobytes() + b.tobytes()
