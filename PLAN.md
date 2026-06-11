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
2. Create a dedicated git worktree and branch for the issue.
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

All currently tracked E2E Tests project issues are complete. Completed issues
stay in the ledger for history.

1. **WDY-1482** — Done: Gate device-to-device E2E jobs behind successful local E2E runs.
2. **WDY-1494** — Done: Clean up Swift E2E route matrix and restore commented route ledger.
3. **WDY-1481** — Done: Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu.
4. **WDY-1479** — Done: Investigate SER9 Swift E2E mTLS auth failure.

## Backlog / only-if-blocking

Use this section for E2E Tests project issues that should not be started yet, or
for related issues outside the project that matter only if they block active E2E
work.

- **WDY-968** — `wendy discover` on mac does not send out discovery packets
  reliably. This is outside the E2E Tests project and currently Done in Linear,
  but it is the related tracker for the macOS→Ubuntu/SER9 mDNS route issue.
  PR #977 restored the commented route ledger and WDY-968 now has corrected
  follow-up instructions: after the issue is fixed, uncomment
  `swift-e2e-physical-macos-ubuntu` in `.github/workflows/swift-e2e-tests.yml`
  and add it back to `swift-e2e-analyze.needs`.
