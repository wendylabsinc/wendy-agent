#!/usr/bin/env python3
"""Basic Python deployment test for Wendy CI."""

import platform
import sys

print(f"Hello from Wendy CI!")
print(f"Python {sys.version}")
print(f"Platform: {platform.machine()}")

# The harness reads the app's exit code back from the device, and falls back to
# a printed verdict on agents that record none. Every other test prints one.
print("PASS: containerized Python app ran")
