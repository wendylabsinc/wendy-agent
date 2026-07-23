To add coverage, find the test file for the command area and follow the nearest
working example.

Keep one suite per command area. Give the test a sentence-style name, write the
user-visible behavior above it, run commands through the scenario, and assert
only the outcomes that matter: status, output, and side effects.

Start directly. Extract a named helper only when the same assertion pattern is
actually repeating.
