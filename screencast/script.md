# Swift E2E Tests

Audience: Wendy engineers who will read, run, debug, or add Swift E2E tests.
Goal: Introduce the current Swift E2E system: what it is for, how to run and
extend it, how its artifacts and AI review fit together, and why its
conventions exist.
Tone: Calm, technical, direct.

---

## 01 Title

### Say

Swift end-to-end tests for the Wendy CLI and agent.

In this screencast, we will look at what the Swift E2E system is for, how to
run it, the conventions behind its 925 documented tests, the artifacts every
run produces, and where its boundaries deliberately are today.

### Show (slide)

```text
Swift E2E Tests

executable specs → real wendy binary → recorded evidence

925 documented tests · hosted macOS + Ubuntu · AI-reviewed runs
```

---

## 02 What It Is and Why

### Say

The Swift E2E suite lives in swift slash WendyE2ETests. It runs the real
`wendy` binary, records every shell command, and writes artifacts for local
debugging, CI, and AI review.

The rationale is simple. Tests are executable behavioral specifications: each
one documents a user-visible behavior in prose and then proves it with real
commands. Orchestration is deterministic, so a failure is reproducible.
Failures leave useful evidence: full command recordings and replay scripts,
not just a red X.

And it is one path. Overlapping legacy integration coverage from the older
CI test scripts is expected to move here over time, so orchestration,
assertions, artifacts, and reporting live in one place.

### Show (slide)

```text
swift/WendyE2ETests

1. Executable behavioral specifications
   prose spec + real commands + real assertions

2. Deterministic orchestration
   sandboxed, reproducible runs

3. Useful failure evidence
   recordings, replay scripts, reports

4. One path
   legacy .github/ci-tests coverage migrates here
```

---

## 03 Layout and Entry Points

### Say

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

### Show (slide)

```text
swift/
  WendyE2ETests/          # the test package: 110 files, one suite each
  Scripts/E2ETest.sh      # preferred runner: sandboxes + artifacts
  Makefile

make e2e-test         run locally, write attempt artifacts
make e2e-analyze      aggregate + AI review + HTML report
make e2e-reference    behavioral reference docs from test source

Remote targets:
make e2e-test-wendy DEVICE=wendyos-raspberry-pi-5.local
```

---

## 04 Anatomy of a Spec

### Say

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

### Show (code)

```swift
// WendyInfoTests.swift
@Suite
struct `'wendy info'` {
    let scenario = CLIAndAgentScenario()

    /**
     Reports the Wendy CLI version and local system details useful for
     support, including operating system and architecture. The command does
     not contact devices, cloud services, or update endpoints.
     */
    @Test
    func `prints CLI and system information`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy --json=false info") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Wendy CLI"))
                #expect(result.stdout.contains("Version:"))
                #expect(result.stderr == "")
            }
        }
    }
}
```

---

## 05 Machines, Sessions, and Scenarios

### Say

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

### Show (slide)

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

---

## 06 Live Run: a Focused Suite

### Say

Let's run one suite locally: the wendy info tests.

The runner builds the managed CLI, creates the sandboxes, and executes just
the filtered tests. The `wendy info` suite is purely local: it needs no
device, no cloud account, and no running agent.

At the end, the runner reports where it wrote the attempt directory.

### Show (terminal)

```sh
cd swift

bash Scripts/E2ETest.sh \
  --output-dir ../Build/e2e \
  --filter "wendy info"
```

---

## 07 Attempt Artifacts

### Say

Every run writes one attempt directory. The attempt ID encodes the workflow,
run, target, and attempt number.

At the attempt root sit the attempt metadata and xUnit results. Under
observations there is one directory per test, containing the human-readable
command recording, a replay script that re-executes the captured shell calls
in order, and the test result as JSON.

When a test fails, this is the evidence you debug from: the exact commands,
their output, and a script to replay them.

### Show (terminal)

```sh
tree -a Build/e2e --filelimit 20 | head -30

cat Build/e2e/*/observations/wendy-info/*/recording.md | head -40
```

---

## 08 Aggregate, AI Review, Report

### Say

Attempts from one or many targets aggregate into a run directory that keeps
attempt-level artifacts separate from per-test observations. Aggregation also
extracts each test's source range, including its specification prose, so
review sees both the spec and the runtime transcript.

