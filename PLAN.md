# Companion App / SDK — Coordinator Work Plan

## Purpose

This worktree is for planning and session coordination for the Wendy Companion
App and Companion SDK workstream.

- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.companion`
- Branch: `kb.companion`
- Purpose: coordinator planning/session handoff only
- Important files: `PLAN.md`, `HANDOVER.md`

Do not implement companion app or SDK work in this coordinator worktree. Use it
to triage Linear, prepare dedicated implementation worktrees in the correct
repository, and write durable handoffs.

## Scope

Coordinate work across these repositories:

- iOS app repo: `wendylabsinc/wendy-companion-ios`
  - URL: https://github.com/wendylabsinc/wendy-companion-ios
  - Private Swift iOS app repository.
  - Root indicators: `WendyOS Companion.xcodeproj`, `WendyOS Companion/`,
    `WendyOS-Companion-Info.plist`.
  - Mostly authored by Joannis.
- Swift SDK repo: `wendylabsinc/wendy-companion-sdk`
  - URL: https://github.com/wendylabsinc/wendy-companion-sdk
  - Public Swift package repository.
  - Mostly authored by Joannis.
- Linear project: **Companion App / SDK**
  - URL: https://linear.app/wendylabsinc/project/companion-app-sdk-33c2dfb5f9ef
  - Status: In Progress
  - Target date: 2026-07-03
  - Project teams: Engineering (`WDY`) and Releases (`REL`)
- Linear initiative: **Companion App & SDK**
  - URL: https://linear.app/wendylabsinc/initiative/companion-app-and-sdk-a20345306f5d
  - Owner: `konstantin@wendy.sh`
  - Status: Active
  - Target date: 2026-07-07

## Coordinator policy

- Keep this plan current as the durable source of truth for Companion App / SDK
  coordination.
- Always list Linear issue titles alongside issue IDs.
- Prefer focused child issues and small implementation PRs over broad umbrella
  work.
- Keep issue-specific context in the relevant issue worktree's `HANDOVER.md`.
- Keep resume prompts generic so all durable context lives in files, not shell
  history.
- Implementation, validation, review-thread handling, and non-empty commits
  happen in per-issue worktrees in the companion app or SDK repository, not in
  this WendyOS coordinator worktree.
- Use Linear GraphQL via `LINEAR_API_KEY` if the Linear CLI is unavailable.

## Issue start protocol

For each issue this coordinator starts:

1. Confirm the canonical Linear issue and project. Include the issue title when
   reporting the ID.
2. Assign the Linear issue to the intended owner, usually
   `konstantin@wendy.sh` unless the user specifies otherwise.
3. Choose the correct implementation repository:
   - `wendy-companion-ios` for app/TestFlight/UI work.
   - `wendy-companion-sdk` for Swift SDK/API/transport work.
   - `WendyOS` only for cross-repo coordination or CLI/agent changes.
4. Clone the implementation repo under `/Volumes/Projects/WendyLabs/` if it is
   not already present.
5. Create a dedicated git worktree/branch for the issue in that implementation
   repo, ideally from freshly fetched `origin/main`.
6. Add an empty setup commit for the issue.
7. Push the branch.
8. Create a draft PR in the implementation repo using a real markdown body file,
   not an inline string with escaped newlines.
9. For mergeable implementation PRs, include the Linear issue link/closing
   reference in the PR body, for example `Closes REL-65` or `Closes WDY-1234`.
10. Write a `HANDOVER.md` file into the issue worktree. Put the real prompt
    content there: scope, constraints, validation, commit/push expectations, PR
    details, and known risks.
11. Leave the user with the worktree path, PR link, and a one-line command to
    resume from that worktree using a generic prompt:

    ```sh
    cd /path/to/worktree && ai --prompt "Read HANDOVER.md and follow its instructions."
    ```

## Current Linear project state

The **Companion App / SDK** project was updated to include the `REL` team as
well as `WDY`, so Release issues can live in the same project.

### Active / queued issues

#### REL-65 — Release Wendy OS companion app to TestFlight (upload + certificates/secrets)

- Linear: https://linear.app/wendylabsinc/issue/REL-65/release-wendy-os-companion-app-to-testflight-upload
- State: In Progress
- Team: Releases (`REL`)
- Project: Companion App / SDK
- Assignee: `konstantin@wendy.sh`
- Priority: High
- Repo: `wendylabsinc/wendy-companion-ios`
- Worktree: `/Volumes/Projects/WendyLabs/wendy-companion-ios/.worktrees/kb.rel-65-testflight`
- Branch: `kb.rel-65-testflight`
- Setup commit: `f4d8e1dcd958bd7c87c11b5993d35639cc44a215`
- Draft PR: https://github.com/wendylabsinc/wendy-companion-ios/pull/1
- Current HEAD: `7c0bf8981d2c60554342431b6a52347cf56aaba2` —
  `ci: skip inspection when secrets are unavailable`
- Status: started. Branch is pushed, draft PR is open, and PR body/handoff are
  current. Continue signing and TestFlight validation from the issue worktree.
- Notes: the app project is now `WendyCompanioniOS.xcodeproj` with scheme
  `WendyCompanioniOS`. The Companion SDK resolves from GitHub over HTTPS rather
  than a local sibling checkout. Current Release signing settings include bundle
  ID `dev.wendy.WendyCompanioniOS`, development team `3YVC792H3S`, manual
  signing, distribution identity `Apple Distribution`, provisioning profile
  `WendyCompanioniOSAppStoreProfile`, marketing version `0000.00.00`, and build
  number `00000000000000`. `xcodebuild -list` and a Debug iOS Simulator build
  pass; Release archive is blocked locally because that provisioning profile is
  not installed. A manual `Inspect App Store Certificates` workflow now exists
  on the branch to print public certificate metadata from the existing GitHub
  secrets using temporary files/keychain only; the PR-triggered run succeeded
  but skipped inspection because repository secrets are unavailable to
  `pull_request` events.
- Resume:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-companion-ios/.worktrees/kb.rel-65-testflight && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

#### REL-66 — Check out skip.dev for Wendy Companion App

- Linear: https://linear.app/wendylabsinc/issue/REL-66/check-out-skipdev-for-wendy-companion-app
- State: Backlog
- Team: Releases (`REL`)
- Project: Companion App / SDK
- Assignee: `konstantin@wendy.sh`
- Priority: No priority
- Likely repo: `wendylabsinc/wendy-companion-ios` or a research-only handoff
- Status: queued. Evaluate whether Skip can help the companion app ship across
  Apple platforms / Android, and record recommendation.
- Resume: not available until prepared.

### Candidate / related issues to triage

- WDY-1549 — Lead development of the hackathon companion app — Backlog,
  assignee `martien@wendy.sh`, project none.
- WDY-1585 — Build the hackathon companion app before the Wednesday engineering
  meeting — Backlog, assignee `martien@wendy.sh`, project none.
- WDY-1500 — Test Swift companion SDK on Linux for Wendy Cloud auth — Backlog,
  assignee `christos@wendy.sh`, project none.
- EVENT-12 — Define Munich hackathon scope and run a final rehearsal — In
  Review, assignee `joannis@wendy.sh`, event/hackathon context.

### Completed / historical context

- WDY-1235 — Establish Companion SDK release timeline — target July 3 — Done,
  assignee `joannis@wendy.sh`.
- WDY-1104 — Wendy Companion SDK: mTLS — Done, assignee `joannis@wendy.sh`.
- WDY-817 — Basic Wendy Companion SDK for iOS — Done, project Wendy Box: iOS
  Companion App.

## Repo discovery notes

Found with GitHub repository search:

- `wendylabsinc/wendy-companion-ios` is the primary iOS app repo.
- `wendylabsinc/wendy-companion-sdk` is the related Swift SDK repo.
- Both have recent May 2026 commits mostly by Joannis.

If local checkouts are needed:

```sh
cd /Volumes/Projects/WendyLabs
gh repo clone wendylabsinc/wendy-companion-ios
gh repo clone wendylabsinc/wendy-companion-sdk
```

## Cross-coordinator references

- General coordinator:
  `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.general`
- Wendy for Mac production coordinator:
  `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wendy-for-mac-production`
- E2E Tests coordinator:
  `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.e2e-tests`

## Issue record template

Copy this block when this coordinator starts tracking an issue.

```md
### REL-XXXX / WDY-XXXX — Issue title

