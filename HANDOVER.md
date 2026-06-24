# Wendy for Mac Coordinator — Handover

## Where you are

This is the Wendy-for-Mac coordinator/planning worktree. It was renamed from the old beta coordinator name.

- Repo root: `/Volumes/Projects/WendyLabs/wendy-agent`
- Coordinator worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wendy-for-mac`
- Branch: `kb.wendy-for-mac`
- Remote branch: `origin/kb.wendy-for-mac`
- Old path compatibility: `.worktrees/kb.macos-beta` is currently a symlink to `.worktrees/kb.wendy-for-mac` so older running sessions/tooling do not break.

This session is for coordination only: Linear updates, planning, creating issue worktrees, setup commits, draft PRs, and handoff files. Do **not** do feature implementation in this coordinator worktree. Implementation happens in per-issue worktrees.

## Current local state

- `PLAN.md` is the durable coordination ledger.
- `HANDOVER.md` is this file.
- There is an untracked `install.tape` in this worktree; it pre-existed the rename and has been intentionally left alone.
- Housekeeping snapshot, 2026-06-24:
  - Coordinator branch is up to date with `origin/kb.wendy-for-mac`.
  - WDY-1608 worktree is clean except for its untracked local `HANDOVER.md`; branch is pushed at `096fd64e`.
  - WDY-1724 is now canceled in Linear and PR #1146 was closed unmerged; worktree remains locally at setup commit `3cf5bfb` with untracked `HANDOVER.md`.
  - WDY-1606 is Done; PR #1071 merged as `b9861453b`. Its worktree remains locally with untracked `HANDOVER.md`; leave it alone unless explicitly cleaning merged worktrees.
- Recent coordinator commits:
  - `bb880460 docs: record WDY-1724 cancellation`
  - `13e49453 docs: start WDY-1724 setup cleanup`
  - `5004d1a5 docs: add Wendy for Mac coordinator handover`
  - `82b9da05 docs: rename Wendy for Mac coordinator`
  - `f2d1459d docs: record WDY-1608 template handoff`
  - `60b9084f docs: add VHS terminal recording plan`

Before doing new coordinator work, run:

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wendy-for-mac
git status --short --branch
git pull --ff-only
```

## Project scope/status

The original `Wendy for Mac — Beta` Linear project is completed/closed. This coordinator now tracks Wendy-for-Mac planning when explicitly asked, including production, templates, demos, or E2E work that is clearly tied to Wendy for Mac.

Do not use this worktree for unrelated general CLI coordination.

Relevant Linear projects:

- `Wendy for Mac — Beta` — completed
  - URL: https://linear.app/wendylabsinc/project/wendy-for-mac-beta-22afd1281b23
  - Latest closeout update: https://linear.app/wendylabsinc/project/wendy-for-mac-beta-22afd1281b23/activity#project-update-ffafe0dd
- `Wendy for Mac — Production` — started
  - URL: https://linear.app/wendylabsinc/project/wendy-for-mac-production-a3cf67464606
  - Latest update posted from this session: https://linear.app/wendylabsinc/project/wendy-for-mac-production-a3cf67464606/activity#project-update-ed2759dd
- `New Templates` — relevant for Mac template work
  - WDY-1530 and WDY-1608 live here.

## Working protocol for starting issues

For each issue this coordinator starts, follow the `PLAN.md` protocol:

1. Assign the Linear issue to `konstantin@wendy.sh`.
2. Move the Linear issue to `In Progress` if starting active work.
3. Put it in the appropriate Linear project if needed.
4. Create a dedicated git worktree and branch.
5. Add an empty setup commit.
6. Push the branch.
7. Create a draft PR from the setup commit using a real markdown body file, not inline escaped newlines.
8. Include `Closes WDY-xxxx` in mergeable implementation PR bodies.
9. Write `HANDOVER.md` in the issue worktree with durable implementation instructions.
10. Leave the user with the worktree path, PR link, and a short resume command:

```sh
cd /path/to/worktree && ai --prompt "Read HANDOVER.md and follow its instructions."
```

Implementation, validation, review-thread handling, and non-empty feature commits happen in the issue worktree, not here.

## Linear GraphQL usage

The Linear CLI was not available in this environment. Use Linear GraphQL directly with `LINEAR_API_KEY` from the environment.

Endpoint:

```text
https://api.linear.app/graphql
```

Useful IDs already discovered:

