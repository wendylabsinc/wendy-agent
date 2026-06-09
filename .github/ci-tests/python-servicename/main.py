#!/usr/bin/env python3
"""
Integration test for WDY-878: serviceName container-naming convention.

When wendy.json carries a top-level "serviceName" field the agent must:
  - Set WENDY_HOSTNAME to "{serviceName}.local"   (not the device hostname)
  - Set WENDY_APP_GROUP to the appId
  - Set WENDY_APP_ID to the appId

Both multi-service env vars are verified here so that any regression in the
naming path causes an immediate, human-readable CI failure.
"""

import os
import sys

EXPECTED_APP_ID    = "sh.wendy.ci.python-servicename"
EXPECTED_HOSTNAME  = "api.local"
EXPECTED_APP_GROUP = "sh.wendy.ci.python-servicename"

failures = []

# ── WENDY_APP_ID ─────────────────────────────────────────────────────────────
app_id = os.environ.get("WENDY_APP_ID", "")
if app_id != EXPECTED_APP_ID:
    failures.append(
        f"WENDY_APP_ID: got {app_id!r}, want {EXPECTED_APP_ID!r}"
    )
else:
    print(f"OK  WENDY_APP_ID={app_id}")

# ── WENDY_HOSTNAME ────────────────────────────────────────────────────────────
hostname = os.environ.get("WENDY_HOSTNAME", "")
if hostname != EXPECTED_HOSTNAME:
    failures.append(
        f"WENDY_HOSTNAME: got {hostname!r}, want {EXPECTED_HOSTNAME!r} "
        f"(must be {{serviceName}}.local, not the device hostname)"
    )
else:
    print(f"OK  WENDY_HOSTNAME={hostname}")

# ── WENDY_APP_GROUP ───────────────────────────────────────────────────────────
app_group = os.environ.get("WENDY_APP_GROUP", "")
if app_group != EXPECTED_APP_GROUP:
    failures.append(
        f"WENDY_APP_GROUP: got {app_group!r}, want {EXPECTED_APP_GROUP!r}"
    )
else:
    print(f"OK  WENDY_APP_GROUP={app_group}")

# ── Result ────────────────────────────────────────────────────────────────────
if failures:
    print("\nFAIL: multi-service environment variables not injected correctly:")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)

print("\nPASS: serviceName environment variables verified")
