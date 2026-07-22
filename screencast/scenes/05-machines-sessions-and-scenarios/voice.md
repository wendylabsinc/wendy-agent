Tests run commands through a small session API.

A machine describes a command target: the CLI machine, the agent machine, or
the current host. Sessions run shell commands on a machine, locally or over
SSH when the machine has an address. There is a `pty` variant for commands
whose behavior depends on an interactive terminal, and OS-specific variants
choose POSIX shell or PowerShell per machine.

Scenarios wrap setup and teardown. `CLIAndAgentScenario` creates CLI and
agent sessions, attaches the recorder, installs the managed CLI on PATH,
configures isolated HOME and TMPDIR, and copies a dedicated auth fixture for
authenticated tests. Sandbox isolation defaults to per-test, which keeps
parallel runs safe; nothing leaks between tests and nothing touches your real
machine state.
