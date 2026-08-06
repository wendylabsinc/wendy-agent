#!/usr/bin/env python3
"""
Integration test for WDY-2040: top-level "env" in wendy.json.

A single-container app declares its environment at the top level of
wendy.json (there is no services map to hang it off). This verifies the
whole chain: the CLI reads the map, expands ${VAR} against the deploying
machine's environment, and the agent applies the result to the container.

Also covers the documented drop-if-empty rule: an entry whose value expands
to nothing is omitted rather than set to "", so the container keeps whatever
its image defines.

Keys are deliberately NOT WENDY_-prefixed: the agent reserves WENDY_, LD_
and DYLD_ and rejects the deploy outright if a caller sets one.
"""

import os
import sys

failures = []

# ── literal values ───────────────────────────────────────────────────────────
for key, want in (("CI_ENV_COLOR", "blue"), ("CI_ENV_GREETING", "hello world")):
    got = os.environ.get(key)
    if got != want:
        failures.append(f"{key}: got {got!r}, want {want!r}")
    else:
        print(f"OK  {key}={got}")

# ── ${VAR} expanded from the deploying machine ───────────────────────────────
# wendy.json asks for ${HOME}, which the CLI resolves at deploy time. The
# container cannot know the deploy host's value, so assert only that it is a
# non-empty absolute path — enough to prove expansion happened rather than the
# literal "${HOME}" being passed through.
from_host = os.environ.get("CI_ENV_FROM_HOST", "")
if not from_host or not from_host.startswith("/"):
    failures.append(
        f"CI_ENV_FROM_HOST: got {from_host!r}, want an absolute path expanded "
        f"from ${{HOME}} on the deploying machine"
    )
else:
    print(f"OK  CI_ENV_FROM_HOST={from_host}")

# ── entry whose value expands to empty is dropped ────────────────────────────
if "CI_ENV_DROPPED" in os.environ:
    failures.append(
        f"CI_ENV_DROPPED: present as {os.environ['CI_ENV_DROPPED']!r}, want it "
        f"absent (an env value expanding to empty must be dropped, not set to \"\")"
    )
else:
    print("OK  CI_ENV_DROPPED absent (expanded to empty, dropped)")

if failures:
    print("\nFAIL: top-level wendy.json env not applied correctly:")
    for f in failures:
        print(f"  {f}")
    sys.exit(1)

print("\nPASS: top-level wendy.json env applied to the container")
