# E2E Tests — Work Plan

## Project policy

This plan tracks the Linear project **E2E Tests** and its issues. Keep this
master plan focused on coordination and handoff. Do not implement issue work in
this worktree unless the user explicitly asks for a planning-only update here.

For each issue, prefer the smallest useful change that improves E2E signal,
reliability, debuggability, or coverage. Avoid broad test-framework rewrites,
large unrelated refactors, and drive-by cleanup unless an issue explicitly calls
for them.

## Working protocol

For each issue we start, this master session only prepares the workspace. It
must not do the actual issue implementation.

1. Assign the Linear issue to `konstantin@wendy.sh`.
2. Fetch `origin/main` and create a dedicated git worktree/branch from the
   freshly fetched `origin/main`, unless the issue explicitly requires a
   different base.
3. Add an empty setup commit for the issue.
4. Push the branch.
5. Create a draft PR from the setup commit using a real markdown body file,
   not an inline string with escaped newlines.
6. Include the Linear issue link/closing reference in the PR body, for example
   `Closes WDY-1234`, so merging the PR closes the issue.
7. Write a `HANDOVER.md` file into the issue worktree. Put the real prompt
   content there: scope, E2E guidance, validation, commit/push expectations,
   PR details, and known constraints.
8. Leave the user with the worktree path, PR link, and a one-line command to
   resume from that worktree using a generic `--prompt`, for example:

   ```sh
   cd /path/to/worktree && ai --prompt "Read HANDOVER.md and follow its instructions."
   ```

   Keep issue-specific detail out of the `--prompt` argument so future resume
   commands stay short and all durable context lives in `HANDOVER.md`.

Implementation, validation, review-thread handling, and non-empty commits happen
in per-issue worktree sessions, not in this master planning session.

## Issue ledger

### WDY-1510 — Re-enable Raspberry Pi physical Swift E2E route

- Status: `canceled`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1510/re-enable-raspberry-pi-physical-swift-e2e-route
- Linear assignee: unassigned
- Linear state: Canceled (`canceled`)
- Branch/worktree name: `kb.wdy-1510-rpi-swift-e2e`
- Worktree path: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1510-rpi-swift-e2e`
- PR: https://github.com/wendylabsinc/WendyOS/pull/983 (closed without merge)
- PR closing reference: `Closes WDY-1510`
- Scope: investigate whether Raspberry Pi 5 physical Swift E2E can be enabled
  now that hosted local routes, route gating, and restored SER9/Jetson physical
  routes are in place. Start with the smallest useful Raspberry Pi route
  (likely macOS 26 → Raspberry Pi 5 if the macOS runner can reach the device),
  keep it gated behind hosted Ubuntu local E2E, and preserve the commented route
  ledger for unavailable routes.
- Outcome: closed without merge because no Raspberry Pi 5 is currently
  available on CI. A Linear comment records this, the temporary repository
  variable `SWIFT_E2E_RASPBERRY_PI_5_DEVICE_ADDRESS` was removed, and Swift E2E
  workflow run `27362744824` was canceled.
- Validation: local YAML parse and actionlint passed before cancellation; CI did
  not need to finish because the route is not currently actionable.
- Resume command:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1510-rpi-swift-e2e && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

### WDY-1559 — Investigate Jetson Orin Nano Swift E2E preflight timeout

- Status: `in progress`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1559/investigate-jetson-orin-nano-swift-e2e-preflight-timeout
- Linear assignee: `konstantin@wendy.sh`
- Linear state: In Progress (`started`)
- Branch/worktree name: `kb.wdy-1559-jetson-e2e-timeout`
- Worktree path: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1559-jetson-e2e-timeout`
- PR: https://github.com/wendylabsinc/WendyOS/pull/1033 (draft setup PR)
- PR closing reference: `Closes WDY-1559`
- Setup commit: `392e8c8`
- Scope: investigate and restore the physical `macOS 26 → Jetson Orin Nano`
  Swift E2E route after the CLI auth fixture timed out against
  `wendyos-strong-dunlin.local` before Swift Testing launched.
- Triggering run: https://github.com/wendylabsinc/WendyOS/actions/runs/27543175882
- Validation: operational/device fix should be validated with a rerun of the
  physical Jetson Swift E2E job or workflow; run YAML/actionlint/shell checks if
  workflow/script files change.
- Resume command:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1559-jetson-e2e-timeout && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

### WDY-1558 — Mark failed Swift E2E attempts without observations as failed

