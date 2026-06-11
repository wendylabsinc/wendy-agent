# WDY-1509 CLI Surface Audit Plan

## Goal

WDY-1509 is now the umbrella/manual audit issue for the current `wendy` CLI
surface. This worktree preserves the generated command surface dump, the
lightweight Swift E2E surface ledger, and the PR summary that hand off focused
cleanup/reference work to WDY-1511 through WDY-1517.

The coordinator source of truth for the child issue sequence is:

`/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.macos-beta/PLAN.md`

Do not continue broad implementation/reference cleanup from this WDY-1509
worktree.

## Ground Rules

- Keep WDY-1509 small: command surface dump, ledger, handoff notes, and PR body.
- Do not edit Swift E2E stubs/references here; use WDY-1511 through WDY-1517.
- Prefer lightweight notes/ledger updates over broad framework work.
- Keep product fixes out of this PR.
- File additional follow-ups only if the umbrella ledger finds a missing child
  issue in the coordinator plan.
- Keep commits small and bucketed by concern.

## Current Observations

- `go/bin/wendy --experimental-dump-help` is not supported by the current local build.
- A temporary Cobra walker was used to generate `.cli-surface-WDY-1509.json` from `commands.NewRootCmd()`.
- The dump found 135 non-internal commands including hidden deprecated compatibility commands, with 106 leaf commands.
- Hidden/internal command surface observed:
  - `wendy __ble-check` is an internal subprocess helper for CoreBluetooth probing and should stay excluded from user-facing E2E reference coverage.
- Hidden deprecated compatibility commands observed:
  - `wendy device version`
  - `wendy cloud device version`
  - `wendy cloud run`
- Public alias commands observed:
  - `wendy device ps` is surfaced in help as an alias for `wendy device apps list`.
  - `wendy cloud device ps` is surfaced in help as an alias for `wendy cloud device apps list`.
- Cobra aliases observed:
  - `wendy device bluetooth` accepts `bt`.
  - `wendy cloud device bluetooth` accepts `bt`.
- Hidden test seam observed:
  - `wendy completion install --output-dir`, which is misleading because it overrides the home directory used to compute install paths rather than selecting an output directory. Follow-up filed as WDY-1511.
- Hidden/deprecated/public alias policy needs a focused cleanup pass for `device version`, `cloud device version`, `cloud run`, public `ps` aliases, and Bluetooth `bt`. Follow-up filed as WDY-1512.
- WDY-1509 is now the umbrella audit issue. Ordered child issues WDY-1511 through WDY-1517 track focused cleanup/alignment passes.
- `swift/WendyE2ETests/CLI_SURFACE_LEDGER.md` is a starting ledger and child
  issue handoff map, not yet the reviewed source of truth.

## Audit Buckets

Review by bucket, not alphabetically.

### A. Host-only CLI/config commands

Examples:

- `wendy analytics ...`
- `wendy auth ...`
- `wendy cache ...`
- `wendy completion ...`
- `wendy info`
- `wendy init`
- `wendy json ...`
- `wendy mcp ...`
- `wendy project ...`
- `wendy tour`
- `wendy utils open-browser`

Questions:

- Does the command require any agent/device?
- Are Mac vs Linux differences limited to filesystem, shell, browser, or tool detection?
- Do existing stubs accidentally imply device behavior?
- Are JSON/help/no-state-change expectations accurate?

### B. Host OS image-management commands

Examples:

- `wendy os cache ...`
- `wendy os download`
- `wendy os install`
- `wendy os list-drives`

Questions:

- Is the command host-only?
- Which host platforms are supported?
- Does macOS have real behavior, degraded behavior, or explicit unsupported behavior?
- Are destructive install paths clearly gated in reference prose?
- Are JSON/reference expectations truthful for platform-specific drive metadata?

### C. WendyOS OTA/device OS update commands

Example:

- `wendy os update`

Questions:

