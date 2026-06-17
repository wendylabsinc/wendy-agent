# General Coordinator Work Plan

## Purpose

This worktree is for top-level planning and session coordination across Wendy
Agent issues that do not have a more specific coordinator worktree.

- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.general`
- Branch: `kb.general`
- Purpose: coordinator planning/session handoff only
- Important file: `PLAN.md`

Do not implement issue work in this coordinator worktree. Use it to choose,
prepare, hand off, and track dedicated issue worktrees.

## Coordinator policy

- Keep this plan current as the durable source of truth for general issue
  coordination.
- Prefer focused child issues and small implementation PRs over broad umbrella
  work.
- When a specialized coordinator exists for a theme, either use that coordinator
  or record an explicit cross-reference here.
- Keep issue-specific context in the relevant issue worktree's `HANDOVER.md`.
- Keep resume prompts generic so all durable context lives in files, not shell
  history.

## Issue start protocol

For each issue this coordinator starts:

1. Assign the Linear issue to `konstantin@wendy.sh`.
2. Create a dedicated git worktree and branch for the issue.
3. Add an empty setup commit for the issue.
4. Push the branch.
5. Create a draft PR from the setup commit using a real markdown body file,
   not an inline string with escaped newlines.
6. For mergeable implementation PRs, include the Linear issue link/closing
   reference in the PR body, for example `Closes WDY-1234`, so merging the PR
   closes the issue. Do not put closing references on non-merge audit artifacts.
7. Write a `HANDOVER.md` file into the issue worktree. Put the real prompt
   content there: scope, constraints, validation, commit/push expectations, PR
   details, and known risks.
8. Leave the user with the worktree path, PR link, and a one-line command to
   resume from that worktree using a generic prompt:

   ```sh
   cd /path/to/worktree && ai --prompt "Read HANDOVER.md and follow its instructions."
   ```

Implementation, validation, review-thread handling, and non-empty commits happen
in per-issue worktree sessions, not in this coordinator worktree.

## Current focus

Started WDY-1532, WDY-1600, and WDY-1602 from the general queue. Coordinator setup is complete for all three; implementation must happen in the issue worktrees.

## Active / paused issues

### WDY-1532 — Support `wendy.json` file sync for WendyOS/Linux deployments

- Linear: https://linear.app/wendylabsinc/issue/WDY-1532/support-wendyjson-file-sync-for-wendyoslinux-deployments
- State: In Progress
- Project: none
- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1532-file-sync`
- Branch: `kb.wdy-1532-file-sync`
- Draft PR: https://github.com/wendylabsinc/WendyOS/pull/998
- Setup commit: `c3ace1815cef5bf69d1d9d72e312cffe52567131`
- Status: Handoff written; ready for implementation in issue worktree.
- Scope:
  - Make top-level `wendy.json.files` work for WendyOS/Linux `wendy run` deployments.
  - Document platform/build-path support, destination resolution, directory/file semantics, and stale/update behavior.
  - Add CLI/agent tests for at least one Linux/WendyOS deployment path.
- Constraints:
  - Keep implementation out of this coordinator worktree.
  - Preserve existing macOS/Darwin file-sync semantics unless sharing helpers requires tiny refactors.
  - Be explicit about multi-service/Compose support if it is out of scope.
- Validation expectations:
  - Focused Go tests for appconfig, CLI command file sync/run behavior, and agent container/spec behavior as changed.
- Resume:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1532-file-sync && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

### WDY-1600 — `wendy device update` reports success before restarted agent is reachable

