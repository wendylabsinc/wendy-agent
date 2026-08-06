"""wendytrain: framework-agnostic core library for training on WendyOS devices.

Every module stands alone; import only what you want. The core imports only the
Python standard library and NumPy.
"""

__version__ = "0.1.0"

from . import wire
from .config import Config, load_config

__all__ = ["wire", "Config", "load_config"]

