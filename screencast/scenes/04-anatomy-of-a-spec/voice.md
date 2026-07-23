Here is a real test from the suite.

Each file contains exactly one flattened suite named after a command area.
The suite name is the command phrase; the test name completes the behavior
sentence, so this reads: wendy info prints CLI and system information.

Before every test there is a specification block, written as concise product
documentation for the behavior under test. The body then states the precise
requirements with direct assertions: exit status, stdout, stderr, and side
effects.

The reference extractor renders quoted command fragments as code, so the same
sources double as behavioral documentation.
