# WDY-1509 — Manually audit CLI surface against E2E stubs across Linux and Mac

You are working in the dedicated issue worktree:

`/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1509-cli-e2e-surface-audit`

Branch: `kb.wdy-1509-cli-e2e-surface-audit`
Draft PR: https://github.com/wendylabsinc/WendyOS/pull/982
Linear: https://linear.app/wendylabsinc/issue/WDY-1509/manually-audit-cli-surface-against-e2e-stubs-across-linux-and-mac

## Goal

WDY-1509 is now the umbrella/manual audit issue for the current `wendy` CLI
surface. Preserve the generated command surface dump, the lightweight Swift E2E
surface ledger, and the PR summary so the focused child issues can continue the
actual cleanup/reference work.

The durable coordinator plan lives at:

`/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.macos-beta/PLAN.md`

Use that coordinator plan for WDY-1511 through WDY-1517. Do not continue broad
implementation/reference cleanup in this WDY-1509 PR.

## Scope

Do:

- Preserve the generated command tree snapshot and note how it was produced.
- Preserve a lightweight command ledger or equivalent notes.
- Keep the ledger organized by the child issues now tracking the focused work:
  WDY-1511 through WDY-1517.
- Record known hidden/internal/deprecated/alias observations that child issues
  need for handoff.
- Update PR #982 with the narrow umbrella scope, validation, and child issue
  handoff.

Do not in this PR:

- Continue broad Swift E2E implementation/reference cleanup.
- Add or split E2E suites for the bucketed child issue work.
- Sample additional device/cloud routes unless needed to keep the umbrella
  ledger truthful.
- File new follow-ups unless the umbrella ledger uncovers a missing coordinator
  item.

Avoid:

- a full E2E infrastructure rewrite,
- requiring every CLI command to become automated immediately,
- broad new DSL work unless the current Swift Testing structure truly cannot
  express what is needed,
- mixing unrelated product fixes into the same PR,
- changing Wendy for Mac public support promises.

Tiny fixes are acceptable only if needed to make references truthful.

## Useful starting points

Command/help surface:

```sh
cd go
make build-cli
./bin/wendy --experimental-dump-help > ../.cli-surface-WDY-1509.json
```

If the built binary path differs, use the locally built `wendy` binary and record
it in the PR.

Existing E2E tests/stubs:

- `swift/WendyE2ETests/Tests/WendyE2ETests/`
- `swift/WendyE2ETests/Sources/WendyE2ETesting/`
- `swift/WendyE2ETests/Tests/WendyE2ETestingTests/`
- `swift/WendyE2ETests/README.md`

Related recent work:

- WDY-1481 — local E2E matrix coverage for macOS↔macOS and Ubuntu↔Ubuntu.
- WDY-1494 — Swift E2E route matrix / commented route ledger cleanup.

Related Mac-beta backlog/stub issues:

- WDY-1349 — Audit CLI commands against Mac beta support matrix.
- WDY-1364 — Review Swift E2E suite against Mac beta contract.
- WDY-1381 — Add platform-aware Swift E2E spec gates and reference rendering.
- WDY-1382 / WDY-1383 / WDY-1384 — smaller Mac-specific E2E specs.

## Current narrow workflow

1. Keep `.cli-surface-WDY-1509.json` as the captured surface snapshot.
2. Keep `swift/WendyE2ETests/CLI_SURFACE_LEDGER.md` as the handoff ledger for
   child issues, not as a fully reviewed contract.
3. Keep `PLAN.md` and this handover aligned with the coordinator plan's
   umbrella-only scope.
4. Validate only the lightweight artifacts touched here.
5. Update PR #982 body with:
   - how the command surface was captured,
   - what the ledger preserves,
   - which child issue owns each follow-up bucket,
   - what was intentionally left out of WDY-1509.
6. Leave WDY-1511 through WDY-1517 to their dedicated issue worktrees.

## Validation expectations

For the current umbrella-only PR, no Swift/Go behavior should change. Run
lightweight artifact validation instead, for example:

```sh
python3 -m json.tool .cli-surface-WDY-1509.json >/tmp/cli-surface-WDY-1509.json
python3 - <<'PY'
import json
from pathlib import Path
items = json.loads(Path('.cli-surface-WDY-1509.json').read_text())
commands = {item['command'] for item in items}
leaf = [item for item in items if not any(
    other != item['command'] and other.startswith(item['command'] + ' ')
    for other in commands
)]
assert len(items) == 135
assert len(leaf) == 106
PY
git diff --check
```

If a future WDY-1509 edit touches Swift E2E code or references, run the relevant
Swift tests under `swift/WendyE2ETests` and document the command in the PR body.

## Expected PR output

- Keep `Closes WDY-1509` in the PR body.
- Keep commits small and reviewable.
- Push all commits to `kb.wdy-1509-cli-e2e-surface-audit`.
- File follow-up Linear issues for product bugs or larger automation gaps rather
  than expanding this PR indefinitely.

## Current state

- Linear is assigned to `konstantin@wendy.sh`, in the `E2E Tests` project, and
  moved to In Progress.
- WDY-1509 is the umbrella/manual audit. Focused work is tracked by:
  - WDY-1511 — completion install hidden test seam.
  - WDY-1512 — hidden/deprecated/public alias policy.
  - WDY-1513 — host-only CLI E2E references.
  - WDY-1514 — OS imaging and update E2E references.
  - WDY-1515 — direct device command E2E references.
  - WDY-1516 — cloud-routed device E2E references.
  - WDY-1517 — build and run E2E references.
- Draft PR #982 should remain a small umbrella artifact PR with
  `Closes WDY-1509`.