- WDY team / Engineering team: `658b3d04-9cb2-4ed0-bf59-3252d9d665c4`
- Konstantin Linear user: `52e444da-5cdb-4988-8a62-eede2c448f9b`
- `New Templates` project: `bad5fc83-7d68-4182-beae-f089044e28e9`
- `Wendy for Mac — Production` project: `6d0f4818-7518-4dfe-bc56-d0dbcca645cb`
- Engineering `Backlog` state: `b00e07fb-9e2c-47ce-97b1-6ed3f6be45a2`
- Engineering `In Progress` state: `63892137-38bf-45a0-aa92-7bb718ae2e35`

Python pattern used successfully:

```py
import os, json, urllib.request
headers = {
    "Content-Type": "application/json",
    "Authorization": os.environ["LINEAR_API_KEY"],
}
req = urllib.request.Request(
    "https://api.linear.app/graphql",
    data=json.dumps({"query": query, "variables": variables}).encode(),
    headers=headers,
)
with urllib.request.urlopen(req, timeout=20) as r:
    data = json.load(r)
```

## Recent coordination actions

### WDY-1724 — Clean up Swift E2E setup scripts for ephemeral local runs

The user asked to add newly-created WDY-1724 to this plan and start it. It was started per coordinator protocol, then canceled the same day after investigation showed no setup-script change was needed.

Final coordination state:

- Linear: https://linear.app/wendylabsinc/issue/WDY-1724/clean-up-swift-e2e-setup-scripts-for-ephemeral-local-runs
- State: Canceled
- Assignee: `konstantin@wendy.sh`
- Project: E2E Tests
- Issue worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1724-swift-e2e-setup-scripts`
- Branch: `kb.wdy-1724-swift-e2e-setup-scripts`
- Setup commit: `3cf5bfb chore: start WDY-1724 Swift E2E setup cleanup`
- PR: https://github.com/wendylabsinc/WendyOS/pull/1146 — closed unmerged
- Linear handoff comment: https://linear.app/wendylabsinc/issue/WDY-1724/clean-up-swift-e2e-setup-scripts-for-ephemeral-local-runs#comment-e859b04f

Reason for cancellation, from PR close comment:

- The failed run was not caused by the setup scripts.
- The local macOS route uses a GitHub-hosted runner (`runs-on: macos-26`), and GitHub-hosted ephemeral runners are expected to have passwordless sudo.
- The failing run behaved like the wrong environment: it ran as the `wendy` user without passwordless sudo or a TTY.
- Since the route worked previously, treat this as a GitHub Actions runner assignment/environment fluke unless it recurs.
- No setup-script changes are needed for WDY-1724.

Resume command: not needed; issue is canceled.

### WDY-1530 — Add Wendy for Mac templates to `wendy init`

The user initially asked to create a Linear issue for Wendy Agent for Mac templates, i.e. `wendy init --template` has no Mac templates yet.

A duplicate search found existing issue:

- WDY-1530 — Add Wendy for Mac templates to `wendy init`
- URL: https://linear.app/wendylabsinc/issue/WDY-1530/add-wendy-for-mac-templates-to-wendy-init
- State: Backlog
- Project: New Templates

No duplicate was created. WDY-1530 already covers adding a Wendy for Mac/macOS target, native macOS SwiftPM template, `platform: "darwin"`, filtering templates by target, and not implying Linux containers on Mac.

### WDY-1608 — Swift MLX-LLM Open WebUI template

The user then asked for Max’s requested template as a dedicated issue.

Request context:

> Can you extend the templates for swift for an llm one using MLX-LLM and openui?
>
> I'm sorry I meant https://openwebui.com
>
> It's basically a chat gpt framework UI you can point to a running llm backend

Created Linear issue:

- WDY-1608 — Add Swift MLX-LLM template for Open WebUI on Wendy Agent for Mac
- URL: https://linear.app/wendylabsinc/issue/WDY-1608/add-swift-mlx-llm-template-for-open-webui-on-wendy-agent-for-mac
- State: In Progress
- Assignee: `konstantin@wendy.sh`
- Project: New Templates

Started it properly per the plan:

- Issue worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1608-mlx-openwebui-template`
- Branch: `kb.wdy-1608-mlx-openwebui-template`
- Setup commit after stack rebasing: `16d2f1d6 chore: start WDY-1608 MLX Open WebUI template`
- Draft WendyOS PR: https://github.com/wendylabsinc/WendyOS/pull/1077
- Companion templates PR: https://github.com/wendylabsinc/templates/pull/47
- Both PR bodies contain `Closes WDY-1608`; avoid adding additional duplicate closing references elsewhere.
- Issue worktree contains its own local untracked `HANDOVER.md` with implementation scope and validation expectations.
- A Linear comment was added with the original worktree/PR handoff details.

