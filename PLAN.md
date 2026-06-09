# Wendy for Mac — Beta Work Plan

## KISS beta policy

The beta is time-constrained. Keep macOS work aligned with what we currently
ship for Linux/WendyOS instead of adding a better support surface only for Mac.

Current Linux standalone-agent docs are intentionally short:

1. install `wendy-agent`,
2. verify the service is running,
3. discover the device / optionally set it as default,
4. run with an explicit `--device` hostname when discovery is not available,
5. link to existing app guides.

For the macOS beta, do the same: install, verify with `device info`, optionally
set a default device, and run one native macOS app path. Do **not** add broad
diagnostics, reset/uninstall docs, firewall/VPN recipes, first-launch prompt
guides, command-by-command matrices, or new E2E infrastructure unless the user
explicitly asks for a post-beta pass.

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
7. Write a `HANDOVER.md` file into the issue worktree. The handover must tell
   the per-issue agent to KISS, stay aligned with the Linux/WendyOS status quo,
   commit small coherent changes often, and push to the draft PR as it goes.
8. Leave the user with the worktree path, PR link, and a one-line command to
   resume from that worktree using `--prompt`.

Implementation, validation, review-thread handling, and non-empty commits happen
in per-issue worktree sessions, not in this master planning session.

## Issue ledger

### WDY-1352 — Verify discovery and device selection for WendyAgentMac

- Status: `wip`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1352/verify-discovery-and-device-selection-for-wendyagentmac
- Linear assignee: `konstantin@wendy.sh`
- Linear state: In Progress
- Branch/worktree name: `kb.wdy-1352-mac-agent-discovery-selection`
- Worktree path: `.worktrees/kb.wdy-1352-mac-agent-discovery-selection`
- PR: https://github.com/wendylabsinc/WendyOS/pull/930 — draft
- PR closing reference: `Closes WDY-1352`
- Base: `main`
- Current commit: `5fc99581 docs: simplify Mac agent targeting guidance`
- KISS scope: match Linux's simple discover/default/explicit-hostname flow;
  avoid Mac-specific selection models and diagnostics/troubleshooting content.
- Validation:
  - `go test ./go/internal/cli/commands -run 'TestResolveDeviceAddress_(Flag|DefaultDevice|ExplicitHostPortFlag|ExplicitHostPortDefault|NoDevice)$'`
  - Manual WendyAgentMac targeting checks recorded in PR #930
  - GitHub checks mostly passing; docs deploy was still in progress at last check
- `HANDOVER.md`: refreshed in the worktree with KISS scope and current branch state
- Resume command: `cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1352-mac-agent-discovery-selection && ai --prompt "Read HANDOVER.md and continue work on WDY-1352. Keep it KISS, aligned with current Linux/WendyOS docs, commit often, and push to the draft PR as you go."`

