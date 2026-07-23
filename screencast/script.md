# Using Swift E2E Tests

Audience: Wendy engineers who need to run, debug, or add CLI behavior tests.
Goal: Show the shortest useful workflow first, then reveal the next tool only
when it becomes relevant.
Tone: Calm, practical, concise.

---

## 01 Title

### Say

Using Swift E2E tests.

This is a quick working tour: run one focused behavior, follow the evidence
when something fails, and add coverage without needing to understand the whole
harness first.

### Show (slide)

```text
Using Swift E2E Tests

run a focused behavior → inspect evidence → add coverage
```

---

## 02 Run the Smallest Useful Slice

### Say

Start in the swift directory and filter to the command area you are changing.

The preferred runner builds the current Wendy CLI, creates isolated test state,
and runs only matching behaviors. Here we run the five local `wendy info`
tests. They need no cloud account or device.

Use more filters as your change grows. Run the full suite only when the focused
slice is green.

### Show (terminal)

```sh
cd swift

bash Scripts/E2ETest.sh \
  --output-dir ../Build/e2e-screencast \
  --filter "wendy info"
```

---

## 03 Follow the Evidence

### Say

A run writes an attempt directory under Build slash e2e. Start with the
recording for the failed behavior.

`recording.md` shows the command, exit status, stdout, and stderr.
`recording.sh.txt` replays the captured shell calls when you need to reproduce
the failure manually. The xUnit and JSON files are there for tools and CI.

The important point is that a failure leaves the exact evidence you need; you
do not have to reconstruct the command from a log fragment.

### Show (terminal)

```sh
find Build/e2e-screencast/<attempt> -type f

# Per behavior:
recording.md       # readable evidence
recording.sh.txt   # replay commands
test.json          # structured result
```

---

## 04 Turn Runs into a Report

### Say

When you have one or more attempts, run the analysis workflow.

It groups attempts by behavior, keeps each target's evidence separate, reviews
the results, and renders an HTML report. Open the report for the overview, then
drill into a behavior's source and recordings only when something needs
attention.

You can also run aggregation, review, and reporting as separate steps while
debugging that pipeline.

### Show (slide)

```text
make e2e-analyze

        aggregate attempts
                 ↓
          review results
                 ↓
          open HTML report

Need one step?
make e2e-aggregate · make e2e-review · make e2e-report
```

---

## 05 Add One Behavior

### Say

To add coverage, find the test file for the command area and follow the nearest
working example.

Keep one suite per command area. Give the test a sentence-style name, write the
user-visible behavior above it, run commands through the scenario, and assert
only the outcomes that matter: status, output, and side effects.

Start directly. Extract a named helper only when the same assertion pattern is
actually repeating.

### Show (code)

```swift
@Suite
struct `'wendy info'` {
    let scenario = CLIAndAgentScenario()

    /**
     Reports the CLI version and local system details without requiring
     a project, device, or cloud account.
     */
    @Test
    func `prints CLI and system information`() async throws {
        try await scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy --json=false info") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Wendy CLI"))
                #expect(result.stderr.isEmpty)
            }
        }
    }
}
```

---

## 06 Choose the Right Environment

### Say

Stay on the simplest route that can prove the behavior.

Local, unauthenticated command behavior should use the default isolated
scenario. Authenticated tests use a dedicated E2E config fixture, never your
personal Wendy config. Device or remote-host behavior uses the explicit target
variables and make targets documented in the package README.

If a behavior genuinely needs cloud state, an interactive prompt harness, or
physical hardware that is not reliable in CI, do not fake it into the hosted
path. Keep that dependency explicit and tracked.

### Show (slide)

```text
Choose the smallest honest environment

local CLI behavior       → isolated default scenario
authenticated behavior   → dedicated E2E auth fixture
remote/device behavior   → explicit DEVICE / target variables
cloud, PTY, hardware     → explicit fixture or tracked dependency

Never use personal auth or machine state as a test fixture.
```

---

## 07 Browse the Behavioral Reference

### Say

When you want to discover existing coverage before writing code, generate the
behavioral reference.

It renders one static page per command suite from the test names and their
specification prose. Use it to find the command area, understand the expected
behavior, and jump back to the source you should extend.

### Show (terminal)

```sh
cd swift
make e2e-reference

# Build/Reference/index.html
```

---

## 08 Closing

### Say

That is the everyday workflow: filter to the behavior you are changing, use the
recording when it fails, analyze broader runs when needed, and extend the
nearest command suite.

The full command and environment reference lives in the WendyE2ETests README.

### Show (slide)

```text
run focused → inspect evidence → analyze when needed → add behavior

Full guide:
swift/WendyE2ETests/README.md
```
