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

`PLAN.md` is populated with the active Linear **E2E Tests** issues:

1. `WDY-1481` — Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu
2. `WDY-1482` — Gate device-to-device E2E jobs behind successful local E2E runs
3. `WDY-1479` — Investigate SER9 Swift E2E mTLS auth failure

## WDY-1481 status

WDY-1481 has been started and prepared:

- Linear state: In Progress
- Assignee: `konstantin@wendy.sh`
- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1481-local-e2e-matrix`
- Branch: `kb.wdy-1481-local-e2e-matrix`
- Draft PR: https://github.com/wendylabsinc/WendyOS/pull/964
- Resume command:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1481-local-e2e-matrix && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

Housekeeping note: PR #964 now has implementation commits and validation in the
PR body. After the latest sync with `origin/main`, Claude Security Review was
failing and some checks were pending. Re-check PR status before marking it ready.

## Remaining intended next steps

- Address WDY-1481 in its issue worktree until PR #964 is ready/merged.
- Start WDY-1482 only after the user asks, following the `PLAN.md` working
  protocol.
- Start WDY-1479 only after the user asks, following the `PLAN.md` working
  protocol.

## Coordinator resume command

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.e2e-tests && ai --prompt "Read HANDOVER.md and follow its instructions."
```
