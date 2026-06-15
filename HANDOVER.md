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

1. `WDY-1562` — In progress: Add legacy app integration suite to Swift E2E
2. `WDY-1561` — In progress: Fix macOS integration discovery empty matrix failure
3. `WDY-1560` — Todo, High: Run physical Swift E2E only for stable releases until dedicated CI devices exist
4. `WDY-1558` — In progress: Mark failed Swift E2E attempts without observations as failed
5. `WDY-1559` — Done: Investigate Jetson Orin Nano Swift E2E preflight timeout
6. `WDY-1521` — Done: Teach E2E AI review to explain why a run failed
7. `WDY-1528` — Done: Add machine-readable Swift E2E recording metadata
8. `WDY-1519` — Backlog: Add IPv4 fallback preflight for physical Swift E2E targets
9. `WDY-1527` — Done: Rework Swift E2E aggregate storage for attempt-level artifacts
10. `WDY-1510` — Canceled: Re-enable Raspberry Pi physical Swift E2E route
11. `WDY-1482` — Done: Gate device-to-device E2E jobs behind successful local E2E runs
12. `WDY-1494` — Done: Clean up Swift E2E route matrix and restore commented route ledger
13. `WDY-1481` — Done: Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu
14. `WDY-1479` — Done: Investigate SER9 Swift E2E mTLS auth failure

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

## WDY-1510 status

WDY-1510 was canceled because no Raspberry Pi 5 is currently available on CI:

- Dedicated worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1510-rpi-swift-e2e`
- Branch: `kb.wdy-1510-rpi-swift-e2e`
- PR: https://github.com/wendylabsinc/WendyOS/pull/983 (closed without merge)
- Linear state: Canceled
- Linear comment: https://linear.app/wendylabsinc/issue/WDY-1510/re-enable-raspberry-pi-physical-swift-e2e-route#comment-6793dbb3
- Setup commit: `7c96b42`
- Implementation commit on the closed branch: `51add25` re-enabled the macOS 26
  → Raspberry Pi 5 physical Swift E2E route and added it to the analysis job
  dependencies.
- Local validation passed before cancellation: YAML parse and actionlint.
- Temporary repository variable `SWIFT_E2E_RASPBERRY_PI_5_DEVICE_ADDRESS` was
  removed after the PR was closed.
- Swift E2E workflow run `27362744824` was canceled after closing the PR; the
  Raspberry Pi and analysis jobs ended canceled.

## Newly created follow-up issues

- WDY-1562 — In Progress, E2E Tests project: add legacy app integration suite to
  Swift E2E. Created because draft PR #867 had no linked/closing Linear issue.
  - Existing PR: https://github.com/wendylabsinc/WendyOS/pull/867
  - Branch/worktree: `ai.e2e-app-integration-plan` at `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/ai.e2e-app-integration-plan`
  - Linear: https://linear.app/wendylabsinc/issue/WDY-1562/add-legacy-app-integration-suite-to-swift-e2e
  - PR body now includes `Closes WDY-1562`.
- WDY-1561 — In Progress, E2E Tests project: fix `PR Integration Tests` macOS
  discovery matrix generation when discovery finds LAN devices but none match
  the allowlist, causing `HOSTS[@]: unbound variable` under `set -u`.
  - Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1561-macos-integration-empty-matrix`
  - Branch: `kb.wdy-1561-macos-integration-empty-matrix`
  - Draft PR: https://github.com/wendylabsinc/WendyOS/pull/1034
  - Linear: https://linear.app/wendylabsinc/issue/WDY-1561/fix-macos-integration-discovery-empty-matrix-failure
  - Setup commit: `26f1dad`
  - Resume command:

    ```sh
    cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1561-macos-integration-empty-matrix && ai --prompt "Read HANDOVER.md and follow its instructions."
    ```
- WDY-1560 — Todo, High priority, E2E Tests project: run physical Swift E2E only
  for stable releases until dedicated CI devices exist. PRs should keep local
  E2E coverage, while physical routes should remain manually triggerable and
  release-blocking before stable releases.
  - Linear: https://linear.app/wendylabsinc/issue/WDY-1560/run-physical-swift-e2e-only-for-stable-releases-until-dedicated-ci
- WDY-1559 — Done, E2E Tests project: investigated the Jetson timeout and
  temporarily disabled the flaky Jetson Swift E2E route.
  - PR: https://github.com/wendylabsinc/WendyOS/pull/1033 (merged)
  - Merge commit: `fda83c1f`
  - Branch/worktree: `kb.wdy-1559-jetson-e2e-timeout` merged; local worktree removed.