### WDY-1377 — Show macOS-specific unsupported messages for hardware APIs

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1377/show-macos-specific-unsupported-messages-for-hardware-apis
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Branch/worktree name: `kb.wdy-1377-macos-unsupported-hardware-errors`
- Worktree path: removed after merge (`.worktrees/kb.wdy-1377-macos-unsupported-hardware-errors`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/928 — merged
- PR closing reference: `Closes WDY-1377`
- Merge commit on `main`: `243fbf32`
- Validation:
  - `cd go && go test ./internal/cli/commands`
  - GitHub checks passed on PR #928
- Resume command: not needed; issue is complete

### WDY-1359 — Add diagnostics and log collection instructions

- Status: `abandoned`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1359/add-diagnostics-and-log-collection-instructions
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Canceled
- Branch/worktree name: `kb.wdy-1359-macos-diagnostics-docs`
- Worktree path: removed after cancellation (`.worktrees/kb.wdy-1359-macos-diagnostics-docs`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/929 — closed unmerged
- PR closing reference: `Closes WDY-1359` was present, but PR was canceled
- Reason: diagnostics/log collection docs are extra compared with current
  Linux/WendyOS docs and are not part of the minimal beta release.
- Resume command: not needed; issue is canceled for beta

### WDY-1343 — Create minimal unlisted Wendy for Mac beta docs page

- Status: `done`
- Branch/worktree name: `kb.wdy-1343-mac-beta-docs`
- Worktree path: removed after completion (`.worktrees/kb.wdy-1343-mac-beta-docs`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/906 — merged
- Closing behavior: PR body includes `Closes WDY-1343`
- Validation:
  - `cd docs && npm run types:check`
  - Manual stable CLI + stable WendyAgentMac install/run smoke validation recorded in PR body

### WDY-1344 — Create Mac beta support matrix

- Status: `done`
- Linear assignee: `konstantin@wendy.sh`
- Branch/worktree name: `kb.wdy-1344-mac-beta-support-matrix`
- Worktree path: removed after completion (`.worktrees/kb.wdy-1344-mac-beta-support-matrix`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/925 — merged
- PR closing reference: `Closes WDY-1344`
- Merge commit on `main`: `49517f19`
- Validation:
  - `cd go/internal/cli/assets/docs && npm run types:check` passed
- Resume command: not needed; issue is complete

### WDY-1378 — Document `platform: "darwin"` in `wendy.json`

- Status: `done`
- Linear assignee: `konstantin@wendy.sh`
- Branch/worktree name: `kb.wdy-1378-darwin-platform-docs`
- Worktree path: removed after completion (`.worktrees/kb.wdy-1378-darwin-platform-docs`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/926 — merged
- PR closing reference: `Closes WDY-1378`
- Merge commit on `main`: `bbb09a4d`
- Validation: completed in PR #926
- Resume command: not needed; issue is complete

### WDY-1386 — Add sticky docs preview comments to documentation PRs

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1386/add-sticky-docs-preview-comments-to-documentation-prs
- Linear assignee: `konstantin@wendy.sh`
- Branch/worktree name: `kb.wdy-1386-docs-preview-comment`
- Worktree path: removed after completion (`.worktrees/kb.wdy-1386-docs-preview-comment`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/927 — merged
- PR closing reference: `Closes WDY-1386`
- Merge commit on `main`: `6decf523`
- Validation: completed in PR #927
- Resume command: not needed; issue is complete

## Minimal beta issue order

Only start these if they are still needed after reviewing merged docs and PR
#930. Keep each one short and validation-focused.

1. **Finish WDY-1352** — minimal device targeting/docs alignment.
2. **WDY-1345** — run and record a minimal Mac beta smoke test.
3. **WDY-1346** — verify one native macOS SwiftPM `wendy run` flow.
4. **WDY-1350** — verify minimal app lifecycle commands for that same app.
5. **WDY-1360** — clean Apple Silicon validation of the shipped docs path.

## Backlog / post-beta or only-if-blocking

These Linear issues were updated so agents know they are not part of the
super time-constrained beta unless the user explicitly re-prioritizes them:

- WDY-1347 — onboarding copy; only if a concrete shipped UI over-promises.
- WDY-1348 — canceled; covered by WDY-1344 unless a specific missing limitation appears.
- WDY-1349 — post-beta CLI audit.
- WDY-1351 — post-beta broader unsupported-flow improvements; WDY-1377 covered beta minimum.
- WDY-1355 — post-beta E2E/smoke subset.
- WDY-1357 — post-beta install/reset/uninstall/troubleshooting docs.
- WDY-1358 — post-beta broader CLI unsupported-error rendering; WDY-1377 covered beta minimum.
- WDY-1359 — canceled; diagnostics/log docs are not in beta scope.
- WDY-1364 — post-beta Swift E2E review.
- WDY-1366 — post-beta Linux/macOS install-doc restructuring.
- WDY-1376 — post-beta security guidance unless security explicitly blocks beta; at most one short callout if revived.
- WDY-1379 — post-beta native macOS SwiftPM example.
- WDY-1380 — post-beta first-launch prompt docs unless clean validation proves a blocker.
- WDY-1381 — post-beta platform-aware E2E reference rendering.
- WDY-1382 — post-beta macOS agent device-info E2E spec.
- WDY-1383 — post-beta native Darwin SwiftPM E2E spec.
- WDY-1384 — post-beta unsupported hardware API E2E specs.
- WDY-1385 — post-beta macOS release artifact smoke workflow.
