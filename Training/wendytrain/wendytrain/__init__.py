"""wendytrain: framework-agnostic core library for training on WendyOS devices.

Every module stands alone; import only what you want. The core imports only the
Python standard library and NumPy.
"""

__version__ = "0.1.0"

from . import wire
from .config import Config, load_config
from .manifest import ManifestError, verify_manifest, write_manifest
from .mesh import Fleet, derive_role, http_get, http_post, parse_peers, worker_slice
from .run import Run
from .service import serve

__all__ = [
    "wire",
    "Config",
    "load_config",
    "Run",
    "Fleet",
    "derive_role",
    "http_get",
    "http_post",
    "parse_peers",
    "worker_slice",
    "ManifestError",
    "verify_manifest",
    "write_manifest",
    "serve",
]

