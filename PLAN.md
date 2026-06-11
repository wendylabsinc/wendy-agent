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

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1353/verify-xcode-project-run-flow-with-vlmlx-on-mac-agent
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Branch/worktree name: `kb.wdy-1353-xcode-hellomlx-run-flow`
- Worktree path: removed after merge (`.worktrees/kb.wdy-1353-xcode-hellomlx-run-flow`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/957 — merged
- PR closing reference: `Closes WDY-1353`
- Merge commit on `main`: `bb2c2870`
- KISS scope: verified the Xcode project build/deploy path using the existing
  `Examples/HelloMLX/HelloMLX.xcodeproj` VLM+MLX app as the VLMLX validation
  target. Avoided broad Xcode tutorial or sample-app guide work.
- Validation: recorded in PR #957; GitHub checks passed before merge
- Notes: validation covered local `wendy run --device localhost:50051 --yes`,
  model/resource sync, runtime logs, and a narrow direct-agent gRPC keepalive
  fix exposed by a remote Mac run.
- Resume command: not needed; issue is complete

### WDY-1396 — Document headless Mac setup for Wendy Agent beta

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1396/document-headless-mac-setup-for-wendy-agent-beta
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Branch/worktree name: `kb.wdy-1396-headless-mac-setup-docs`
- Worktree path: removed after merge (`.worktrees/kb.wdy-1396-headless-mac-setup-docs`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/939 — merged
- PR closing reference: `Closes WDY-1396`
- Merge commit on `main`: `7b12ae9a`
- KISS scope: added a short headless Mac note: virtual display dongle / HDMI
  dummy plug, manual System Settings setup for new Macs, keeping the Mac awake
  on AC power, and manual macOS Screen Sharing / automatic-login choices only if
  needed. Avoided broad diagnostics or reset/troubleshooting docs.
- Related validation: WDY-1360 should validate this guidance if the clean Apple
  Silicon environment is headless or can reasonably exercise a headless setup.
- Source reference: `kb.ansible:ansible/roles/power_policy/tasks/macos.yml`
  uses `sudo pmset -c sleep 0 displaysleep 10 disksleep 0 womp 1` as an
  implementation reference, not necessarily the public-docs-first path.
- Validation: recorded in PR #939; GitHub checks passed before merge
- Resume command: not needed; issue is complete

### WDY-1350 — Verify app lifecycle commands on Mac agent

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1350/verify-app-lifecycle-commands-on-mac-agent
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Branch/worktree name: `kb.wdy-1350-mac-app-lifecycle`
- Worktree path: removed after merge (`.worktrees/kb.wdy-1350-mac-app-lifecycle`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/958 — merged
- PR closing reference: `Closes WDY-1350`
- Merge commit on `main`: `cc1ea2a2`
- KISS scope: verified minimal lifecycle sanity for Mac beta: list, stop,
  and remove against the app paths already validated by WDY-1346 and WDY-1353.
  Avoided a command matrix, broad diagnostics, and new E2E infrastructure.
- Validation: recorded in PR #958; found and fixed native Mac app deletion so
  `device apps remove --force` also removes the synced app payload directory,
  including orphaned payloads left by prior registry-only removals. Swift
  `ContainerServiceTests` passed.
- Resume command: not needed; issue is complete

### WDY-1360 — Validate Mac beta on a clean Apple Silicon macOS device

- Status: `prepared`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1360/validate-mac-beta-on-a-clean-apple-silicon-macos-device
- Linear assignee: `konstantin@wendy.sh`
- Linear state: In Progress
- Branch/worktree name: `kb.wdy-1360-clean-mac-beta-validation`
- Worktree path: `.worktrees/kb.wdy-1360-clean-mac-beta-validation`
- PR: https://github.com/wendylabsinc/WendyOS/pull/963 — draft
- PR closing reference: `Closes WDY-1360`
- Base: `main`
- Current commit: `bd5c25cb chore: start WDY-1360 clean Mac beta validation`
- KISS scope: interactive clean Apple Silicon validation of the shipped Mac beta
  docs path, WDY-1346 native macOS SwiftPM run flow, WDY-1353 HelloMLX Xcode
  flow, and WDY-1396 headless setup guidance only if the environment supports
  it. Avoid diagnostics, firewall/VPN/TCC matrices, reset/uninstall docs, E2E
  infra, and mTLS work.
- Validation: not run; setup commit only
- `HANDOVER.md`: written in the worktree with interactive step-by-step guidance
- Resume command: `cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1360-clean-mac-beta-validation && ai --prompt "Read HANDOVER.md and follow its instructions."`

### WDY-1486 — Fix Wendy Agent auto-start checkbox not registering in Login Items

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1486/fix-wendy-agent-auto-start-checkbox-not-registering-in-login-items
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Linear project: Wendy for Mac — Beta
- Branch/worktree name: `kb.wdy-1486-login-items-autostart` (setup PR closed),
  completed via `kb.fix-login-item-startup`
- Worktree path: removed after completion (`.worktrees/kb.wdy-1486-login-items-autostart`)
- Setup PR: https://github.com/wendylabsinc/WendyOS/pull/967 — closed unmerged
- Completion PR: https://github.com/wendylabsinc/WendyOS/pull/978 — merged
- PR closing reference: `Closes WDY-1486`
- Merge commit on `main`: `be639fd2`
- KISS scope: fixed only the welcome-screen auto-start checkbox/login item
  registration behavior. Avoided the broader WDY-1460 LaunchAgent replacement,
  reset/uninstall docs, first-launch guide expansion, diagnostics matrices, and
  unrelated menu bar changes.
- Source: found during WDY-1360 clean Mac beta validation on a fresh M1 Mac mini
  where the checkbox was enabled by default but Wendy Agent did not appear in
  System Settings → Login Items → Open at Login.
- Validation: recorded in PR #978; `xcodebuild` WendyAgentMac Debug build passed
- Resume command: not needed; issue is complete

### WDY-1495 — Let HelloMLX choose smaller models for constrained Macs

- Status: `done`
- Linear: https://linear.app/wendylabsinc/issue/WDY-1495/let-hellomlx-choose-smaller-models-for-constrained-macs
- Linear priority: High
- Linear assignee: `konstantin@wendy.sh`
- Linear state: Done
- Linear project: Wendy for Mac — Beta
- Branch/worktree name: `kb.wdy-1495-hellomlx-smaller-models`
- Worktree path: removed after merge (`.worktrees/kb.wdy-1495-hellomlx-smaller-models`)
- PR: https://github.com/wendylabsinc/WendyOS/pull/979 — merged
- PR closing reference: `Closes WDY-1495`
- Merge commit on `main`: `11782254`
- KISS scope: made HelloMLX practical on constrained Apple Silicon Macs by
  adding explicit model tiers selected with `Scripts/DownloadVLM.sh
  small|medium|large`, while keeping the Gemma 27B 4-bit large path available
  for higher-memory machines. Avoided a broad model zoo and unrelated HelloMLX
  rewrite.
- Source: WDY-1360 clean Mac validation showed the previous
  `mlx-community/gemma-3-27b-it-qat-4bit` default downloaded/synced about
  16–17 GB and was unbearably slow on an M1 Mac mini with 16 GB RAM, with swap
  nearly exhausted.
- Validation: recorded in PR #979; covered script syntax/help, `wendy json
  validate`, Xcode project listing, Release build for macOS arm64, and
  `git diff --check`.
- Resume command: not needed; issue is complete

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
- Timing: do after WDY-1353, WDY-1396, WDY-1350, WDY-1360, WDY-1486, and
  WDY-1495 are complete or explicitly waived.
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
4. **Completed: WDY-1353** — Xcode HelloMLX run flow merged in PR #957.
5. **Completed: WDY-1396** — minimal headless Mac setup guidance merged in PR #939.
6. **Completed: WDY-1350** — minimal Mac app lifecycle validation merged in PR #958.
7. **WDY-1360** — prepared; clean Apple Silicon validation of the shipped docs
   path, including the WDY-1346 native run flow, WDY-1353 HelloMLX Xcode flow,
   and WDY-1396 headless setup if the environment supports it.
8. **Completed: WDY-1486** — login item registration fix merged in PR #978.
9. **Completed: WDY-1495** — smaller HelloMLX model choices merged in PR #979.
10. **WDY-1473** — final public website/docs status update using PR #945 once beta readiness is complete.

## Backlog / post-beta or only-if-blocking

These Linear issues were updated so agents know they are not part of the
super time-constrained beta unless the user explicitly re-prioritizes them.

Linear/project check: WDY-1480 now tracks proper mTLS support in the
`Wendy for Mac — Beta` project backlog. Related non-beta issues include WDY-1212
(self-signed local CA support for mTLS without cloud), WDY-1376 (macOS exposed
port 50051 security guidance), WDY-1019 (cloud-tunnel registry mTLS), and
WDY-1479 (SER9 Swift E2E mTLS auth failure).

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
- WDY-1480 — add proper mTLS support for Wendy for Mac; beta-project backlog,
  but outside the current KISS beta path unless explicitly reprioritized.
  Joannis noted on Slack that this is optional for now: “It's not hard to add,
  but leave it for now 🙂”
  (https://wendylabs.slack.com/archives/C0AM24AKWF4/p1781091500750299).
- WDY-1492 — explore USB pass-through for Linux containers on Wendy for Mac;
  beta-project backlog and explicitly exploratory. Investigate whether Apple's
  Virtualization.framework + Accessory Access USB pass-through can work with
  Wendy's Docker-based Linux container path or requires a Wendy-owned VM path.
  Includes Max's Slack context:
  https://wendylabs.slack.com/archives/C07RK9XAFD1/p1781155454497069.
- WDY-1498 — add a headless/device-code flow for `wendy auth login` so SSH or
  browserless machines can authenticate via a browser on another device. Moved
  out of `Wendy for Mac — Beta`; this is general CLI/cloud auth backlog, not a
  Mac-specific beta deliverable. Related issue check found no exact duplicate;
  nearby work includes WDY-1325/WDY-874/WDY-865 for broader OIDC/auth, WDY-1478
  for Firebase refresh-token persistence, and WDY-719 for an older fixed CLI
  login issue.
