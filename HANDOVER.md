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

1. `WDY-1494` — In Progress: Clean up Swift E2E route matrix and restore commented route ledger
2. `WDY-1482` — Todo: Gate device-to-device E2E jobs behind successful local E2E runs
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
- WDY-1494 was created and started to clean this up properly: restore the
  commented route ledger removed during PR #964, keep the local hosted routes
  alongside it, and update WDY-968 with the correct revert/follow-up
  instructions.
- WDY-1494 worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1494-e2e-route-ledger`
- WDY-1494 draft PR: https://github.com/wendylabsinc/WendyOS/pull/977
- WDY-1494 resume command:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1494-e2e-route-ledger && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```
- The temporary one-off worktrees for PRs #974, #975, and #976 were removed.
- The WDY-1481 issue worktree has also been removed after merge.

## Remaining intended next steps

- Continue WDY-1494 in its issue worktree until PR #977 is ready/merged. Do not
  merge unless CI passes; if CI fails, stop and report the failing check(s).
- Start WDY-1482 only after the user asks, following the `PLAN.md` working
  protocol.
- Treat WDY-968 as related/background only: it is outside the E2E Tests project
  and should receive corrected follow-up instructions as part of WDY-1494.

## Coordinator resume command

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.e2e-tests && ai --prompt "Read HANDOVER.md and follow its instructions."
```