Resume command for the implementation session:

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1608-mlx-openwebui-template && ai --prompt "Read HANDOVER.md and follow its instructions."
```

Current WDY-1608 state as of housekeeping on 2026-06-24:

- WDY-1606 / PR #1071 has merged, so WendyOS PR #1077 is now based on `main` rather than the old Brewfile branch.
- PR #1077 adds native Mac template target support in the CLI: `--target darwin` / `--target macos`, template filtering by target, Darwin init tests, and template smoke harness target selection.
- PR #1077 also carries two Mac-run fixes discovered during template validation: headless Xcode package macro builds and relaxed direct-agent keepalive pings for long-running Mac log streams.
- Companion templates PR wendylabsinc/templates#47 adds the macOS side of the shared `llm` template using Swift/MLX plus Open WebUI.
- Templates PR #47 is an open draft with green checks and recorded live validation on `mac-mini.local`: first run installed `uv`, installed Open WebUI 0.9.5, started the localhost MLX backend, served Open WebUI, and returned expected `/api/config` data.
- Remaining review order is likely: refresh/finish WDY-1608 PR #1077 on `main`, then finish/merge the companion templates PR #47.
- WDY-1530 should be reassessed after WDY-1608 lands because PR #1077 covers much of the native Mac template-target plumbing.

### Wendy for Mac — Production project update

The user asked to check latest Linear project updates and draft/post one in the same style for `Wendy for Mac — Production` based on recent progress.

Latest prior Production update was from 2026-05-15:

> No noteworthy progress on the Mac version in the past two weeks, all efforts go towards E2E tests/specs.

A new update was posted with health `onTrack`:

- URL: https://linear.app/wendylabsinc/project/wendy-for-mac-production-a3cf67464606/activity#project-update-ed2759dd
- Body summarized:
  - Production is moving again after beta closeout.
  - Native Darwin SwiftPM and Xcode/HelloMLX paths are validated and backed by clearer CLI guardrails, resource syncing, lifecycle behavior, and project-shape selection.
  - WDY-1606 Brewfile support merged as the first production dependency-management improvement.
  - WDY-1530 and WDY-1608 carry template follow-through adjacent to Production.
  - Backlog cleanup reduced ambiguity by canceling stale beta/post-beta umbrellas or marking duplicates.
  - Current state at the time: ~15% complete in Linear scope: 4 done, 1 in review, 1 todo, 22 backlog, 6 canceled, 2 duplicates.
  - Next big product decision: Docker-backed Linux containers on Mac vs continuing to tighten the native macOS app path.

### Coordinator rename

The user asked to rename the worktree to `kb.wendy-for-mac` and update the plan.

Completed:

- Moved worktree from `.worktrees/kb.macos-beta` to `.worktrees/kb.wendy-for-mac`.
- Renamed local branch from `kb.macos-beta` to `kb.wendy-for-mac`.
- Pushed `origin/kb.wendy-for-mac` and set upstream.
- Deleted `origin/kb.macos-beta`.
- Updated `PLAN.md` with coordinator identity and broader Wendy-for-Mac scope.
- Commit: `82b9da05 docs: rename Wendy for Mac coordinator`
- Left `.worktrees/kb.macos-beta` as a symlink to `.worktrees/kb.wendy-for-mac` for session/tool compatibility.

## Current Production project issue snapshot from Linear

At the time of the latest project update, `Wendy for Mac — Production` had:

- 36 total issues
- 4 Done
- 1 In Review
- 1 Todo
- 22 Backlog
- 6 Canceled
- 2 Duplicate

Notable active/recent issues:

- WDY-1606 — Add Brewfile support for Wendy Agent for Mac apps
  - State: Done
  - PR: https://github.com/wendylabsinc/WendyOS/pull/1071 — merged as `b9861453b`
  - URL: https://linear.app/wendylabsinc/issue/WDY-1606/add-brewfile-support-for-wendy-agent-for-mac-apps
- WDY-1362 — Enable Linux container support on Mac via Docker
  - State: Todo
  - URL: https://linear.app/wendylabsinc/issue/WDY-1362/enable-linux-container-support-on-mac-via-docker
- WDY-1590 — Support `.xcworkspace` projects for Wendy Agent for Mac
  - State: Backlog
  - URL: https://linear.app/wendylabsinc/issue/WDY-1590/support-xcworkspace-projects-for-wendy-agent-for-mac
- WDY-1480 — Add proper mTLS support for Wendy for Mac
  - State: Backlog
- WDY-1492 — Explore USB pass-through for Linux containers on Wendy for Mac
  - State: Backlog

Done production items included:

- WDY-948 — Set up integration tests for Wendy for Mac
- WDY-963 — Properly sync SwiftPM resource bundles
- WDY-970 — Project-shape picker when Dockerfile/Package.swift/Xcode project coexist
- WDY-974 — Configure various Swift settings for maximum robustness

Cleaned-up/canceled/duplicate items included:

- WDY-1347 — onboarding copy; canceled
- WDY-1349 — CLI audit against Mac beta support matrix; canceled
- WDY-1355 — Mac beta E2E/smoke subset; canceled
- WDY-1357 — install/reset/uninstall/troubleshooting docs; canceled
- WDY-1364 — review Swift E2E suite against Mac beta contract; canceled
- WDY-1366 — simplify Linux/macOS install docs; canceled
- WDY-1358 — unsupported macOS agent error rendering; duplicate
- WDY-1363 — verify Linux container Docker flow on Mac agent; duplicate of/covered by Docker support direction

## Product/coordination context to preserve

### Beta closeout baseline

Wendy for Mac beta is effectively ready/completed from the core product path:

- Install Wendy Agent for Mac.
- Verify with `device info`.
- Discover/select or explicitly target the Mac agent.
- Run native macOS SwiftPM app flow.
- Run Xcode/HelloMLX flow.
- Manage deployed app lifecycle.
- Public/docs status says beta and does not imply Linux container or broad hardware API support.
- Unsupported Mac-agent commands preserve contextual “not supported on Wendy Agent for Mac” errors.
- `wendy run` rejects unsupported Mac-target project shapes early.

Completed beta PRs called out in `PLAN.md` include:

- PR #996 / WDY-1529 — contextual macOS unsupported errors.
- PR #999 / WDY-1531 — graceful rejection for Mac-target unsupported project types.
- PR #979 / WDY-1495 — smaller HelloMLX model choices for constrained Macs.
- PR #963 / WDY-1360 — clean Apple Silicon validation.
- PR #957 / WDY-1353 — Xcode/HelloMLX run flow.
- PR #936 / WDY-1346 — native macOS SwiftPM run flow.

### KISS guidance

For Wendy for Mac coordination, keep the beta-era KISS guidance unless explicitly told otherwise:

- Match Linux/WendyOS support level first; do not add a better support surface only for Mac unless product explicitly asks.
- Do not add broad diagnostics, reset/uninstall docs, firewall/VPN recipes, first-launch prompt guides, command-by-command matrices, or new E2E infrastructure unless explicitly requested.
- Native Darwin SwiftPM and Xcode are the supported Mac app path.
- Linux containers on Mac agents are unsupported until a dedicated Docker/container issue changes that.

### Templates direction

There are two related template issues:

- WDY-1530: adds the Mac template target/path to `wendy init --template`.
- WDY-1608: adds the requested Swift MLX-LLM/Open WebUI template.

Current implementation split:

- WendyOS PR #1077 covers the CLI/native Mac target side and is now based on `main` after WDY-1606 PR #1071 merged.
- `wendylabsinc/templates` PR #47 covers the generated Swift MLX/Open WebUI template content.

The template implementation may live outside this repository in `wendylabsinc/templates`. This repo contains:

- CLI/template smoke script: `go/scripts/test-templates.sh`
- Template schema docs: `docs/apps/template-schema.md`
- Existing Mac examples: `Examples/HelloMac/`, `Examples/HelloMLX/`

If a future session implements template content, check whether the work belongs in the templates repo and whether this repo only needs CLI registry/test/docs changes.

## Useful commands

List relevant worktrees:

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent
git worktree list | grep -E 'kb\.wendy-for-mac|wdy-1608|wdy-1606'
```

Check current coordinator state:

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wendy-for-mac
git status --short --branch
git log --oneline -5
```

Resume WDY-1608 implementation:

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1608-mlx-openwebui-template && ai --prompt "Read HANDOVER.md and follow its instructions."
```

View WDY-1608 PRs:

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1608-mlx-openwebui-template
gh pr view 1077 --web
gh pr view 47 --repo wendylabsinc/templates --web
```

## Next likely actions

Depending on what the user asks next:

1. If continuing WDY-1608, open the issue worktree and follow its `HANDOVER.md`; #1077 is now based on `main` and has companion templates PR #47.
2. If cleaning up WDY-1606 after merge, remove/archive its local worktree only after confirming no local handoff notes are needed.
3. If WDY-1724 comes up again, start from the cancellation note above; do not resume implementation unless the runner-environment diagnosis changes.
4. If starting another Mac production issue, follow the coordinator protocol and add a new ledger entry to `PLAN.md`.
5. If updating Linear project status, query project updates first and match the existing concise narrative style with health `onTrack` unless evidence suggests otherwise.
6. If cleaning up coordination after a PR merges, update `PLAN.md` with final state, merge commit, validation, and resume command status.
