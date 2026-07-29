#!/usr/bin/env python3
"""
Integration test for WDY-2040: `wendy run --env` versus wendy.json env.

The harness deploys this app with:

    --env CI_ENV_LEVEL=debug --env CI_ENV_ONLY_FLAG=1

while wendy.json declares CI_ENV_LEVEL=info and CI_ENV_REGION=us. The flag
overrides a key it names, leaves the others alone, and can introduce keys
wendy.json never mentioned.
"""

import os
import sys

EXPECTED = (
    ("CI_ENV_LEVEL", "debug", "--env overrides the wendy.json value"),
    ("CI_ENV_REGION", "us", "untouched wendy.json value survives"),
    ("CI_ENV_ONLY_FLAG", "1", "--env can add a key wendy.json does not declare"),
)

failures = []
for key, want, why in EXPECTED:
    got = os.environ.get(key)
    if got != want:
        failures.append(f"{key}: got {got!r}, want {want!r} ({why})")
    else:
        print(f"OK  {key}={got}  ({why})")

if failures:
    print("\nFAIL: --env / wendy.json precedence is wrong:")
    for f in failures:
        print(f"  {f}")
    sys.exit(1)

print("\nPASS: --env overrides wendy.json per key and leaves the rest intact")
