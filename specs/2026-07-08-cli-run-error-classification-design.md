# CLI `run` Error Sub-Classification (T1.3) — Design

**Date:** 2026-07-08
**Status:** Draft for review
**Parent:** `specs/2026-07-08-cli-activation-retention-design.md` (Tier 1, item T1.3 — deferred from the telemetry plan)

## Context

`errorClass` (`go/cmd/wendy/main.go:99-133`) maps a command error to a bounded `error_class` enum for analytics. It already unwraps gRPC (`status.FromError`) and context errors, so most agent-RPC failures classify correctly today. Everything else falls through to the catch-all `"other"` (`main.go:132`). Telemetry shows the largest failure bucket is `wendy run` errors in `other` — and code investigation confirms these are overwhelmingly **pre-RPC local failures**: build, config/validation, project-type detection, and device resolution. `other` is therefore opaque exactly where it matters most.

This design makes the highest-volume, most-actionable `wendy run` failures classifiable, without leaking message text.

## Findings (from code investigation)

- **Existing sentinels `errorClass` keys on:** `commands.ErrUserCancelled` (`helpers.go:583`), `commands.ErrDefaultCleared` (`helpers.go:587`).
- **`imageBuildFailedError`** (unexported, `commands/ocilayers.go:456`) is the only structural build-failure marker; it wraps only 3 of ~7 build call sites (`ocilayers.go:543,683,748`) and `errorClass` cannot see it.
- **Confirmed bug:** `runMacOSSwiftPMWithAgent` (`run.go:1050-1053`) returns `swifttoolchain.ErrUserCancelled` **raw**, while the other two call sites (`run.go:980-983`, `run.go:1247-1250`) remap it to `commands.ErrUserCancelled`. So a Ctrl-C during the macOS SwiftPM product picker is misclassified as `other` instead of `user_cancelled`. `swifttoolchain.ErrUserCancelled` is defined at `swifttoolchain/toolchain.go:32` and is not checked by `errorClass`.
- Everything else in run's failure surface (build, device-resolution, config/validation, transfer/readiness) is a bare `fmt.Errorf`/`errors.New` with no sentinel.

## Goals

1. Replace the dominant `wendy run` slice of `other` with meaningful, bounded classes.
2. Fix the `swifttoolchain.ErrUserCancelled` misclassification.
3. Preserve privacy (class carries no message text) and all user-facing error messages (wrap with `%w`).
4. Keep `other` as the honest residual — do not chase 100% coverage.

**Non-goals:** changing user-facing error text; classifying commands other than `run` (may extend later); a general classification framework.

## Proposed taxonomy

Ordered by expected volume. Each new class needs a sentinel/typed error the wrap sites carry and `errorClass` detects via `errors.Is`/`errors.As`.

| `error_class` | Maps to | Detection |
|---|---|---|
| `run_build_failed` | Dockerfile/BuildKit/Apple-Container solve failures: `ocilayers.go` `imageBuildFailedError` sites, `docker.go:1899` (Apple Container build), `docker.go:1374` (buildx registry-push build), `run.go:994/1066/1275` (Swift builds) | Export/widen `imageBuildFailedError` into a shared exported type applied at all ~7 build sites (currently 3) |
| `run_builder_unavailable` | Builder tooling/env, not the Dockerfile: `checkAppleContainerCLI` (`docker.go:1706-1716`), `ensureAppleContainerSystem` (`docker.go:1723-1793`), docker-not-installed (`docker.go:793`) | New sentinel `ErrBuilderUnavailable` |
| `run_no_device` | `helpers.go:749,1056,1902,2414,500`; `run.go:752,755,759,816-820`; `cloud_tunnel.go:389,462` | New sentinel `ErrNoDevice` |
| `run_project_mismatch` | `run.go:48-61` (`rejectUnsupportedMacRunProject`); `run.go:1390-1449` (project/platform incompatibility) | New sentinel `ErrProjectTargetMismatch` |
| `run_config_invalid` | `wendy.json` load/validate (`run.go:672,698,702`; `appconfig.Validate()` `appconfig.go:437+`); dockerfile/flag validation (`run.go:623-640`) | Wrap `ErrConfigInvalid` at ~6 sites |
| `run_registry_auth` | `docker.go:196` (mTLS cert required), `docker.go:171` (loopback-push guard), `docker.go:1637` (mTLS/Apple-Container mismatch) | New sentinel `ErrRegistryAuth` |
| `run_transfer_failed` | chunk push (`chunkpush.go`), file sync (`filesync.go:149`, `run.go:1088`), progress-stream-ended-without-completion (`run.go:312,360`) | New sentinel `ErrTransferFailed` — **verify volume before adding**; may stay `other` if low |
| `run_partial_multiservice` | `multibuild.go` `joinServiceErrors` aggregate | Typed aggregate exposing `Unwrap() []error`, OR have `errorClass` recurse and classify the first/majority cause |
| `user_cancelled` (existing) | Extend to also match `swifttoolchain.ErrUserCancelled`; fix the `run.go:1050` remap gap | Widen `errorClass`'s `errors.Is` set **and** fix the call site (belt-and-suspenders) |

`other` remains the residual for anything unmatched.

## Approach

**Sentinel/typed errors + `errors.Is`/`errors.As` in `errorClass`** — matches the existing `ErrUserCancelled` convention, is privacy-safe by construction (sentinel carries no message), and is the cheapest to review and test.

Rejected alternatives:
- **Typed error with a `Class string` field** — bigger structural change, no existing precedent, and invites message-text leakage into a free-form field (contradicts the `errorClass` privacy comment).
- **Classification hook/registry** — new indirection for ~15-20 concrete sites in one file family; overkill unless many more commands need this soon (not established).

New sentinels live in a new `go/internal/cli/commands/errors.go`. Build-failure detection reuses/exports `imageBuildFailedError` via `errors.As` (same mechanism as the existing private `isImageBuildFailure`).

## Scope / invasiveness

~20-25 call-site edits across `run.go`, `docker.go`, `ocilayers.go`, `multibuild.go`, `helpers.go`, plus new sentinel declarations and additive `errorClass` branches. Mechanical and low-risk — `%w` wrapping preserves `Error()` text so no user-facing message changes.

**Sequencing:** land `user_cancelled` fix + `run_build_failed` + `run_no_device` first (highest signal), then the rest. Ship behind the existing analytics stance; no new event names (reuses `error_class` on `command_executed`), so no cloud change required.

## Dependency

`errorClass` (`main.go:99-133`) was just modified by the Tier 0 telemetry work on branch `jo/retention-growth-plan`. This work must branch from / rebase onto that landed version — the two changes touch the same ~35-line function and will conflict on adjacent lines otherwise.

## Open questions

- `run_transfer_failed`: confirm its telemetry volume justifies a class before adding, else leave in `other`.
- `run_partial_multiservice`: recurse-and-classify (simpler) vs. a dedicated aggregate class (more informative) — decide during planning.
- Whether to extend the same taxonomy to `wendy watch` (shares `runCommand` and thus all these paths).
