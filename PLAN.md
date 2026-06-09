# Wendy for Mac — Beta Work Plan

Prioritize issues as if Wendy for Mac may need to release after the next issue,
and again after every issue after that. Prefer user-facing correctness, clear
support boundaries, and actionable failure modes before deeper automation.

## Working protocol

For each issue we start, this master session only prepares the workspace. It
must not do the actual issue implementation.

1. Create a dedicated git worktree and branch for the issue.
2. Add an empty setup commit for the issue.
3. Push the branch.
4. Create a draft PR from the setup commit using a real markdown body file,
   not an inline string with escaped newlines. Prefer:

   ```sh
   cat > /tmp/pr-body.md <<'MD'
   ## Summary
   - Set up WDY-1234.

   Closes WDY-1234.

   ## Tests
   - Not run yet.
   MD
   gh pr create --draft --base main --head <branch-name> --title "..." --body-file /tmp/pr-body.md
   ```

5. Include the Linear issue link/closing reference in the PR body, for example
   `Closes WDY-1234`, so merging the PR closes the issue.
6. Write a `HANDOVER.md` file into the issue worktree with the handover context
   for a new session. The handover must explicitly tell the per-issue agent to
   commit small coherent changes often and push to the draft PR as it goes.
7. Leave the user with the worktree path, PR link, and a one-line command to
   resume from that worktree using `--prompt`, for example:

   ```sh
   cd /path/to/.worktrees/<branch-name> && ai --prompt "Read HANDOVER.md and continue work on WDY-1234. Commit often and push to the draft PR as you go."
   ```

Implementation, validation, review-thread handling, and non-empty commits happen
in the per-issue worktree session, not in this master planning session.

## Issue ledger

Keep this ledger current whenever an issue is prepared, in progress, ready,
merged, or abandoned. Branch names and worktree directory names must be 1:1.

For each issue, record:

- Status: `planned`, `prepared`, `wip`, `ready`, `merged`, `done`, or `abandoned`
- Branch/worktree name
- Worktree path
- PR link and draft/ready/merged state
- PR closing reference status, e.g. `Closes WDY-1234`
- Base branch or stack parent
- Current commit, if useful
- Validation status
- `HANDOVER.md` status and summary
- One-line resume command using `ai --prompt` to read `HANDOVER.md`

### WDY-1343 — Create minimal unlisted Wendy for Mac beta docs page

- Status: `done`
- Branch/worktree name: `kb.wdy-1343-mac-beta-docs`
- Worktree path: `.worktrees/kb.wdy-1343-mac-beta-docs`
- PR: https://github.com/wendylabsinc/WendyOS/pull/906 — merged
- Closing behavior: PR body includes `Closes WDY-1343`
- Validation:
  - `cd docs && npm run types:check`
  - Manual stable CLI + stable WendyAgentMac install/run smoke validation recorded in PR body

### WDY-1344 — Create Mac beta support matrix

- Status: `wip`
- Branch/worktree name: `kb.wdy-1344-mac-beta-support-matrix`
- Worktree path: `.worktrees/kb.wdy-1344-mac-beta-support-matrix`
- PR: https://github.com/wendylabsinc/WendyOS/pull/925 — draft
- PR closing reference: `Closes WDY-1344`
- Base: `main` after PR #906 merge
- Current commit: `1f144158 docs: add Mac beta support matrix`
- Validation:
  - `cd go/internal/cli/assets/docs && npm run types:check` passed
- `HANDOVER.md`: not written; this issue predates the clarified handover-file protocol
- Resume command: `cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1344-mac-beta-support-matrix && ai --prompt "Read HANDOVER.md and continue work on WDY-1344. Commit often and push to the draft PR as you go."`
- Note: this issue was started before the clarified master-session protocol, so it already contains implementation work rather than only an empty setup commit.

### WDY-1378 — Document `platform: "darwin"` in `wendy.json`

- Status: `prepared`
- Branch/worktree name: `kb.wdy-1378-darwin-platform-docs`
- Worktree path: `.worktrees/kb.wdy-1378-darwin-platform-docs`
- PR: https://github.com/wendylabsinc/WendyOS/pull/926 — draft
- PR closing reference: `Closes WDY-1378`
- Base: `main`
- Current commit: `0dd69c23 chore: start WDY-1378 Darwin platform docs`
- Validation: not run; setup commit only
- `HANDOVER.md`: written in the worktree with issue context and commit/push guidance
- Resume command: `cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1378-darwin-platform-docs && ai --prompt "Read HANDOVER.md and continue work on WDY-1378. Commit often and push to the draft PR as you go."`

## Recommended order

### 1. WDY-1344 — Create Mac beta support matrix

Define the beta contract: what works, what is unsupported, what is planned, and
what must not be promised. This should drive subsequent docs, onboarding,
unsupported errors, and validation.

### 2. WDY-1348 — Document Mac beta known limitations

Turn the support matrix into clear user-facing limitations. If we released after
this, users should understand the beta boundaries.

### 3. WDY-1347 — Update onboarding copy to avoid over-promising hardware support

Make sure product/onboarding copy matches the support matrix and does not imply
Linux/WendyOS-level hardware access on macOS.

### 4. WDY-1351 / WDY-1358 — Make unsupported flows actionable

Improve unsupported Wi-Fi, Bluetooth, hardware, audio, GPU, camera, and related
macOS agent errors. Prefer messages that explain macOS beta limitations instead
of suggesting ineffective updates.

### 5. WDY-1357 — Document install, reset, uninstall, and troubleshooting

Add practical beta support docs for cleaning up, resetting state, uninstalling,
collecting useful context, and resolving common setup failures.

### 6. WDY-1345 / WDY-1346 / WDY-1350 — Verify smoke, native run, and app lifecycle flows

Record and close the core validation work:

- Mac beta smoke test
- Native macOS app run flow
- App lifecycle commands on Mac agent

Some of this was manually validated during WDY-1343 and can be summarized or
expanded depending on the issue scope.

### 7. WDY-1349 — Audit CLI commands against Mac beta support matrix

Systematically compare CLI behavior against the matrix and identify command
families that should be hidden, documented as unsupported, or improved.

### 8. WDY-1360 — Validate Mac beta on a clean Apple Silicon macOS device

Run a clean-machine validation pass before broader release confidence claims.

### 9. Automation and E2E follow-ups

Defer deeper automation until the release-facing contract is stable:

- WDY-1355 — Define Mac beta E2E/smoke subset
- WDY-1364 — Review Swift E2E suite against Mac beta contract
- WDY-1381 — Add platform-aware Swift E2E spec gates and reference rendering
- WDY-1382 — Add macOS agent device info E2E spec
- WDY-1383 — Add native Darwin SwiftPM wendy run E2E spec
- WDY-1384 — Add macOS unsupported hardware API E2E specs
- WDY-1385 — Add macOS CLI and agent release artifact smoke flow

Automation remains important, but it should encode a stable beta contract rather
than define it prematurely.

### Additional follow-ups already created

- WDY-1366 — Simplify Wendy Agent Linux and macOS installation docs
- WDY-1376 — Add macOS Wendy Agent security guidance for exposed port 50051
- WDY-1377 — Show macOS-specific unsupported messages for hardware APIs
- WDY-1378 — Document `platform: "darwin"` in `wendy.json`
- WDY-1379 — Add minimal native macOS SwiftPM deployment example
- WDY-1380 — Document Wendy Agent for macOS first-launch prompts
