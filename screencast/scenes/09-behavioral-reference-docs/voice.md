Because every test carries specification prose and a sentence-style name, the
suite doubles as a behavioral reference for the whole CLI.

`make e2e-reference` renders static HTML documentation directly from the test
sources, independent of any test run: one page per suite, one entry per
behavior. This is the generated, always-current answer to "what is the CLI
supposed to do here".
