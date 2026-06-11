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

1. `WDY-1482` — Done: Gate device-to-device E2E jobs behind successful local E2E runs
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
- PR #981 re-enabled the physical macOS→Ubuntu/SER9 and macOS→Jetson Swift E2E
  routes after CI passed, with both physical routes gated behind hosted local
  E2E and included in E2E analysis:
  https://github.com/wendylabsinc/WendyOS/pull/981
- PR #981 merge commit: `28020381`
- WDY-968 has a final comment noting the physical route restoration and passing
  checks.
- The WDY-1494 issue worktree has been removed after merge.
- The temporary one-off worktrees for PRs #974, #975, and #976 were removed.
- The WDY-1481 issue worktree has also been removed after merge.

## WDY-1482 status

WDY-1482 is complete:

- Linear state: Done
- Assignee: `konstantin@wendy.sh`
- PR: https://github.com/wendylabsinc/WendyOS/pull/980
- Merge commit: `33e19a1b`
- Summary: physical Swift E2E routes are gated by the target agent platform they
  exercise. Linux/WendyOS targets depend on the hosted Ubuntu local run; macOS
  targets depend on the hosted macOS local run.
- Validation: PR #980 passed CI before merge. PR body records YAML parse and
  actionlint validation.
- The WDY-1482 issue worktree has been removed after merge.

## Remaining intended next steps

- All currently tracked E2E Tests project issues are complete.
- Treat WDY-968 as related/background only: it is outside the E2E Tests project
  and now has a final comment noting PR #981 restored the physical Swift E2E
  routes after passing checks.

## Coordinator resume command

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.e2e-tests && ai --prompt "Read HANDOVER.md and follow its instructions."
```
