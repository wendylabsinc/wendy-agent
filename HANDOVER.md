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

1. `WDY-1482` — Todo: Gate device-to-device E2E jobs behind successful local E2E runs
2. `WDY-1481` — Done: Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu
3. `WDY-1479` — Done: Investigate SER9 Swift E2E mTLS auth failure

## Completed housekeeping

- PR #964 merged WDY-1481 local hosted-runner E2E coverage:
  https://github.com/wendylabsinc/WendyOS/pull/964
- PR #974 documented/commented the disabled macOS→Jetson Swift E2E route while
  WDY-968 tracks the mDNS issue:
  https://github.com/wendylabsinc/WendyOS/pull/974
- PR #975 reverted the WDY-1479 temporary PR-trigger disable from PR #960:
  https://github.com/wendylabsinc/WendyOS/pull/975
- WDY-968 was updated with instructions to revert PR #974 once Jetson mDNS is
  fixed and validated. Do not revert WDY-1479/PR #960 for that; PR #975 already
  restored Swift E2E `pull_request` triggers.
- The temporary one-off worktrees for PRs #974 and #975 were removed.
- The WDY-1481 issue worktree has also been removed after merge.

## Remaining intended next steps

- Start WDY-1482 only after the user asks, following the `PLAN.md` working
  protocol.
- Treat WDY-968 as related/background only: it is outside the E2E Tests project
  and tracks when the commented Jetson route can be restored by reverting PR
  #974.

## Coordinator resume command

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.e2e-tests && ai --prompt "Read HANDOVER.md and follow its instructions."
```
