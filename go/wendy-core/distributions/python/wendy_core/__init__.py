"""Thin Core interop. Product conveniences belong in the Wendy Python SDK."""
from ctypes import CDLL, c_int32
from pathlib import Path
from sys import platform

__all__ = ["WendyHelloWorld"]

_filename = {"darwin": "libWendyCore.dylib", "linux": "libWendyCore.so", "win32": "WendyCore.dll"}.get(platform)
if _filename is None:
    raise ImportError(f"Wendy Core does not support {platform}")
_library = CDLL(str(Path(__file__).parent / "native" / _filename))
_hello = _library.WendyHelloWorld
_hello.argtypes = []
_hello.restype = c_int32


def WendyHelloWorld() -> int:
    """Return 42 from the shared Go engine."""
    return _hello()