- Does the command require a WendyOS OTA-capable Linux target?
- What is the expected behavior against a plain Linux agent?
- What is the expected behavior against a Darwin/macOS agent?
- Do stubs explicitly state that macOS agents are unsupported for WendyOS OTA?
- Does failure happen before artifact serving or destructive side effects?

### D. Direct local agent commands

Examples:

- `wendy device info`
- `wendy device apps ...`
- `wendy device logs`
- `wendy device wifi ...`
- `wendy device bluetooth ...`
- `wendy device camera ...`
- `wendy device hardware ...`
- `wendy device volumes ...`
- `wendy device dashboard`
- `wendy device telemetry-stream`

Questions:

- Is this a full Linux/WendyOS support path?
- Does Wendy Agent for Mac support this path?
- If unsupported on Mac, is the expected diagnostic explicit?
- Does the CLI return a macOS beta unsupported message when the agent returns `Unimplemented`?
- Does the existing E2E prose overpromise Mac support?
- Is this practical to automate today, or should it remain a disabled reference stub?

### E. Cloud/tunnel agent commands

Examples:

- `wendy cloud device ...`
- `wendy cloud tunnel`
- hidden `wendy cloud run`

Questions:

- Is behavior the same as the direct route after tunnel establishment?
- Does cloud auth/session validation happen before device mutation?
- Are broker/tunnel errors distinguished from agent errors?
- Do cloud stubs capture auth and tunnel semantics instead of merely duplicating direct-route prose?
- Are hidden cloud aliases represented appropriately?

### F. Build/run commands

Examples:

- `wendy build`
- `wendy run`
- hidden `wendy cloud run`

Questions:

- What host OS build paths are supported?
- What target OS deploy paths are supported?
- Does Darwin target mean native macOS app deployment, not Linux containers?
- Are Linux containers on Mac agents explicitly unsupported?
- Do existing stubs accidentally overpromise Mac container support?
- Which build/run paths are reasonable E2E candidates vs manual/reference-only?

## Ledger Review Process

For every leaf command, track:

```text
command
public/hidden/deprecated?
host-only / direct-agent / cloud-agent / OS-imaging / build-deploy?
Linux/WendyOS expectation
macOS/Darwin expectation
existing Swift E2E suite?
gap/mismatch?
manual sample needed?
follow-up issue needed?
```

Use `.cli-surface-WDY-1509.json` and `swift/WendyE2ETests/CLI_SURFACE_LEDGER.md`
as inputs, but treat the ledger as draft until manually reviewed.

## Cross-Reference Process

For each command bucket, compare against:

- `swift/WendyE2ETests/Tests/WendyE2ETests/`
- `swift/WendyE2ETests/Sources/WendyE2ETesting/`
- `swift/WendyE2ETests/Tests/WendyE2ETestingTests/`
- `swift/WendyE2ETests/README.md`

Check:

- Does a suite exist for the command or is the command intentionally covered by an alias/related suite?
- Does the suite name match the command phrase?
- If the command is hidden/deprecated, is that called out?
- Does the prose distinguish direct local route vs cloud route?
- Does the prose distinguish host-only behavior vs agent-target behavior?
- Are Linux/WendyOS-specific expectations explicit?
- Are macOS/Darwin-supported expectations explicit?
- Are intentionally unsupported macOS beta paths explicit?
- Are impractical-to-automate paths disabled with a useful reason?

## Manual Sampling Strategy

Do not run every command. Sample representative commands where classification or
behavior is unclear.

### Help shape

```sh
cd go
make build-cli
./bin/wendy --help
./bin/wendy device --help
./bin/wendy cloud device --help
./bin/wendy os --help
./bin/wendy run --help
```

Hidden aliases:

```sh
./bin/wendy device version --help
./bin/wendy device ps --help
./bin/wendy cloud device version --help
./bin/wendy cloud device ps --help
./bin/wendy cloud run --help
```

### No-device behavior

