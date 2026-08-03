#!/usr/bin/env python3
"""
Integration test for WDY-2040: a service's env overrides the app-level default
key by key — 'beta' half.

beta overrides CI_ENV_OVERRIDE and adds CI_ENV_BETA_ONLY. Overriding one key
must not drop CI_ENV_SHARED, which beta never mentions.

Prints "beta: PASS" / "beta: FAIL" for the harness, which reads both services'
verdicts back from the device logs.
"""

import os
import sys

failures = []

for key, want in (
    ("CI_ENV_SHARED", "from-app"),
    ("CI_ENV_OVERRIDE", "beta-level"),
    ("CI_ENV_BETA_ONLY", "beta-extra"),
):
    got = os.environ.get(key)
    if got != want:
        failures.append(f"{key}: got {got!r}, want {want!r}")
    else:
        print(f"OK  {key}={got}")

if failures:
    for f in failures:
        print(f"  {f}")
    print("beta: FAIL")
    sys.exit(1)

print("beta: PASS")