- WDY-1558 — In Progress, E2E Tests project: mark failed Swift E2E attempts
  without observations as failed. Created after run
  https://github.com/wendylabsinc/WendyOS/actions/runs/27543175882 failed in
  `macOS 26 → Jetson Orin Nano` during preflight. The AI diagnosis correctly
  found the device/network timeout, but the target overview rendered
  `macos-jetson-orin-nano` as Unknown despite `attempt.json.exitStatus = 1` and
  no per-test observations.
  - Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1558-e2e-attempt-failures`
  - Branch: `kb.wdy-1558-e2e-attempt-failures`
  - Draft PR: https://github.com/wendylabsinc/WendyOS/pull/1032
  - Linear: https://linear.app/wendylabsinc/issue/WDY-1558/mark-failed-swift-e2e-attempts-without-observations-as-failed
  - Setup commit: `09f75c4`
  - Resume command:

    ```sh
    cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1558-e2e-attempt-failures && ai --prompt "Read HANDOVER.md and follow its instructions."
    ```
- WDY-1519 — Backlog, E2E Tests project: add temporary IPv4 fallback preflight
  for physical Swift E2E targets after run `27381316219` failed with an
  unreachable IPv6 route to `wendy-SER9.local`.
  - Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1519-ipv4-e2e-preflight`
  - Branch: `kb.wdy-1519-ipv4-e2e-preflight`
  - Draft PR: https://github.com/wendylabsinc/WendyOS/pull/991 (left open but not active)
  - Linear: https://linear.app/wendylabsinc/issue/WDY-1519/add-ipv4-fallback-preflight-for-physical-swift-e2e-targets
  - Setup commit: `58a8abf`
  - Resume command:

    ```sh
    cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1519-ipv4-e2e-preflight && ai --prompt "Read HANDOVER.md and follow its instructions."
    ```
- WDY-1520 — Backlog: consider the long-term product CLI fix for dual-stack
  hostnames where IPv6 fails but IPv4 succeeds.
  https://linear.app/wendylabsinc/issue/WDY-1520/consider-cli-ipv6-to-ipv4-fallback-for-device-connections
- WDY-1527 — Done, E2E Tests project: reworked Swift E2E aggregate storage for
  attempt-level artifacts.
  - PR: https://github.com/wendylabsinc/WendyOS/pull/994 (merged after CI passed)
  - Merge commit: `3a4b42b0`
  - Branch/worktree: `kb.wdy-1527-e2e-attempt-artifacts` merged; local worktree removed.
- WDY-1528 — Done, E2E Tests project: added narrow machine-readable Swift E2E
  recording metadata sidecars.
  - PR: https://github.com/wendylabsinc/WendyOS/pull/997 (merged after CI passed)
  - Merge commit: `e3c67a72`
  - Branch/worktree: `kb.wdy-1528-e2e-recording-metadata` merged; local worktree removed.
- WDY-1521 — Done, E2E Tests project: taught E2E AI review to explain why a
  specific run failed and what to do next, including preflight/setup failures
  with missing attempt artifacts.
  - PR: https://github.com/wendylabsinc/WendyOS/pull/993 (merged)
  - Merge commit: `858eaade`
  - Linear: https://linear.app/wendylabsinc/issue/WDY-1521/teach-e2e-ai-review-to-explain-why-a-run-failed
  - Branch/worktree: `kb.wdy-1521-e2e-ai-failure-diagnosis` merged; local worktree removed.

## Project issue inventory update

Checked the E2E Tests Linear project on 2026-06-12. New/current issues not
previously represented in this coordinator include:

- WDY-1528 — Done, assigned to `konstantin@wendy.sh`: machine-readable Swift
  E2E recording metadata landed in PR #997. Duplicate test names across
  suites/files now have stable source/test identity available for xUnit
  matching.
  https://linear.app/wendylabsinc/issue/WDY-1528/add-machine-readable-swift-e2e-recording-metadata
- WDY-1512 — Done: audit and align hidden deprecated CLI aliases.
- WDY-1511 — Done: remove misleading hidden completion install `--output-dir`
  test seam.
- WDY-1513 through WDY-1517 — Backlog reference-alignment follow-ups.
- WDY-1509 — Done: CLI surface audit.

## One-off SER9 route disable

- PR #995 temporarily disabled the physical macOS 26 → Ubuntu 24/SER9 Swift E2E
  route while WDY-1519 adds IPv4 fallback preflight hardening:
  https://github.com/wendylabsinc/WendyOS/pull/995
- Merged after CI passed; merge commit `d8406c21`.
- Branch/worktree: `kb.disable-ser9-e2e-ipv6` merged; local worktree removed.
- Implementation commit: `4197a25d ci: temporarily disable SER9 Swift E2E route`
- Local validation passed: YAML parse and actionlint.

## Remaining intended next steps

- Continue WDY-1562, WDY-1561, and WDY-1558 in their dedicated worktrees and
  draft PRs #867 / #1034 / #1032.
- WDY-1560 is Todo/High and should be started after the current active fixes
  unless reprioritized.
- WDY-1559, WDY-1521, WDY-1528, WDY-1527, and the SER9 route disable are complete.
- The E2E Tests Linear project update was posted:
  https://linear.app/wendylabsinc/project/e2e-tests-02d5dc2a4b79/activity#project-update-a5ed07b2
- WDY-1519 and WDY-1520 are in the backlog; do not resume them unless asked.
  PR #991 remains open as a draft with stale failing checks from before the SER9
  route was disabled.
- PR #1033, PR #993, PR #997, PR #995, and PR #994 are merged; no action needed there.
- If Raspberry Pi 5 CI hardware comes online later, open a new Linear issue or
  reopen WDY-1510 and start from the closed PR branch/diff.
- Treat WDY-968 as related/background only: it is outside the E2E Tests project
  and now has a final comment noting PR #981 restored the physical Swift E2E
  routes after passing checks.

## Coordinator resume command

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.e2e-tests && ai --prompt "Read HANDOVER.md and follow its instructions."
```
