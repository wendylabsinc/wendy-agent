The Swift E2E suite lives in swift slash WendyE2ETests. It runs the real
`wendy` binary, records every shell command, and writes artifacts for local
debugging, CI, and AI review.

The rationale is simple. Tests are executable behavioral specifications: each
one documents a user-visible behavior in prose and then proves it with real
commands. Orchestration is deterministic, so a failure is reproducible.
Failures leave useful evidence: full command recordings and replay scripts,
not just a red X.

And it is one path. Overlapping legacy integration coverage from the older
CI test scripts is expected to move here over time, so orchestration,
assertions, artifacts, and reporting live in one place.
