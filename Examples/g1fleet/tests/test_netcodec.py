import numpy as np
from g1fleet.netcodec import encode_array, decode_array, encode_named, decode_named

def test_array_roundtrip_preserves_shape_dtype():
    a = np.random.default_rng(0).standard_normal((3, 5)).astype(np.float32)
    b = decode_array(encode_array(a))
    assert b.dtype == a.dtype and b.shape == a.shape and np.array_equal(a, b)

def test_named_roundtrip():
    d = {"obs": np.zeros((2, 4), np.float32), "rew": np.arange(2, dtype=np.float32)}
    out = decode_named(encode_named(d))
    assert set(out) == set(d)
    for k in d:
        assert np.array_equal(out[k], d[k]) and out[k].dtype == d[k].dtype
