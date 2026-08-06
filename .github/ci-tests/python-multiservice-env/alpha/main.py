#!/usr/bin/env python3
"""
Integration test for WDY-2040: app-level env is the default for a service
that declares none — 'alpha' half.

alpha sets no env of its own, so it takes both app-level keys as-is, and must
not see the key beta declares.

Prints "alpha: PASS" / "alpha: FAIL" for the harness, which reads both
services' verdicts back from the device logs.
"""

import os
import sys

failures = []

for key, want in (("CI_ENV_SHARED", "from-app"), ("CI_ENV_OVERRIDE", "app-level")):
    got = os.environ.get(key)
    if got != want:
        failures.append(f"{key}: got {got!r}, want {want!r}")
    else:
        print(f"OK  {key}={got}")

if "CI_ENV_BETA_ONLY" in os.environ:
    failures.append(
        f"CI_ENV_BETA_ONLY: present as {os.environ['CI_ENV_BETA_ONLY']!r}, want "
        f"absent (a key declared only by beta must not reach alpha)"
    )
else:
    print("OK  CI_ENV_BETA_ONLY absent")

if failures:
    for f in failures:
        print(f"  {f}")
    print("alpha: FAIL")
    sys.exit(1)

print("alpha: PASS")
