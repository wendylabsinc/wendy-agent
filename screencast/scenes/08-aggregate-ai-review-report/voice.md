Attempts from one or many targets aggregate into a run directory that keeps
attempt-level artifacts separate from per-test observations. Aggregation also
extracts each test's source range, including its specification prose, so
review sees both the spec and the runtime transcript.

`make e2e-review` runs a single AI review pass over the aggregate and writes
issue files into the run directory. `make e2e-report` renders the HTML
report, plus a compact review summary that CI can post as a comment.
