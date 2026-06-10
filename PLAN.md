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
7. Write a `HANDOVER.md` file into the issue worktree. Put the real prompt
   content there: scope, KISS guidance, validation, commit/push expectations,
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

### WDY-1352 — Verify discovery and device selection for WendyAgentMac

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1352/verify-discovery-and-device-selection-for-wendyagentmac
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Branch/worktree name: `kb.wdy-1352-mac-agent-discovery-selection`
- Worktree path: removed after merge (`.worktrees/kb.wdy-1352-mac-agent-discovery-selection`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/930 — merged
- PR closing reference: `Closes WDY-1352`
- Merge commit on `main`: `a941dcae`
- KISS scope: matched Linux's simple discover/default/explicit-hostname flow;
  avoided Mac-specific selection models and diagnostics/troubleshooting content.
- Validation:
  - `go test ./go/internal/cli/commands -run 'TestResolveDeviceAddress_(Flag|DefaultDevice|ExplicitHostPortFlag|ExplicitHostPortDefault|NoDevice)$'`
  - Manual WendyAgentMac targeting checks recorded in PR #930
- Resume command: not needed; issue is complete

### WDY-1345 — Run and record Mac beta smoke test

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1345/run-and-record-mac-beta-smoke-test
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Branch/worktree name: `kb.wdy-1345-mac-beta-smoke-test`
- Worktree path: removed after merge (`.worktrees/kb.wdy-1345-mac-beta-smoke-test`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/934 — merged
- PR closing reference: `Closes WDY-1345`
- Merge commit on `main`: `214c8744`
- KISS scope: minimal release smoke only; launch agent, verify `device info`,
  confirm one unsupported command is clear, and record versions/commands.
- Validation: recorded in PR #934
- Resume command: not needed; issue is complete

### WDY-1346 — Verify native macOS app run flow on Mac agent

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1346/verify-native-macos-app-run-flow-on-mac-agent
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Branch/worktree name: `kb.wdy-1346-native-macos-run-flow`
- Worktree path: removed after merge (`.worktrees/kb.wdy-1346-native-macos-run-flow`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/936 — merged
- PR closing reference: `Closes WDY-1346`
- Merge commit on `main`: `8a295508`
- KISS scope: verified one minimal native macOS SwiftPM `wendy run` path;
  avoided tutorials, sample-app guides, lifecycle deep dives, Docker/container
  work, and E2E automation.
- Validation: recorded in PR #936; GitHub checks passed
- Resume command: not needed; issue is complete

### WDY-1353 — Verify Xcode project run flow with VLMLX on Mac agent

- Status: `prepared`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1353/verify-xcode-project-run-flow-with-vlmlx-on-mac-agent
- Linear assignee: `konstantin@wendy.sh`
- Linear state: In Progress
- Branch/worktree name: `kb.wdy-1353-xcode-hellomlx-run-flow`
- Worktree path: `.worktrees/kb.wdy-1353-xcode-hellomlx-run-flow`
- PR: https://github.com/wendylabsinc/WendyOS/pull/957 — draft
- PR closing reference: `Closes WDY-1353`
- Base: `main`
- Current commit: `644166ee chore: start WDY-1353 Xcode HelloMLX run flow`
- KISS scope: beta requirement to verify the Xcode project build/deploy path
  using the existing `Examples/HelloMLX/HelloMLX.xcodeproj` VLM+MLX app as the
  VLMLX validation target. Keep docs minimal and avoid a broad Xcode tutorial
  or sample-app guide.
- Validation: not run; setup commit only
- `HANDOVER.md`: written in the worktree with KISS scope and commit/push guidance
- Resume command: `cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1353-xcode-hellomlx-run-flow && ai --prompt "Read HANDOVER.md and follow its instructions."`

### WDY-1396 — Document headless Mac setup for Wendy Agent beta

- Status: `ready`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1396/document-headless-mac-setup-for-wendy-agent-beta
- Linear assignee: `konstantin@wendy.sh`
- Linear state: In Review
- Branch/worktree name: `kb.wdy-1396-headless-mac-setup-docs`
- Worktree path: `.worktrees/kb.wdy-1396-headless-mac-setup-docs`
- PR: https://github.com/wendylabsinc/WendyOS/pull/939 — open, ready for review
- PR closing reference: `Closes WDY-1396`
- Base: `main`
- Current commit: `c201a67e docs: add SSH setup to headless Mac checklist`
- KISS scope: add a short headless Mac note, recommend a virtual display
  dongle / HDMI dummy plug, lead with manual System Settings setup for new
  Macs, mention keeping the Mac awake on AC power, and reference manual macOS
  Screen Sharing / automatic-login choices only if needed. Terminal commands
  such as `pmset` are optional/secondary, not the primary setup path.
- Related validation: WDY-1360 should validate this guidance if the clean Apple
  Silicon environment is headless or can reasonably exercise a headless setup.
- Source reference: `kb.ansible:ansible/roles/power_policy/tasks/macos.yml`
  uses `sudo pmset -c sleep 0 displaysleep 10 disksleep 0 womp 1` as an
  implementation reference, not necessarily the public-docs-first path.
- Validation: recorded in PR #939; GitHub checks in progress at last check
- `HANDOVER.md`: written in the worktree with KISS scope and commit/push guidance
- Resume command: `cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1396-headless-mac-setup-docs && ai --prompt "Read HANDOVER.md and follow its instructions."`

### WDY-1473 — Publicize Wendy for Mac beta across website

- Status: `planned`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1473/publicize-wendy-for-mac-beta-across-website
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Todo
- Branch/worktree name: not prepared yet
- Worktree path: not prepared yet
- Existing PR: https://github.com/wendylabsinc/WendyOS/pull/945 — open; use as starting point when beta is ready
- KISS scope: one of the last beta steps; update website/docs status language
  from coming-soon/future tense to beta-available language without expanding
  the support promise.
- Timing: do after WDY-1353, WDY-1396, WDY-1350, and WDY-1360 are complete or
  explicitly waived.
- Resume command: not available until prepared

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

Keep each remaining issue short and validation-focused.

1. **Completed: WDY-1352** — minimal device targeting/docs alignment merged in PR #930.
2. **Completed: WDY-1345** — minimal Mac beta smoke test merged in PR #934.
3. **Completed: WDY-1346** — native macOS SwiftPM `wendy run` flow merged in PR #936.
4. **WDY-1353** — prepared; beta requirement to verify Xcode project run flow with `Examples/HelloMLX/HelloMLX.xcodeproj`.
5. **WDY-1396** — ready; document headless Mac setup: no sleep on AC and virtual display dongle.
6. **WDY-1350** — verify minimal app lifecycle commands for the WDY-1346/WDY-1353 apps.
7. **WDY-1360** — clean Apple Silicon validation of the shipped docs path, including the WDY-1346 native run flow, WDY-1353 HelloMLX Xcode flow, and WDY-1396 headless setup if the environment supports it.
8. **WDY-1473** — final public website/docs status update using PR #945 once beta readiness is complete.

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
- WDY-1460 — post-beta replace Wendy Agent for Mac login/startup item with a
  proper user LaunchAgent so launchd starts it on login and restarts it if it
  exits unexpectedly.
- WDY-1472 — plan the agreed Wendy Agent → Wendy Daemon rename timing: macOS
  beta-only now, whole-codebase now, or whole-codebase after beta. Sync with
  Joannis before implementation.