- Linear: https://linear.app/wendylabsinc/issue/WDY-1600/wendy-device-update-reports-success-before-restarted-agent-is
- State: In Progress
- Project: none
- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1600-device-update-restart`
- Branch: `kb.wdy-1600-device-update-restart`
- Draft PR: https://github.com/wendylabsinc/WendyOS/pull/1064
- Setup commit: `9c1ea2432232ca99c3a436033af4eb37ca24f422`
- Status: Handoff written; ready for implementation in issue worktree.
- Scope:
  - First try to reproduce the reported post-update restart-gap failure.
  - Fix or clarify `wendy device update --binary` readiness semantics so success does not race the agent restart.
  - Add focused tests for the selected behavior.
- Constraints:
  - Keep the PR focused on device-update restart readiness.
  - Preserve JSON output semantics.
  - Avoid broad connection retry changes unless they are the smallest safe fix.
- Validation expectations:
  - Focused Go tests for changed CLI/agent packages.
  - Hardware/device reproduction and post-fix validation if available; otherwise document the unavailable hardware and cover behavior with tests/mocks.
- Resume:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1600-device-update-restart && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

### WDY-1602 — Fix Fumadocs audit failure from esbuild advisory

- Linear: https://linear.app/wendylabsinc/issue/WDY-1602/fix-fumadocs-audit-failure-from-esbuild-advisory
- State: In Progress
- Project: none
- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1602-fumadocs-esbuild-audit`
- Branch: `kb.wdy-1602-fumadocs-esbuild-audit`
- Draft PR: https://github.com/wendylabsinc/WendyOS/pull/1066
- Setup commit: `f423ef4d`
- Status: Handoff written; ready for implementation in issue worktree.
- Scope:
  - First reproduce the Fumadocs `npm audit --audit-level=high` failure.
  - Apply a minimal docs lockfile update for the esbuild advisory.
  - Avoid unsafe `npm audit fix --force` dependency churn.
- Constraints:
  - Keep the PR focused on docs dependency audit health.
  - Prefer lockfile-only changes unless validation proves `package.json` must change.
- Validation expectations:
  - `cd docs && npm audit --audit-level=high`
  - `cd docs && npm run build`
- Resume:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1602-fumadocs-esbuild-audit && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```

## Candidate issue queue

- WDY-1520 — Consider CLI IPv6-to-IPv4 fallback for device connections — https://linear.app/wendylabsinc/issue/WDY-1520/consider-cli-ipv6-to-ipv4-fallback-for-device-connections
- WDY-1498 — Add headless device-code flow for `wendy auth login` — https://linear.app/wendylabsinc/issue/WDY-1498/add-headless-device-code-flow-for-wendy-auth-login
- WDY-1497 — Explore `wendy.json` profiles for selectable app configurations — https://linear.app/wendylabsinc/issue/WDY-1497/explore-wendyjson-profiles-for-selectable-app-configurations
- WDY-1496 — CLI: support explicit `--config` path for `wendy.json` — https://linear.app/wendylabsinc/issue/WDY-1496/cli-support-explicit-config-path-for-wendyjson
- WDY-1472 — Plan Wendy Agent to Wendy Daemon rename timing — https://linear.app/wendylabsinc/issue/WDY-1472/plan-wendy-agent-to-wendy-daemon-rename-timing

## Recently completed

- TBD.

## Follow-ups / discoveries

- The `linear` CLI is not installed in this environment despite the Linear skill docs; coordinator used Linear GraphQL via `LINEAR_API_KEY` for WDY-1532 assignment/status.
- Pushing WDY-1532 to `origin` over the default SSH URL hung; pushing via `ssh://git@ssh.github.com:443/wendylabsinc/WendyOS.git` worked. The WDY-1532 worktree has `remote.origin.pushurl` set to that SSH-over-443 URL.
- The user clarified the intended issue was WDY-1600, not WDY-1063. WDY-1600 references the accidentally-created-and-closed GitHub issue https://github.com/wendylabsinc/WendyOS/issues/1063.
- WDY-1602 came from the Fumadocs failure on PR #1064; the blocking issue is `npm audit --audit-level=high` flagging locked `esbuild@0.28.0`.

## Cross-coordinator references

- Wendy for Mac beta coordinator:
  `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.macos-beta`
- TBD.

## Issue record template

Copy this block when this coordinator starts tracking an issue.

```md
### WDY-XXXX — Issue title

- Linear: https://linear.app/wendylabsinc/issue/WDY-XXXX/...
- State: TBD
- Project: TBD
- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-xxxx-slug`
- Branch: `kb.wdy-xxxx-slug`
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
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-xxxx-slug && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```
```

## Next recommended steps

1. Replace the `TBD` sections with the first real issue queue.
2. For the first selected issue, follow the issue start protocol above.
3. Commit and push coordinator plan updates after meaningful planning changes.
