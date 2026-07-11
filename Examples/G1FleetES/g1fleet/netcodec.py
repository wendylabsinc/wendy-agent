"""Gzip-compressed numpy transport for mesh HTTP bodies (uses np.savez)."""
from __future__ import annotations
import gzip, io
import numpy as np


def encode_named(d: dict[str, np.ndarray]) -> bytes:
    buf = io.BytesIO()
    np.savez(buf, **{k: np.ascontiguousarray(v) for k, v in d.items()})
    return gzip.compress(buf.getvalue())


def decode_named(b: bytes) -> dict[str, np.ndarray]:
    with np.load(io.BytesIO(gzip.decompress(b))) as z:
        return {k: z[k] for k in z.files}


def encode_array(a: np.ndarray) -> bytes:
    return encode_named({"_": a})


def decode_array(b: bytes) -> np.ndarray:
    return decode_named(b)["_"]
