# CLI Activation, Retention & Growth — Design

**Date:** 2026-07-08
**Author:** Joannis Orlandos (with Claude)
**Status:** Draft for review

## Context

A drop-off analysis of the past month's telemetry (Cloud SQL `cli_events`, GA4, `users`, `audit_logs`) produced these population figures:

- **CLI:** 323 unique anonymous CLI users/installations (Jun 8 → Jul 8).
- **Dashboard (authenticated):** 0 active users recorded — `audit_logs` has 0 events in the window, so dashboard activity is effectively **not instrumented** in that source.
- **New registered dashboard users:** 27 (`users.created_at`).
- **Marketing website:** 1,349 active users (GA4, 30 days), 16.1% returning, 27.3% engaged-session rate.

Recurrence among CLI users is shallow but has a real retained core:

- 75/323 used it on 2+ days (23.2%), 46 on 3+ (14.2%), 31 on 5+ (9.6%), 19 on 10+ (5.9%).
- Median active days per CLI user: 1. Mean: 2.5.

The original drop-off read was "61% of one-day users only ran completion/setup commands → activation/positioning problem." A code investigation revised the diagnosis materially (below).

## Revised diagnosis (grounded in code)

### 1. The "completion install" bucket is a Homebrew post-install artifact, not user behavior

The CLI emits exactly **one** telemetry event type, `command_executed`, fired once per process from `main()`:

- `go/cmd/wendy/main.go:34-36` — `cmd.ExecuteC()` then `trackCommand(...)`, the sole call site of `analytics.Track` in the codebase.
- `main.go:60-75` — `command_name = executed.CommandPath()`; no per-command tracking exists.

The Homebrew formula's post-install hook runs `wendy completion install` automatically on the user's machine:

- `.github/workflows/build.yml:1108-1109` (homebrew-tap PR body): *"Post-install: `wendy completion install` runs automatically to configure shell completions for the current user."*
- Analytics is **on by default on first run** (`go/internal/cli/analytics/analytics.go:62-93`; `root.go:54-68` only prints a notice, does not suppress the event).
- `IsCI()` (`internal/shared/env/env.go:36-55`) does not trip on an end-user machine, so the CI kill-switch does not apply to a `brew install`.

Result: `brew install wendy` fires `command_executed{command_name:"wendy completion install", success:true}` as the user's first — and for **141 people, only** — event. Other completion-install paths (ambient prompt `completion_prompt.go:128`, `wendy tour` `tour.go:935`) run **in-process** and are attributed to the real command, so they do not produce this artifact.

**Implication:** 141/248 (57%) of the "one-and-done" cohort are people who **installed via Homebrew and never ran a single real command**. This is a top-of-funnel measurement leak, not (only) an onboarding-UX failure. Every funnel/retention number is polluted because **install and usage emit the identical event**.

### 2. There is no activation-milestone concept in telemetry

Every command is the same `command_executed` event with `command_name`, `command_root`, `duration_ms`, `success`, `error_class` (on failure), plus CLI/OS/arch/`is_dev_build` (`analytics.go:26-38`). There is no `first_run`, `first_deploy`, or any milestone kind. Activation is therefore **currently unmeasurable** even in principle.

### 3. `error_class="other"` is a pure catch-all dominated by local `run` failures

`errorClass(err)` (`main.go:99-133`) recognizes cancellation and gRPC/context codes, then defaults to `"other"` (`main.go:132`). All local/business-logic failures — build failures, file/IO, validation, `fmt.Errorf`-wrapped domain errors — fall through to `other`. `wendy run`'s local build/deploy path (`internal/cli/commands/run.go`) is the biggest contributor. "run failed: other = 14 users" is really "14 unclassified local failures."

### 4. The onboarding wizard already exists but is hidden

`wendy tour` (`internal/cli/commands/tour.go`) encodes the exact happy path (existing-device scan → OS install → discover → template → create project → `wendy run` → completions → cloud; phases at `tour.go:452-455`) but is registered `Hidden` (`root.go:162-163`). Next-step guidance is **strong for `init`** (`init_cmd.go:718-724, 763-769, 1501` all point to `wendy run`) but **absent on success for `run`, `discover`, and `device info`** (`run.go:895-909`, `discover.go:188`, `device.go`).

### 5. Documented happy path

