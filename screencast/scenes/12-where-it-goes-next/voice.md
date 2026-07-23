The near-term work is mostly about unlocking those disabled specs: seeded
managed-agent state, cloud fixtures, and a PTY harness for interactive
prompts.

On the ergonomics side, two explorations are tracked: structured scoping
traits as a possible replacement for the scenario run call, and thin named
helpers for repeated assertion and config-fixture patterns. Neither is done;
both are open issues.

If you are adding CLI behavior, the expectation is simple: write the spec as
a test, follow the naming and prose conventions, and let the suite be the
documentation.