- Status: `in progress`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1558/mark-failed-swift-e2e-attempts-without-observations-as-failed
- Linear assignee: `konstantin@wendy.sh`
- Linear state: In Progress (`started`)
- Branch/worktree name: `kb.wdy-1558-e2e-attempt-failures`
- Worktree path: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1558-e2e-attempt-failures`
- PR: https://github.com/wendylabsinc/WendyOS/pull/1032 (draft setup PR)
- PR closing reference: `Closes WDY-1558`
- Setup commit: `09f75c4`
- Scope: fix Swift E2E report/overview classification for preflight or setup
  failures that produce an attempt-level artifact with `attempt.json.exitStatus`
  non-zero but no per-test observations. These should be shown as failed target
  attempts, not unknown empty target rows.
- Triggering run: https://github.com/wendylabsinc/WendyOS/actions/runs/27543175882
  failed in `macOS 26 → Jetson Orin Nano` because the CLI auth fixture timed
  out against `wendyos-strong-dunlin.local`; the AI diagnosis was correct but
  the target overview showed `macos-jetson-orin-nano` as Unknown.
- Validation: add regression coverage for an attempt-only failure and run
  targeted `RunOverviewTests` / `ReportCommandTests`.
- Resume command:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1558-e2e-attempt-failures && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

### WDY-1519 — Add IPv4 fallback preflight for physical Swift E2E targets

- Status: `backlog`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1519/add-ipv4-fallback-preflight-for-physical-swift-e2e-targets
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Backlog (`backlog`)
- Branch/worktree name: `kb.wdy-1519-ipv4-e2e-preflight`
- Worktree path: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1519-ipv4-e2e-preflight`
- PR: https://github.com/wendylabsinc/WendyOS/pull/991 (draft, left open but not active)
- PR closing reference: `Closes WDY-1519`
- Scope: add a temporary CI hardening fallback for physical Swift E2E preflight.
  If a configured hostname such as `wendy-SER9.local` fails due to an
  unreachable IPv6 route, resolve an IPv4 address, retry preflight with it, and
  use the resolved IPv4 address for the remainder of the attempt. Preserve clear
  logs/attempt metadata showing both configured and resolved addresses.
- Validation: reproduce or unit-test hostname-to-IPv4 fallback behavior where
  practical; at minimum run shell/YAML/actionlint validation and ensure Swift
  E2E CI passes before merging any workflow/script changes.
- Resume command:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1519-ipv4-e2e-preflight && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

### WDY-1528 — Add machine-readable Swift E2E recording metadata

- Status: `done`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1528/add-machine-readable-swift-e2e-recording-metadata
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done (`completed`)
- Branch/worktree name: `kb.wdy-1528-e2e-recording-metadata` (merged; local worktree removed)
- Worktree path: removed after merge
- PR: https://github.com/wendylabsinc/WendyOS/pull/997 (merged, merge commit `e3c67a72`)
- PR closing reference: `Closes WDY-1528`
- Setup commit: `8fa3463`
- Scope: add a narrow, versioned `recording.json` next to each
  `recording.md`/`recording.sh.txt` with stable source/test identity metadata:
  file path/name, suite, test name, function, and line. Keep the schema focused
  on source/test identity only; do not add command stdout/stderr JSON or broad
  redaction design here.
- Why ASAP/blocker: WDY-1512 exposed duplicate test names across suites/files
  (`'--json' keeps JSON output clean`). Run/report/review result matching needs
  machine-readable suite/test identity instead of parsing human Markdown or
  relying on globally unique test names.
- Required behavior: `RunOverview`, `ReportCommand`, and `ReviewCommand` should
  prefer `recording.json` for xUnit matching while keeping `recording.md` as a
  backward-compatible fallback for older artifacts. Add unit coverage for the
  duplicate-name/multi-suite case.
- Validation: PR #997 passed CI before merge.
- Resume command: not applicable; issue complete and worktree removed.

### WDY-1527 — Rework Swift E2E aggregate storage for attempt-level artifacts

- Status: `done`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1527/rework-swift-e2e-aggregate-storage-for-attempt-level-artifacts
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done (`completed`)
- Branch/worktree name: `kb.wdy-1527-e2e-attempt-artifacts` (merged; local worktree removed)
- Worktree path: removed after merge
- PR: https://github.com/wendylabsinc/WendyOS/pull/994 (merged, merge commit `3a4b42b0`)
- PR closing reference: `Closes WDY-1527`
- Scope: decide and implement a canonical aggregate storage location for
  attempt-level artifacts (`attempt.json`, full `test-results.xml`, future
  `attempt.log`) instead of copying them into every per-test observation
  directory. Preserve current report compatibility where possible and make early
  failures/no-observation attempts retain attempt-level evidence.
- Validation: PR #994 passed CI before merge, including Swift E2E with SER9
  disabled and Jetson active.
- Resume command: not applicable; issue complete and worktree removed.

### WDY-1521 — Teach E2E AI review to explain why a run failed