`os install → discover → run`, with `init` scaffolding the project `run` deploys (README `:17,43-65`; `root.go:105-125` command grouping). `cloud login`/`enroll-device` are the Cloud path; `tour` is the interactive embodiment of the happy path.

## Goals

1. Make activation **measurable** — separate install from usage, add milestone events.
2. Lift **activation rate** — get more installers to a first successful `wendy run`.
3. Improve **retention** of the activated core and re-engage installed-but-never-ran users.
4. Close the **measurement gap** on the dashboard and join the marketing → CLI funnel.

**Non-goals (this spec):** marketing-site redesign; pricing; dashboard feature work beyond instrumentation.

## North star & funnel metrics

- **Activation = first successful `wendy run` (deploy) within N days of install** (N to be set once data is clean; start with 7).
- **Funnel:** brew/other install → first real command → discover success → init success → first deploy success → day-2 return.
- **Retention:** weekly cohort retention segmented by activation state (activated vs installed-only).

## Approach — tiered roadmap

### Tier 0 — Fix measurement first (days). Everything else is unmeasurable until this lands.

- **T0.1 Stop install masquerading as usage.** Suppress the Homebrew post-install event or emit a distinct `install_completed` kind. Detection: `HOMEBREW_*` env and/or non-interactive stdin during a `completion install` invocation. Keep the artifact out of `command_executed`.
- **T0.2 Add milestone events.** Extend the event with a `kind`/enum: `first_run`, `first_real_command`, `discover_success`, `init_success`, `auth_success`, `first_deploy_success`. Emit once per anonymous_id (persist a marker under `~/.wendy/`).
- **T0.3 Re-run the drop-off query** with brew ghosts excluded to establish the true one-and-done rate and a clean activation baseline.

### Tier 1 — Quick activation wins (1–2 weeks).

- **T1.1 Un-hide and promote `tour`** as the default first-real-run experience. It already encodes the happy path; it is the highest-leverage asset and it is hidden.
- **T1.2 Add success next-step nudges** where missing: after `discover` success → "Run `wendy run`"; after `run` success → view logs / open app; after `device info`/`top`/`apps list` → nudge toward deploy (rescues the inspection-and-leave cohort). Central hook: `root.go` `PersistentPostRunE` next to `maybeShowOptimizeTip` (`root.go:83-97`), or per-command at the cited success sites.
- **T1.3 Sub-classify `other`**, especially `run` local errors → `build_failed` / `deploy_failed` / `no_device` / `validation`. Makes the largest failure bucket diagnosable.

### Tier 2 — Reliability & retention (2–6 weeks).

- **T2.1** Attack real blockers once T1.3 gives visibility: `device info grpc_unavailable` (12 users; connectivity), `run` failures, `os install` cancels/failures.
- **T2.2** Re-engagement path for installed-but-never-ran (the biggest leak).
- **T2.3** Invest in what the retained core does — discover/init/run/os-install all >57% among recurring users (templates, examples, app store).

### Tier 3 — Strategic bets (quarter).

- **T3.1 Instrument the dashboard** — authenticated activity is 0 in `audit_logs`; confirm the right source or add MAU/retention events. Currently flying blind on that funnel.
- **T3.2 Positioning + funnel join** — two personas surface (deploy/run adopters vs one-time diagnostic users). Decide the core loop, message it, and connect marketing site (1,349 users, 16% returning) → brew install → first deploy with source attribution.

## Privacy & measurement hygiene

Milestone events must preserve the existing privacy stance (no flag values, args, or error message text — `main.go:47-59,90-98`, `README.md:209-217`). Install-source attribution must not introduce PII. Milestone de-dup markers live locally under `~/.wendy/` alongside `analytics_id`.

## Risks & open questions

- **Homebrew formula lives in an external repo** (`wendylabsinc/homebrew-tap`); the T0.1 fix is CLI-side detection, not a formula change, so it is self-contained here.
- **N-day activation window** and the exact milestone set need a data pass (T0.3) to tune.
- **Dashboard source** — is `audit_logs` the wrong table, or is instrumentation genuinely absent? T3.1 must confirm before building.

## Deliverables

1. This design doc.
2. An implementation plan for **Tier 0 + Tier 1** (concrete CLI code changes / PRs).
3. Tiers 2–3 tracked as follow-on specs.
