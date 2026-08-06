"""Test wiring for the single template.

Puts the template directory on ``sys.path`` so ``import train`` and
``import cartpole`` resolve to ``Training/templates/single/``, exactly as they
do inside the container where every staged file sits in ``/app``.
"""

import sys
from pathlib import Path

TEMPLATE_DIR = Path(__file__).resolve().parent.parent
if str(TEMPLATE_DIR) not in sys.path:
    sys.path.insert(0, str(TEMPLATE_DIR))
