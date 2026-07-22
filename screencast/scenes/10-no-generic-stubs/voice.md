A convention worth calling out: the suite contains zero generic stub markers.

All 925 tests carry real specification prose. 448 of them execute on hosted
runners today. The remaining specs are disabled, but never with a vague
placeholder: each one names a specific reason and a tracking issue.

The biggest groups are honest statements about missing test infrastructure:
simulated managed-agent state for device behaviors, isolated cloud auth and
tunnel fixtures, an interactive PTY harness for prompt-driven flows, and
real hardware. Some encode agreed product behavior that is not implemented
yet, like machine-readable output flags. When the fixture or feature lands,
the spec is already written; it just gets enabled.
