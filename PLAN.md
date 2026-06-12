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

TBD.

## Active / paused issues

- TBD.

## Candidate issue queue

- TBD.

## Recently completed

- TBD.

## Follow-ups / discoveries

- TBD.

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
