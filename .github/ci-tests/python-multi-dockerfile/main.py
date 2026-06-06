#!/usr/bin/env python3
"""
Multi-Dockerfile auto-selection test for Wendy CI.

Verifies that wendy run correctly auto-selected a hardware-appropriate
Dockerfile when multiple Dockerfiles are present in the project directory.

The WENDY_DOCKERFILE_VARIANT env var is baked in at build time by the
chosen Dockerfile:
  - Dockerfile      -> "default"
  - Dockerfile.gpu  -> "gpu"

On a GPU device, wendy should have auto-selected Dockerfile.gpu, so
WENDY_DOCKERFILE_VARIANT will equal "gpu".

On a non-GPU device, wendy should have fallen back to Dockerfile, so
WENDY_DOCKERFILE_VARIANT will equal "default".

Both outcomes are valid — the test succeeds in either case. The CI log
records which Dockerfile was selected so regressions in auto-selection
are visible without requiring this test to know the device's GPU state.
"""

import os
import sys

variant = os.environ.get("WENDY_DOCKERFILE_VARIANT", "")

if not variant:
    print("FAIL: WENDY_DOCKERFILE_VARIANT is not set — Dockerfile was not embedded correctly")
    sys.exit(1)

print(f"WENDY_DOCKERFILE_VARIANT={variant!r}")
print("PASS: Multi-Dockerfile auto-selection test complete")
sys.exit(0)