- Linear: https://linear.app/wendylabsinc/issue/...
- State: TBD
- Team: TBD
- Project: Companion App / SDK
- Assignee: TBD
- Repo: `wendylabsinc/...`
- Worktree: `/Volumes/Projects/WendyLabs/<repo>/.worktrees/kb.issue-slug`
- Branch: `kb.issue-slug`
- Draft PR: TBD
- Setup commit: TBD
- Status: TBD
- Scope:
  - TBD.
- Constraints:
  - TBD.
- Validation expectations:
  - TBD.
- Resume:

  ```sh
  cd /Volumes/Projects/WendyLabs/<repo>/.worktrees/kb.issue-slug && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```
```

## Next recommended steps

1. For **REL-65 — Release Wendy OS companion app to TestFlight (upload + certificates/secrets)**,
   get the certificate inspection workflow onto the default branch and run it
   with `workflow_dispatch`, then install/confirm provisioning profile
   `WendyCompanioniOSAppStoreProfile` for team `3YVC792H3S` and verify the App
   Store Connect bundle ID `dev.wendy.WendyCompanioniOS` before re-running the
   Release archive.
2. Start **REL-66 — Check out skip.dev for Wendy Companion App** when the
   TestFlight release path no longer needs active coordination.
3. Commit and push coordinator plan updates after meaningful planning changes.
