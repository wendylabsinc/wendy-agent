Everything starts in the swift directory.

`make e2e-test` builds the managed CLI into go slash bin, runs the Swift E2E
tests locally, and writes attempt artifacts under Build slash e2e.

`make e2e-analyze` aggregates attempts, runs the AI review step, and renders
the HTML report. `make e2e-reference` renders behavioral reference
documentation straight from the test sources.

Under the hood, `Scripts/E2ETest.sh` is the preferred runner. It creates
isolated CLI and agent run directories, puts the managed `wendy` binary first
on PATH, passes machine metadata into the Swift tests, and writes xUnit
output, recordings, replay scripts, and report inputs.
