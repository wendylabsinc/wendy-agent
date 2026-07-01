# Wendy Companion App & SDK Coordinator Handover

Read `PLAN.md` first. This worktree is for coordinator planning only; do not
implement companion app, companion SDK, TestFlight, or Skip research work here.

## What this coordinator is for

Use this coordinator to manage the Wendy Companion App & SDK workstream across:

- iOS app repo: https://github.com/wendylabsinc/wendy-companion-ios
- Swift SDK repo: https://github.com/wendylabsinc/wendy-companion-sdk
- Linear project: **Wendy Companion App & SDK**
- Linear initiative: **Companion App & SDK**

The app repo is the primary Swift iOS app, mostly authored by Joannis. The SDK
repo is the related Swift package.

## Important context

- This coordinator lives inside the WendyOS repo only for planning/handoff.
- Implementation should happen in dedicated worktrees in the correct companion
  repository, not here.
- Always list Linear issue titles alongside issue IDs.
- Use Linear GraphQL via `LINEAR_API_KEY` if the Linear CLI is unavailable.
- Keep resume prompts generic:

  ```sh
  ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

## Current queued issues

- **REL-65 — Release Wendy OS companion app to TestFlight (upload + certificates/secrets)**
  - Project: Wendy Companion App & SDK
  - State: In Progress
  - Assignee: `konstantin@wendy.sh`
  - Likely repo: `wendy-companion-ios`
  - Status: blocked on Apple signing/Xcode Cloud/App Store Connect ownership
    alignment and real iOS Apple Distribution credentials.

- **REL-66 — Check out skip.dev for Wendy Companion App**
  - Project: Wendy Companion App & SDK
  - State: Backlog
  - Assignee: `konstantin@wendy.sh`
  - Likely repo: `wendy-companion-ios` or research-only handoff

- **WDY-1735 — Add deep linking support to Companion app and SDK**
  - Project: Wendy Companion App & SDK
  - State: Backlog
  - Assignee: `konstantin@wendy.sh`
  - Likely repo: `wendy-companion-ios` and/or `wendy-companion-sdk`

- **WDY-1781 — Add linting and formatting for Wendy Companion App**
  - Project: Wendy Companion App & SDK
  - State: Backlog
  - Assignee: `konstantin@wendy.sh`
  - Likely repo: `wendy-companion-ios`

- **REL-69 — Prepare Wendy Companion App Store release listing and submission**
  - Project: Wendy Companion App & SDK
  - State: Backlog
  - Assignee: `konstantin@wendy.sh`
  - Likely repo: `wendy-companion-ios` or App Store Connect-only handoff

- **REL-70 — Prepare public TestFlight release for Wendy Companion App**
  - Project: Wendy Companion App & SDK
  - State: Backlog
  - Assignee: `konstantin@wendy.sh`
  - Likely repo: `wendy-companion-ios` or App Store Connect-only handoff

The **Wendy Companion App & SDK** Linear project has been updated to include both the
`REL` and `WDY` teams so these Release issues can be attached to it.

## First-session checklist

1. Inspect `PLAN.md`.
2. Verify Linear state for **REL-65 — Release Wendy OS companion app to
   TestFlight (upload + certificates/secrets)**, **REL-66 — Check out
   skip.dev for Wendy Companion App**, **WDY-1735 — Add deep linking support to
   Companion app and SDK**, **WDY-1781 — Add linting and formatting for Wendy
   Companion App**, **REL-69 — Prepare Wendy Companion App Store release listing
   and submission**, and **REL-70 — Prepare public TestFlight release for Wendy
   Companion App**.
3. If asked to start an issue, follow the issue start protocol in `PLAN.md`:
   assign/confirm assignee, clone the correct repo if needed, create a dedicated
   worktree/branch, add an empty setup commit, push, open a draft PR with a body
   file, and write that issue worktree's `HANDOVER.md`.
4. After meaningful planning changes, update `PLAN.md`, commit, and push this
   coordinator branch.

## Useful commands

Clone companion repositories if needed:

```sh
cd /Volumes/Projects/WendyLabs
gh repo clone wendylabsinc/wendy-companion-ios
gh repo clone wendylabsinc/wendy-companion-sdk
```

Check this coordinator status:

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.companion
git status --short --branch
```
