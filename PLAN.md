# Wendy for Mac — Production Work Plan

## Purpose

This worktree coordinates the Linear project **Wendy for Mac — Production**.
It continues the coordinator workflow and lessons from the completed Wendy for
Mac beta project, but it should not implement issue work directly.

- Linear project: https://linear.app/wendylabsinc/project/wendy-for-mac-production-a3cf67464606/overview
- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wendy-for-mac-production`
- Branch: `kb.wendy-for-mac-production`
- Important file: `PLAN.md`
- Coordinator role: planning/session handoff only

Implementation, validation, review-thread handling, and non-empty feature/fix
commits happen in dedicated per-issue worktrees, not in this coordinator.

## Project handoff from Wendy for Mac — Beta

The **Wendy for Mac — Beta** project is closed. Its remaining open issues were
moved into **Wendy for Mac — Production**.

Beta closeout summary:

- Core native Mac path validated end-to-end: install Wendy Agent for Mac,
  connect with the CLI, discover/select the Mac target, run native SwiftPM
  macOS apps, run the Xcode/HelloMLX flow, and manage deployed app lifecycle
  commands.
- Clean Apple Silicon validation completed and fixed important issues such as
  Login Items auto-start registration and smaller HelloMLX model tiers for
  constrained Macs.
- Public/docs surfaces aligned: Mac install page is in navigation,
  `platform: "darwin"` is documented, support matrix exists, and public copy
  describes Wendy Agent for Mac as beta without over-promising Linux containers
  or broad hardware API support.
- Unsupported paths are clearer: contextual Mac-agent unsupported errors landed,
  and `wendy run` rejects unsupported Mac-target project shapes early while
  preserving native Darwin SwiftPM/Xcode flows.
- Broad beta umbrella cleanup was canceled once focused post-beta follow-ups
  existed.
- Final beta project update posted with `onTrack` health:
  https://linear.app/wendylabsinc/project/wendy-for-mac-beta-22afd1281b23/activity#project-update-ffafe0dd

## Lessons learned / production operating principles

1. **Preserve the shipped path before expanding scope.** Native Darwin SwiftPM
   and Xcode app deployment are the proven paths. Keep them working while adding
   production capabilities.
2. **Be explicit about target/platform mismatches.** When a Mac agent cannot
   support a project shape, fail early with a Mac-specific diagnostic instead of
   falling through to container, registry, local-tool, or agent-version errors.
3. **Do not imply Linux containers on Mac until implemented and verified.**
   Docker/Linux-container work belongs to focused production issues.
4. **Keep docs concise and aligned with Linux/WendyOS parity.** Avoid broad
   Mac-only troubleshooting matrices unless production scope explicitly requires
   them or Linux/WendyOS gets equivalent treatment.
5. **Prefer focused follow-ups over umbrella work.** If a broad issue only says
   “audit” or “create backlog,” close/cancel it once focused issues exist.
6. **Preserve Linux/WendyOS behavior.** Mac production work should not regress
   existing Linux/WendyOS CLI, agent, docs, or E2E behavior.
7. **Document supported vs unsupported behavior in PR bodies.** Especially for
   build/run, container, security, MCP, and hardware API work.
8. **Use real issue worktrees.** Coordinator sessions prepare work only; issue
   sessions implement and validate.
9. **Keep resume prompts generic.** Put durable context in `HANDOVER.md`, not in
   long `ai --prompt` strings.
10. **Linear CLI may be unavailable.** In this environment, previous sessions
    used Linear GraphQL via `LINEAR_API_KEY` when `linear` was not installed.

## Issue start protocol

For each issue this coordinator starts:

1. Confirm the issue belongs to **Wendy for Mac — Production** or explicitly
   record why it is being coordinated here.
2. Assign the Linear issue to `konstantin@wendy.sh`.
3. Move the Linear issue to `In Progress`.
4. Create a dedicated git worktree and branch for the issue.
5. Add an empty setup commit for the issue.
6. Push the branch.
7. Create a draft PR from the setup commit using a real markdown body file, not
   an inline string with escaped newlines.
8. For mergeable implementation PRs, include `Closes WDY-xxxx` in the PR body.
   Do not put closing references on non-merge audit artifacts.
9. Write a `HANDOVER.md` file into the issue worktree with scope, constraints,
   validation, commit/push expectations, PR details, and known risks.
10. Leave the user with the worktree path, PR link, and a one-line command to
    resume from that worktree:

    ```sh
    cd /path/to/worktree && ai --prompt "Read HANDOVER.md and follow its instructions."
    ```

## Current status snapshot

Housekeeping snapshot, 2026-06-12:

- Production project state: started.
- Production project open issues at handoff: 30.
- Beta project state: completed; open issues there: 0.
- No production issue has been selected by this coordinator yet.
- Next issues are **TBD** and should be chosen in the production coordinator
  session.

## Candidate queue

TBD. Choose the first production issue in a future coordinator session.

Use the backlog categories below as inputs, not as priority order.

### Backlog categories imported from beta

- **Linux containers / Docker on Mac** — enable and verify Docker-backed Linux
  container support, clarify USB pass-through possibilities, and keep hardware
  limitations explicit.
- **Security / mTLS / exposed port guidance** — align Wendy Agent for Mac with
  the broader WendyOS security model rather than inventing a Mac-only one.
- **MCP / container proxy behavior** — make supported/unsupported behavior clear
  for Mac agents.
- **E2E and release automation** — add platform-aware E2E gates, native Darwin
  run specs, device-info specs, unsupported hardware specs, and release artifact
  smoke flow when the production contract is stable.
- **Docs / examples / onboarding** — add production-quality but concise docs,
  examples, first-launch notes, troubleshooting, and onboarding wording as
  needed.
- **Platform semantics** — clarify `wendyos`, `linux`, and `darwin` semantics in
  `wendy.json` and build/deploy paths.
- **Mac app reliability / packaging / analytics** — production hardening such as
  crash reporting, analytics, packaging architecture, App Store/TestFlight
  decisions, process architecture, and real stats.

### Open production backlog snapshot at handoff

Do not treat this as priority order.

- WDY-1492 — Explore USB pass-through for Linux containers on Wendy for Mac
- WDY-1480 — Add proper mTLS support for Wendy for Mac
- WDY-1395 — Evaluate wendyos platform semantics in wendy.json
- WDY-1385 — Add macOS CLI and agent release artifact smoke flow
- WDY-1384 — Add macOS unsupported hardware API E2E specs
- WDY-1383 — Add native Darwin SwiftPM wendy run E2E spec
- WDY-1382 — Add macOS agent device info E2E spec
- WDY-1381 — Add platform-aware Swift E2E spec gates and reference rendering
- WDY-1380 — Document Wendy Agent for macOS first-launch prompts
- WDY-1379 — Add minimal native macOS SwiftPM deployment example
- WDY-1376 — Add macOS Wendy Agent security guidance for exposed port 50051
- WDY-1366 — Simplify Wendy Agent Linux and macOS installation docs
- WDY-1364 — Review Swift E2E suite against Mac beta contract
- WDY-1363 — Verify Linux container Docker flow on Mac agent
- WDY-1362 — Enable Linux container support on Mac via Docker
- WDY-1358 — Improve CLI rendering of unsupported macOS agent errors
- WDY-1357 — Document install, reset, uninstall, and troubleshooting
- WDY-1356 — Validate menu bar state against agent and app lifecycle
- WDY-1355 — Define Mac beta E2E/smoke subset
- WDY-1354 — Add clear unsupported MCP/container proxy behavior where needed
- WDY-1351 — Make unsupported Wi-Fi, Bluetooth, hardware, audio, GPU, and camera flows actionable
- WDY-1349 — Audit CLI commands against Mac beta support matrix
- WDY-1347 — Update onboarding copy to avoid over-promising hardware support
- WDY-1080 — Check network service order and warn user if configuration is suboptimal
- WDY-973 — Set up crash reporting and analytics
- WDY-972 — Release to Test Flight?
- WDY-971 — Release to Mac App Store?
- WDY-962 — Mac agent (Containerization framework): implement volume/persist entitlement support
- WDY-943 — Set up CodeQL for Wendy for Mac
- WDY-930 — Explore more packaging and process architecture options for Wendy on macOS
- WDY-855 — Grab-bag
- WDY-854 — Implement real container stats in the Swift mac prototype

## Active / paused issues

- TBD.

## Recently completed in this production coordinator

- TBD.

## Follow-ups / discoveries

- TBD.

## Issue record template

Copy this block when this coordinator starts tracking an issue.

```md
### WDY-XXXX — Issue title

- Linear: https://linear.app/wendylabsinc/issue/WDY-XXXX/...
- State: TBD
- Project: Wendy for Mac — Production
- Worktree: `/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-xxxx-slug`
- Branch: `kb.wdy-xxxx-slug`
- Draft PR: TBD
- Setup commit: TBD
- Status: TBD
- Scope:
  - TBD.
- Constraints:
  - Preserve validated native Darwin SwiftPM/Xcode flows unless the issue
    explicitly targets them.
  - Preserve Linux/WendyOS behavior.
  - TBD.
- Validation expectations:
  - TBD.
- Resume:

  ```sh
  cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-xxxx-slug && ai --prompt "Read HANDOVER.md and follow its instructions."
  ```
```

## Next recommended steps

1. Start a new AI session in this coordinator worktree.
2. Review this `PLAN.md` and current Linear production issues.
3. Choose the first production issue to start.
4. Follow the issue start protocol above.
5. Commit and push coordinator plan updates after meaningful planning changes.
