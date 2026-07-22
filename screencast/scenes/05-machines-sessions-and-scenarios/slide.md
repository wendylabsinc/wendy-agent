# Machines, Sessions, and Scenarios

```text
WendyE2EMachine     .cli  .agent  .current   (local or SSH)

cli.sh("wendy --version")                    # must succeed
cli.sh("wendy device info") { result in … }  # assert on result
cli.pty("wendy device info") { … }           # interactive terminal
agent.sh(posix: "…", power: "…")             # per-OS variants

CLIAndAgentScenario
  managed wendy on PATH · isolated HOME/TMPDIR
  recorder attached · auth fixture copied
  before/after hooks · isolation: per-test | per-run | none
```
