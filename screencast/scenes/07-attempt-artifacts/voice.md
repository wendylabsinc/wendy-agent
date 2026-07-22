Every run writes one attempt directory. The attempt ID encodes the workflow,
run, target, and attempt number.

At the attempt root sit the attempt metadata and xUnit results. Under
observations there is one directory per test, containing the human-readable
command recording, a replay script that re-executes the captured shell calls
in order, and the test result as JSON.

When a test fails, this is the evidence you debug from: the exact commands,
their output, and a script to replay them.
