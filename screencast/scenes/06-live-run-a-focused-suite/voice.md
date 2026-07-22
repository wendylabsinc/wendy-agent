Let's run one suite locally: the wendy info tests.

The runner builds the managed CLI, creates the sandboxes, and executes just
the filtered tests. The `wendy info` suite is purely local: it needs no
device, no cloud account, and no running agent.

At the end, the runner reports where it wrote the attempt directory.
