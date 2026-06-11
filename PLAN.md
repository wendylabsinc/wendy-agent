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

### WDY-1481 — Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu

- Status: `draft PR open; security review failing after latest sync`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1481/add-local-e2e-matrix-coverage-for-macosmacos-and-ubuntuubuntu
- Linear assignee: konstantin@wendy.sh
- Linear state: In Progress (`started`)
- Branch/worktree name: `kb.wdy-1481-local-e2e-matrix`
- Worktree path: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1481-local-e2e-matrix`
- PR: https://github.com/wendylabsinc/WendyOS/pull/964
- PR closing reference: `Closes WDY-1481`
- Scope: add two hosted-runner-only E2E matrix entries that exercise local
  E2E paths without requiring physical WendyOS devices on the network:
  macOS↔macOS and Ubuntu↔Ubuntu. These should use local agent/CLI processes or
  an equivalent local harness, and must not depend on SER9, Raspberry Pi,
  Jetson, mDNS discovery, cloud tunnels, or reachable physical devices.
- Validation: PR #964 documents local shell/YAML/actionlint/Swift checks and a
  successful manual local-only workflow dispatch. Latest observed PR checks
  after syncing with `origin/main`: Claude Security Review failing; several
  required checks still pending. Re-check before marking ready.
- Resume command:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1481-local-e2e-matrix && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

### WDY-1482 — Gate device-to-device E2E jobs behind successful local E2E runs

- Status: `planned`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1482/gate-device-to-device-e2e-jobs-behind-successful-local-e2e-runs
- Linear assignee: unassigned
- Linear state: Todo (`unstarted`)
- Branch/worktree name: not prepared yet
- Worktree path: not prepared yet
- PR: not created yet
- PR closing reference: `Closes WDY-1482`
- Scope: consider splitting the hosted-runner local E2E checks into a separate
  prerequisite job, then only run physical device-to-device E2E jobs after the
  local macOS↔macOS and Ubuntu↔Ubuntu runs succeed. Preserve a clear manual or
  workflow path for real-device jobs and keep the status understandable when
  local-only checks fail, skip, or are disabled.
- Validation: document the decision; if implemented, verify real-device E2E
  jobs declare the local E2E job via `needs` or equivalent gating, fail fast
  when local hosted-runner E2E fails, and still make local-vs-physical failures
  easy to distinguish in workflow results.
- Resume command: not available until prepared

### WDY-1479 — Investigate SER9 Swift E2E mTLS auth failure

- Status: `planned`
- Linear project: E2E Tests
- Linear: https://linear.app/wendylabsinc/issue/WDY-1479/investigate-ser9-swift-e2e-mtls-auth-failure
- Linear assignee: unassigned
- Linear state: Todo (`unstarted`)
- Branch/worktree name: not prepared yet
- Worktree path: not prepared yet
- PR: not created yet
- PR closing reference: `Closes WDY-1479`
- Scope: investigate why the CI CLI cannot authenticate to
  `wendy-SER9.local` over mTLS before Swift E2E tests execute. Current failure:
  `certificate is not valid for client authentication`; SER9 logs report
  `rejected cert without clientAuth EKU`. Confirm whether Cloud-issued CLI/user
  certificates should include the Client Authentication EKU, identify the
  issuing/refresh path that omits it, and fix or document the required
  certificate behavior so SER9 Swift E2E can authenticate again.
- Validation: reproduce or inspect the failing mTLS path; verify a refreshed CLI
  auth certificate contains the expected client authentication capability or
  that the agent accepts the intended certificate; run the smallest available
  SER9 Swift E2E/auth smoke check. Once fixed, restore Swift E2E PR triggers by
  reverting the temporary disable PR noted in Linear (`wendylabsinc/WendyOS#960`)
  if that is in scope for the issue branch.
- Resume command: not available until prepared

## E2E Tests issue order

Keep each issue short and validation-focused. Start with hosted-runner local
coverage so CI has useful E2E signal that does not depend on physical devices,
then consider workflow gating, then return to the SER9-specific blocker.

1. **WDY-1481** — Add local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu.
2. **WDY-1482** — Gate device-to-device E2E jobs behind successful local E2E runs.
3. **WDY-1479** — Investigate SER9 Swift E2E mTLS auth failure.

## Backlog / only-if-blocking

Use this section for E2E Tests project issues that should not be started yet, or
for related issues outside the project that matter only if they block active E2E
work.

- None currently.
