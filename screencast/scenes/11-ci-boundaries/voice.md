In CI, the swift-e2e-tests workflow runs two hosted local routes: macOS 26
and Ubuntu 24. Each builds the CLI, launches a managed local agent — the real
Mac agent app on macOS, the Go daemon on Linux — and runs the executable
suite against it. An analyze job then aggregates both attempts, runs AI
review, and posts the compact summary on pull requests.

Secrets stay out of tests. Authenticated scenarios use a dedicated auth
fixture, never your live wendy config, and fixture-dependent suites like the
legacy integration tests only run in protected, non-fork workflows because
they deploy to real devices.

Physical device routes exist in the workflow as a commented ledger:
macOS-to-Pi, Ubuntu-to-Jetson, and friends. They are deliberately dormant
because the current dedicated devices are too flaky to gate CI, and they stay
dormant until better hardware exists. Re-enabling one is a small,
deliberate uncomment — not a rewrite.