- Status: `done`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1521/teach-e2e-ai-review-to-explain-why-a-run-failed
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done (`completed`)
- Branch/worktree name: `kb.wdy-1521-e2e-ai-failure-diagnosis` (merged; local worktree removed)
- Worktree path: removed after merge
- PR: https://github.com/wendylabsinc/WendyOS/pull/993 (merged, merge commit `858eaade`)
- PR closing reference: `Closes WDY-1521`
- Scope: adapt Swift E2E AI review/reporting so failed runs include a concise
  diagnosis: likely failure category, evidence from logs/artifacts, confidence
  or inconclusive status, and recommended next action. It should handle
  preflight/setup failures that produce no attempt artifact, and make the output
  useful for Slack or PR comments.
- Validation: local checks are recorded in PR #993. PR checks included the
  expected non-required macOS Discover integration failure; required checks and
  Swift E2E analysis passed before merge.
- Resume command: not applicable; issue complete and worktree removed.

### WDY-1494 — Clean up Swift E2E route matrix and restore commented route ledger

- Status: `done`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1494/clean-up-swift-e2e-route-matrix-and-restore-commented-route-ledger
- Linear assignee: konstantin@wendy.sh
- Linear state: Done (`completed`)
- Branch/worktree name: `kb.wdy-1494-e2e-route-ledger` (merged; local worktree removed)
- Worktree path: removed after merge
- PR: https://github.com/wendylabsinc/WendyOS/pull/977 (merged, merge commit `7aa938d1`)
- PR closing reference: `Closes WDY-1494`
- Scope: clean up the Swift E2E workflow after WDY-1481 and the follow-up
  one-off route-disable PRs. Restore the commented-out route entries that
  existed before PR #964 flattened the route matrix into explicit jobs, keep
  the hosted local macOS↔macOS and Ubuntu↔Ubuntu routes alongside that ledger,
  and make the active/disabled physical routes clear. Correct the WDY-968
  tracking note so it applies to the macOS→Ubuntu/SER9 mDNS route, not Jetson.
- Validation: PR #977 passed CI before merge. PR body records local validation:
  YAML parse, actionlint, and `bash -n swift/Scripts/E2EReview.sh`.
  WDY-968 was updated after merge with corrected instructions for restoring the
  macOS→Ubuntu/SER9 route.
- Resume command: not applicable; issue complete and worktree removed.

### WDY-1481 — Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu

- Status: `done`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1481/add-local-e2e-matrix-coverage-for-macosmacos-and-ubuntuubuntu
- Linear assignee: konstantin@wendy.sh
- Linear state: Done (`completed`)
- Branch/worktree name: `kb.wdy-1481-local-e2e-matrix` (merged; local worktree removed)
- Worktree path: removed after merge
- PR: https://github.com/wendylabsinc/WendyOS/pull/964 (merged, merge commit `ef620742`)
- PR closing reference: `Closes WDY-1481`
- Scope: add two hosted-runner-only E2E matrix entries that exercise local
  E2E paths without requiring physical WendyOS devices on the network:
  macOS↔macOS and Ubuntu↔Ubuntu. These should use local agent/CLI processes or
  an equivalent local harness, and must not depend on SER9, Raspberry Pi,
  Jetson, mDNS discovery, cloud tunnels, or reachable physical devices.
- Validation: PR #964 documents local shell/YAML/actionlint/Swift checks and a
  successful manual local-only workflow dispatch. The PR merged on
  2026-06-11.
- Resume command: not applicable; issue complete and worktree removed.

### WDY-1482 — Gate device-to-device E2E jobs behind successful local E2E runs

- Status: `done`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1482/gate-device-to-device-e2e-jobs-behind-successful-local-e2e-runs
- Linear assignee: konstantin@wendy.sh
- Linear state: Done (`completed`)
- Branch/worktree name: `kb.wdy-1482-gate-device-e2e` (merged; local worktree removed)
- Worktree path: removed after merge
- PR: https://github.com/wendylabsinc/WendyOS/pull/980 (merged, merge commit `33e19a1b`)
- PR closing reference: `Closes WDY-1482`
- Scope: consider splitting the hosted-runner local E2E checks into a separate
  prerequisite job, then only run physical device-to-device E2E jobs after the
  local macOS↔macOS and Ubuntu↔Ubuntu runs succeed. Preserve a clear manual or
  workflow path for real-device jobs and keep the status understandable when
  local-only checks fail, skip, or are disabled.
- Validation: PR #980 passed CI before merge. PR body records local validation:
  YAML parse and actionlint. The merged workflow gates physical routes by target
  agent platform: Linux/WendyOS targets depend on hosted Ubuntu local E2E, and
  macOS targets depend on hosted macOS local E2E.
- Resume command: not applicable; issue complete and worktree removed.

### WDY-1479 — Investigate SER9 Swift E2E mTLS auth failure

