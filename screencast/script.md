# Swift E2E Tests

Audience: Wendy engineers who need to run or add CLI tests.
Goal: Explain the everyday workflow in about two minutes.
Tone: Direct, practical, concise.

---

## 01 Title

### Say

Swift E2E tests for the Wendy CLI and agent.

Here is the everyday workflow: run a focused suite, inspect the evidence, and
add the next behavior.

### Show (slide)

```text
Swift E2E Tests

run → inspect → add coverage
```

---

## 02 The Workflow

### Say

Work from the swift directory.

Use the test runner with a filter while developing. It builds the current
Wendy CLI, isolates test state, and records every command. When the focused
suite is green, analyze the run for the HTML report and AI review. Generate
the reference when you want to browse the behavior documented by the suite.

### Show (slide)

```sh
cd swift

# Run only the command area you are changing
bash Scripts/E2ETest.sh \
  --output-dir ../Build/e2e \
  --filter "wendy info"

make e2e-analyze      # aggregate, review, report
make e2e-reference    # browse documented behavior
```

---

## 03 A Focused Run

### Say

This runs the five `wendy info` behaviors. They are local and unauthenticated,
so they need no cloud account or device.

The same filter works for any command area. Keep the loop small while you work;
run broader coverage before merging.

### Show (terminal)

```sh
cd swift

bash Scripts/E2ETest.sh \
  --output-dir ../Build/e2e-screencast \
  --filter "wendy info"
```

---

## 04 Add a Behavior

### Say

To add coverage, open the test file for the command area and follow the nearest
working example.

Use a sentence-style name, describe the user-visible behavior above the test,
run the real command through the scenario, and assert the outcomes that matter:
status, output, and side effects.

### Show (code)

```swift
@Suite
struct `'wendy info'` {
    let scenario = CLIAndAgentScenario()

    /** Reports CLI and system details without requiring auth or a device. */
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

## 05 What You Get

### Say

Each test leaves a readable command recording, a replay script, and a structured
result. Start with the recording when a test fails; it contains the exact
command, status, stdout, and stderr.

The analysis workflow combines attempts into an HTML report and AI review. The
reference command creates static documentation from the test names and prose.

### Show (slide)

```text
Each behavior
  recording.md       exact command and output
  recording.sh.txt   replay the captured shell calls
  test.json          structured result

Each analyzed run
  index.html          report
  review.md           review summary

Behavioral reference
  Build/Reference/index.html
```

---

## 06 Keep the Boundary Honest

### Say

Hosted CI runs local coverage on macOS and Ubuntu.

Authenticated tests use a dedicated E2E fixture, never personal Wendy config.
Cloud state, interactive prompts, remote targets, and physical hardware need
explicit fixtures or targets. Physical-device CI routes remain disabled until
the hardware is reliable enough to be a useful gate.

### Show (slide)

```text
Hosted by default
  local macOS + Ubuntu

Explicit when needed
  auth fixture · cloud fixture · PTY · remote target · hardware

Never depend on personal auth or machine state.
Physical CI stays dormant until the hardware is reliable.
```

---

## 07 Closing

### Say

That is it: filter to the command you are changing, use the recording when it
fails, and add the next behavior beside the existing ones.

The full guide lives in the WendyE2ETests README.

### Show (slide)

```text
run focused → inspect evidence → add coverage

swift/WendyE2ETests/README.md
```