```sh
./bin/wendy --json device info
./bin/wendy --json device hardware list
./bin/wendy --json device wifi list
./bin/wendy --json os update
```

### Host-only JSON/help behavior

```sh
./bin/wendy info --json
./bin/wendy cache list --json
./bin/wendy os cache list --json
```

### macOS beta unsupported checks

If a Darwin/macOS agent route is available, sample:

```sh
wendy --device <mac-agent> device hardware list
wendy --device <mac-agent> device wifi list
wendy --device <mac-agent> device wifi status
wendy --device <mac-agent> device camera list
wendy --device <mac-agent> device bluetooth list
wendy --device <mac-agent> os update
```

Goal: confirm diagnostics and side-effect boundaries, not create full automation.

## Edit Strategy

For this WDY-1509 worktree, edit only umbrella artifacts:

1. Preserve `.cli-surface-WDY-1509.json` as the generated command surface
   snapshot.
2. Preserve `swift/WendyE2ETests/CLI_SURFACE_LEDGER.md` as the ledger and child
   issue handoff map.
3. Keep `HANDOVER.md`, this `PLAN.md`, and PR #982 aligned with the coordinator
   plan.

Leave implementation/reference cleanup to the child issues:

- WDY-1511 handles the completion install hidden test seam.
- WDY-1512 handles hidden/deprecated/public alias policy.
- WDY-1513 handles host-only CLI references.
- WDY-1514 handles OS imaging and update references.
- WDY-1515 handles direct device command references.
- WDY-1516 handles cloud-routed device references.
- WDY-1517 handles build and run references.

## Issue Breakdown

WDY-1509 remains the umbrella/manual audit issue. Focused child issues are ordered
as follows in Linear:

1. WDY-1511 — Remove misleading hidden completion install `--output-dir` test seam.
2. WDY-1512 — Audit and align hidden/deprecated CLI aliases.
3. WDY-1513 — Align host-only CLI E2E references.
4. WDY-1514 — Align OS imaging and update E2E references.
5. WDY-1515 — Align direct device command E2E references.
6. WDY-1516 — Align cloud-routed device E2E references.
7. WDY-1517 — Align build and run E2E references.

Use WDY-1509 to keep the command surface ledger and PR summary coherent. Use the
child issues for implementation/reference cleanup so this PR does not grow into
a full E2E rewrite.

## Potential Follow-Up Issues

File additional Linear issues instead of expanding this PR for:

- Missing or broken `--experimental-dump-help` support.
- Misleading command help not already covered by WDY-1511 or WDY-1512.
- Incorrect macOS beta unsupported diagnostics.
- Cloud route behavior that diverges unexpectedly from direct route behavior.
- Hidden aliases that should be removed, documented, or explicitly deprecated beyond WDY-1512 scope.
- JSON contract mismatches.
- Larger E2E framework gaps or missing deterministic seams.

## Validation

For the current umbrella-only artifacts, run lightweight validation:

```sh
python3 -m json.tool .cli-surface-WDY-1509.json >/tmp/cli-surface-WDY-1509.json
python3 - <<'PY'
import json
from pathlib import Path
items = json.loads(Path('.cli-surface-WDY-1509.json').read_text())
commands = {item['command'] for item in items}
leaf = [item for item in items if not any(
    other != item['command'] and other.startswith(item['command'] + ' ')
    for other in commands
)]
assert len(items) == 135
assert len(leaf) == 106
PY
git diff --check
```

If a later WDY-1509 edit touches Swift E2E code or references, run the relevant
Swift tests under `swift/WendyE2ETests` and document the command in the PR body.

## PR Notes to Maintain

The PR body should eventually include:

- What command surface was reviewed.
- How the surface was generated, especially because `--experimental-dump-help` is currently unavailable.
- Which stubs/references changed.
- Which commands/routes remain intentionally manual or out of automation scope.
- Which follow-up Linear issues were filed.
- `Closes WDY-1509`.
