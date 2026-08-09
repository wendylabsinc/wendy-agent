"""WTW1: a self-describing wire format for named NumPy arrays plus metadata.

Layout of an encoded blob:

    MAGIC (4 bytes, b"WTW1")
    header length (4 bytes, unsigned little-endian integer)
    header (JavaScript Object Notation (JSON), UTF-8)
    payload (gzip-compressed concatenation of each array's raw bytes)

The header is ``{"meta": {...}, "arrays": [{"name", "dtype", "shape",
"offset", "nbytes"}, ...]}``. The payload concatenates each array's
``tobytes()`` in header order, so any language with gzip and JSON can produce
or parse the format. Because every array carries its own dtype and shape,
receivers never infer architecture from parameter counts.
"""

import gzip
import json
import struct

import numpy as np

MAGIC = b"WTW1"

_LEN_STRUCT = struct.Struct("<I")


def encode(arrays: dict[str, np.ndarray], meta: dict | None = None) -> bytes:
    """Encode named arrays and an open metadata object into a WTW1 blob.

    ``meta`` must be JSON-serializable; it defaults to an empty object. Arrays
    round-trip exactly, including zero-dimensional and empty arrays and
    ``uint8`` blobs used to carry opaque bytes such as serialized torch state.
    """
    entries = []
    chunks = []
    offset = 0
    for name, array in arrays.items():
        array = np.asarray(array)
        raw = array.tobytes()  # always C-order bytes, regardless of array layout
        entries.append(
            {
                "name": name,
                "dtype": array.dtype.str,
                "shape": list(array.shape),
                "offset": offset,
                "nbytes": len(raw),
            }
        )
        chunks.append(raw)
        offset += len(raw)
    header = json.dumps({"meta": meta if meta is not None else {}, "arrays": entries}).encode()
    payload = gzip.compress(b"".join(chunks))
    return MAGIC + _LEN_STRUCT.pack(len(header)) + header + payload


def decode(blob: bytes) -> tuple[dict[str, np.ndarray], dict]:
    """Decode a WTW1 blob into ``(arrays, meta)``.

    Raises ``ValueError`` on a wrong magic, a truncated header or payload, a
    header and payload length mismatch, or an unknown dtype.
    """
    if len(blob) < len(MAGIC) + _LEN_STRUCT.size:
        raise ValueError("wire: blob too short to contain the WTW1 header")
    if blob[: len(MAGIC)] != MAGIC:
        raise ValueError(f"wire: bad magic {blob[:len(MAGIC)]!r}, expected {MAGIC!r}")
    (header_len,) = _LEN_STRUCT.unpack_from(blob, len(MAGIC))
    header_start = len(MAGIC) + _LEN_STRUCT.size
    header_end = header_start + header_len
    if len(blob) < header_end:
        raise ValueError("wire: truncated header")
    try:
        header = json.loads(blob[header_start:header_end])
    except json.JSONDecodeError as exc:
        raise ValueError(f"wire: header is not valid JSON: {exc}") from exc
    try:
        payload = gzip.decompress(blob[header_end:])
    except (OSError, EOFError) as exc:
        raise ValueError(f"wire: payload is not valid gzip data: {exc}") from exc
    arrays: dict[str, np.ndarray] = {}
    for entry in header["arrays"]:
        try:
            dtype = np.dtype(entry["dtype"])
        except TypeError as exc:
            raise ValueError(f"wire: unknown dtype {entry['dtype']!r}") from exc
        start = entry["offset"]
        end = start + entry["nbytes"]
        if end > len(payload):
            raise ValueError(
                f"wire: array {entry['name']!r} claims bytes [{start}, {end}) "
                f"but the payload has only {len(payload)} bytes"
            )
        shape = tuple(entry["shape"])
        expected = dtype.itemsize * int(np.prod(shape, dtype=np.int64))
        if expected != entry["nbytes"]:
            raise ValueError(
                f"wire: array {entry['name']!r} shape {shape} and dtype {dtype} "
                f"imply {expected} bytes, header says {entry['nbytes']}"
            )
        arrays[entry["name"]] = np.frombuffer(payload[start:end], dtype=dtype).reshape(shape).copy()
    return arrays, header["meta"]
