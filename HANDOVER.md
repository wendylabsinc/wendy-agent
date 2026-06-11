# Handover — E2E Tests planning worktree

## Context

This is the master planning/coordinator worktree for the Linear project
**E2E Tests**.

- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.e2e-tests`
- Branch: `kb.e2e-tests`
- Base branch used when created: `kb.disable-swift-e2e-ser9-auth`
- Starting commit: `440f9628` (`ci: disable Swift E2E PR runs`)

Keep this worktree for coordination only. Implementation, validation,
review-thread handling, and non-empty implementation commits should happen in
per-issue worktrees.

## Current plan state

`PLAN.md` is populated with the Linear **E2E Tests** issues and current status:

1. `WDY-1482` — In Progress: Gate device-to-device E2E jobs behind successful local E2E runs
2. `WDY-1494` — Done: Clean up Swift E2E route matrix and restore commented route ledger
3. `WDY-1481` — Done: Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu
4. `WDY-1479` — Done: Investigate SER9 Swift E2E mTLS auth failure

## Completed housekeeping

- PR #964 merged WDY-1481 local hosted-runner E2E coverage:
  https://github.com/wendylabsinc/WendyOS/pull/964
- PR #974 documented/commented the disabled macOS→Jetson Swift E2E route, but
  that turned out to be the wrong route for the WDY-968 mDNS issue:
  https://github.com/wendylabsinc/WendyOS/pull/974
- PR #975 reverted the WDY-1479 temporary PR-trigger disable from PR #960:
  https://github.com/wendylabsinc/WendyOS/pull/975
- WDY-968 was previously updated with instructions to revert PR #974, but that
  instruction is now known to be wrong/incomplete because the flaky route is
  macOS→Ubuntu/SER9, not Jetson.
- PR #976 attempted to disable the correct macOS→Ubuntu/SER9 route, but it was
  closed without merge after CI failed and the workflow cleanup scope became
  clear: https://github.com/wendylabsinc/WendyOS/pull/976
- WDY-1494 cleaned this up properly in merged PR #977: restored the commented
  route ledger removed during PR #964, kept the local hosted routes alongside
  it, and updated WDY-968 with the correct macOS→Ubuntu/SER9 follow-up
  instructions.
- WDY-1494 PR: https://github.com/wendylabsinc/WendyOS/pull/977
- WDY-1494 merge commit: `7aa938d1`
- The WDY-1494 issue worktree has been removed after merge.
- The temporary one-off worktrees for PRs #974, #975, and #976 were removed.
- The WDY-1481 issue worktree has also been removed after merge.

## WDY-1482 status

WDY-1482 has been started and prepared:

- Linear state: In Progress
- Assignee: `konstantin@wendy.sh`
- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1482-gate-device-e2e`
- Branch: `kb.wdy-1482-gate-device-e2e`
- Draft PR: https://github.com/wendylabsinc/WendyOS/pull/980
- Resume command:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1482-gate-device-e2e && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

## Remaining intended next steps

- Continue WDY-1482 in its issue worktree until PR #980 is ready/merged. Do not
  merge unless CI passes; if CI fails, stop and report the failing check(s).
- Treat WDY-968 as related/background only: it is outside the E2E Tests project
  and already has corrected follow-up instructions from WDY-1494.

## Coordinator resume command

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.e2e-tests && ai --prompt "Read HANDOVER.md and follow its instructions."
```