- Status: `done`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1479/investigate-ser9-swift-e2e-mtls-auth-failure
- Linear assignee: joannis@wendy.sh
- Linear state: Done (`completed`)
- Branch/worktree name: not prepared in this coordinator
- Worktree path: not applicable
- PR: https://github.com/wendylabsinc/WendyOS/pull/975 (merged, merge commit `7bf0077d`)
- PR closing reference: none; one-off revert of temporary PR-trigger disable
- Scope: investigate why the CI CLI cannot authenticate to
  `wendy-SER9.local` over mTLS before Swift E2E tests execute. Current failure:
  `certificate is not valid for client authentication`; SER9 logs report
  `rejected cert without clientAuth EKU`. Confirm whether Cloud-issued CLI/user
  certificates should include the Client Authentication EKU, identify the
  issuing/refresh path that omits it, and fix or document the required
  certificate behavior so SER9 Swift E2E can authenticate again.
- Validation: WDY-1479 was resolved outside this coordinator. PR #975 reverted
  the temporary PR-trigger disable from PR #960, restoring Swift E2E
  `pull_request` triggers after WDY-1479 was closed.
- Resume command: not applicable; issue complete.

## E2E Tests issue order

Keep each issue short and validation-focused. Completed issues stay in the
ledger for history; current active work is WDY-1559 and WDY-1558.

1. **WDY-1559** — In progress: Investigate Jetson Orin Nano Swift E2E preflight timeout.
2. **WDY-1558** — In progress: Mark failed Swift E2E attempts without observations as failed.
3. **WDY-1521** — Done: Teach E2E AI review to explain why a run failed.
4. **WDY-1528** — Done: Add machine-readable Swift E2E recording metadata.
5. **WDY-1519** — Backlog: Add IPv4 fallback preflight for physical Swift E2E targets.
6. **WDY-1527** — Done: Rework Swift E2E aggregate storage for attempt-level artifacts.
7. **WDY-1510** — Canceled: Re-enable Raspberry Pi physical Swift E2E route.
8. **WDY-1482** — Done: Gate device-to-device E2E jobs behind successful local E2E runs.
9. **WDY-1494** — Done: Clean up Swift E2E route matrix and restore commented route ledger.
10. **WDY-1481** — Done: Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu.
11. **WDY-1479** — Done: Investigate SER9 Swift E2E mTLS auth failure.

## One-off route/workflow PRs

- **PR #995** — Temporarily disables the physical macOS 26 → Ubuntu 24/SER9
  Swift E2E route while WDY-1519 adds IPv4 fallback preflight hardening:
  https://github.com/wendylabsinc/WendyOS/pull/995
  Merged after CI passed, merge commit `d8406c21`. The local one-off worktree
  `kb.disable-ser9-e2e-ipv6` was removed.

## Backlog / only-if-blocking

Use this section for E2E Tests project issues that should not be started yet, or
for related issues outside the project that matter only if they block active E2E
work.

Project update posted on 2026-06-12:
https://linear.app/wendylabsinc/project/e2e-tests-02d5dc2a4b79/activity#project-update-a5ed07b2

Project inventory check on 2026-06-12 also found these E2E Tests issues not yet
fully represented in this coordinator flow:

- **WDY-1512** — Done: Audit and align hidden deprecated CLI aliases.
- **WDY-1511** — Done: Remove misleading hidden completion install
  `--output-dir` test seam.
- **WDY-1513** — Backlog: Align host-only CLI E2E references.
- **WDY-1514** — Backlog: Align OS imaging and update E2E references.
- **WDY-1515** — Backlog: Align direct device command E2E references.
- **WDY-1516** — Backlog: Align cloud-routed device E2E references.
- **WDY-1517** — Backlog: Align build and run E2E references.
- **WDY-1509** — Done: Manually audit CLI surface against E2E stubs across
  Linux and Mac.

- **WDY-1520** — Backlog: Consider CLI IPv6-to-IPv4 fallback for device connections:
  https://linear.app/wendylabsinc/issue/WDY-1520/consider-cli-ipv6-to-ipv4-fallback-for-device-connections
  This is the potential long-term product fix for dual-stack hostnames where an
  IPv6 route fails but IPv4 is reachable. Keep separate from WDY-1519, which is
  a temporary CI preflight hardening issue.
- **WDY-968** — `wendy discover` on mac does not send out discovery packets
  reliably. This is outside the E2E Tests project and currently Done in Linear.
  Follow-up completed: PR #981 re-enabled the physical macOS→Ubuntu/SER9 route
  (and macOS→Jetson) after CI passed, with physical routes gated behind hosted
  local E2E runs and included in E2E analysis. WDY-968 has a final comment
  noting the restored routes and passing checks.