`make e2e-review` runs a single AI review pass over the aggregate and writes
issue files into the run directory. `make e2e-report` renders the HTML
report, plus a compact review summary that CI can post as a comment.

### Show (slide)

```text
make e2e-aggregate      attempts → run directory
make e2e-review         AI review → review.<reviewer>/<slug>.md
make e2e-report         index.html · review.md · review.html

<run>/
  attempts/<target>/<n>/…
  observations/<suite>/<test>/
    source.md            # spec + test source
    <target>/<n>/recording.md, recording.sh.txt
  source-index.md
```

---

## 09 Behavioral Reference Docs

### Say

Because every test carries specification prose and a sentence-style name, the
suite doubles as a behavioral reference for the whole CLI.

`make e2e-reference` renders static HTML documentation directly from the test
sources, independent of any test run: one page per suite, one entry per
behavior. This is the generated, always-current answer to "what is the CLI
supposed to do here".

### Show (terminal)

```sh
cd swift

make e2e-reference

ls Build/Reference | head
```

---

## 10 No Generic Stubs

### Say

A convention worth calling out: the suite contains zero generic stub markers.

All 925 tests carry real specification prose. 448 of them execute on hosted
runners today. The remaining specs are disabled, but never with a vague
placeholder: each one names a specific reason and a tracking issue.

The biggest groups are honest statements about missing test infrastructure:
simulated managed-agent state for device behaviors, isolated cloud auth and
tunnel fixtures, an interactive PTY harness for prompt-driven flows, and
real hardware. Some encode agreed product behavior that is not implemented
yet, like machine-readable output flags. When the fixture or feature lands,
the spec is already written; it just gets enabled.

### Show (slide)

```text
925 documented tests · 0 generic "SPEC STUB" markers

448 executable on hosted macOS + Ubuntu
477 disabled — each with a specific reason + tracking issue

  WDY-1952  simulated managed-agent device state
  WDY-1949  isolated cloud auth/tunnel fixtures
  INTERACTIVE  PTY prompt harness
  WDY-1909/1934/…  agreed CLI behavior, pending implementation
  hardware  real devices (flashing, cameras, WiFi)
```

---

## 11 CI Boundaries

### Say

In CI, the swift-e2e-tests workflow runs two hosted local routes: macOS 26
and Ubuntu 24. Each builds the CLI, launches a managed local agent — the real
Mac agent app on macOS, the Go daemon on Linux — and runs the executable
suite against it. An analyze job then aggregates both attempts, runs AI
review, and posts the compact summary on pull requests.

Secrets stay out of tests. Authenticated scenarios use a dedicated auth
fixture, never your live wendy config, and fixture-dependent suites like the
legacy integration tests only run in protected, non-fork workflows because
they deploy to real devices.

Physical device routes exist in the workflow as a commented ledger:
macOS-to-Pi, Ubuntu-to-Jetson, and friends. They are deliberately dormant
because the current dedicated devices are too flaky to gate CI, and they stay
dormant until better hardware exists. Re-enabling one is a small,
deliberate uncomment — not a rewrite.

### Show (slide)

```text
swift-e2e-tests.yml

  Local: macOS 26     hosted runner + managed WendyAgentMac
  Local: Ubuntu 24    hosted runner + managed Go agent
  Analyze             aggregate → AI review → PR comment

Boundaries
  auth fixtures only — personal ~/.wendy/config.json never leaks
  legacy integration suite: protected, non-fork workflows only
  physical routes (Pi, Jetson, SER9, …): dormant ledger,
  disabled until reliable CI hardware exists
```

---

## 12 Where It Goes Next

### Say

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

### Show (slide)

```text
Next
  unlock disabled specs: fixtures, cloud, PTY, hardware
  WDY-1962  scoping traits vs Scenario.run()
  WDY-1963  named assertion + config-fixture helpers

Adding behavior?
  write the spec as a test
  one suite per command area · prose before every @Test
  swift/WendyE2ETests/README.md has the full guide
```

---

## 13 Thanks

### Say

Thanks for watching. The full guide lives in the WendyE2ETests README, and
the issues on screen track the open follow-ups. For questions, reach out to
Konstantin at konstantin at wendy dot dev.

### Show (slide)

```text
Thanks for watching

Guide:   swift/WendyE2ETests/README.md
Issues:  WDY-1952 · WDY-1949 · WDY-1962 · WDY-1963

Contact: konstantin@wendy.dev
```
