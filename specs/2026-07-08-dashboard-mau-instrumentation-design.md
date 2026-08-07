# Dashboard MAU / Retention Instrumentation (Tier 3) — Design

**Date:** 2026-07-08
**Status:** Draft for review
**Parent:** `specs/2026-07-08-cli-activation-retention-design.md` (Tier 3)
**Target repo:** `~/git/wendy/cloud` (the Swift gRPC backend) — **NOT** this repo. This doc lives here as a planning artifact; implementation belongs in the cloud repo, whose migrations are shared (`services/migrations`, golang-migrate) across the legacy Go (`./services`) and new Swift (`./swift`) servers.

## Context

Monthly analytics show real activity for the marketing site (GA4) and the CLI (Cloud SQL `cli_events`), but **authenticated dashboard activity reads 0** when queried from `audit_logs.actor_user_id` — despite 27 new dashboard user registrations in the window. This design determines why and proposes the minimal instrumentation to get dashboard MAU + retention.

## Findings (from code investigation)

### Why `audit_logs` shows 0 — it's the wrong source, and it's not even wired

- **Schema:** `services/migrations/000018_create_audit_logs_table.up.sql`. Table comment: *"Tracks important events like ownership transfers, member changes, and configuration updates."* `action` enum: `ownership_transferred, member_added, member_removed, member_role_changed, …`.
- It is, **by design, an admin/security audit trail scoped to org-management mutations** — never page views, logins, or "was active today" signals. Wrong grain for MAU.
- **On `origin/main` there are ZERO write sites** anywhere (Go or Swift) — the only `audit_logs` reference is the migration. `AuditService.LogEvent` + callers exist **only in unmerged worktree branches** (`services/internal/service/audit.go`, called from `organization_server.go`). So on deployed main, nothing writes it.
- Double-explained: wrong grain **and** not wired. It structurally cannot answer an MAU question.
- Red herring ruled out: `Pkicore_V1_AuditLogEntry`/`GetAuditLog` (`swift/Sources/Proto/pkicore`) is a private-CA cert-issuance audit, a different subsystem.

### Dashboard activity is genuinely uninstrumented

Nothing persists per-user authenticated activity: `users` has only `created_at`/`updated_at` (no `last_active_at`); no session table (Firebase holds auth state client-side, not mirrored to Postgres); no login/auth event log; no request-logging interceptor. `AuthContext.userID` is resolved per-request but is a `TaskLocal` used only for in-request authorization checks and then discarded. `cli_events` is anonymous (`anonymous_id`) and CLI-only.

### How the dashboard reaches the backend

`dashboard` (Next.js 15, `cloud.wendy.dev`) is a pure frontend; all product data flows via gRPC-web to the Swift `Broker` (`swift/Sources/Broker/main.swift`), with the user's Firebase ID token as `Authorization: Bearer` (`dashboard/src/lib/grpc-client-web.ts:8-53`). Every one of the ~19 handlers registered at `Broker/main.swift:268-289` is wrapped by the same interceptor stack (`main.swift:316-319`): `[UserIDInterceptor, RateLimitInterceptor]`.

## Goals

1. Get dashboard **MAU** (distinct active authenticated users / 30d) and **retention** (cohort by `users.created_at`).
2. Minimal, privacy-conscious change; must never be able to break a real dashboard request.

**Non-goals:** per-page/per-feature analytics; replacing `audit_logs` (leave it for its admin-audit purpose); marketing-site attribution (separate work).

## Approach

**Insertion point already exists:** `UserIDInterceptor` (`swift/Sources/GRPCServices/AuthInterceptor.swift:37-98`) resolves `userID` for every authenticated RPC and stores it in `AuthContext.$userID` before `next(...)`. This is the single choke point for 100% of dashboard traffic — no per-handler changes needed.

**Minimal design:**

1. **New table** (new migration, `services/migrations/0000XX_create_dashboard_activity.up.sql`), day-granularity, privacy-minimal:
   ```sql
   CREATE TABLE dashboard_activity (
       user_id VARCHAR(128) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
       organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
       activity_date DATE NOT NULL,
       last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
       PRIMARY KEY (user_id, activity_date)
   );
   CREATE INDEX idx_dashboard_activity_date ON dashboard_activity(activity_date);
   ```
   Upsert `ON CONFLICT (user_id, activity_date) DO UPDATE SET last_seen_at = NOW()` → ~1 row/user/day regardless of RPC volume. No method name, path, IP, or body — just "this user touched the dashboard on this day."

2. **New `ActivityRecorderInterceptor`** (mirror `RateLimitInterceptor.swift`, ~20 lines), registered **after** `UserIDInterceptor` so `AuthContext.userID` is populated:
   ```swift
   public struct ActivityRecorderInterceptor: ServerInterceptor {
       let recorder: DashboardActivityRecorder   // wraps PostgresClient, fire-and-forget upsert
       public func intercept<Input, Output>(...) async throws -> StreamingServerResponse<Output> {
           if let userID = AuthContext.userID { await recorder.recordSeen(userID: userID) }
           return try await next(request, context)
       }
   }
   ```
   Must be **fire-and-forget / best-effort**: a write error is logged, never propagated — instrumentation cannot break a real request (same defensive stance as `AuditService.LogEvent`).

**Rejected:** reusing the CLI telemetry pipeline (`POST /v1/telemetry/events` → `cli_events`). That path is intentionally **anonymous + unauthenticated + isolated** (`CLITelemetryHTTPServer.swift:7-13`, isolated so a CLI flood can't affect dashboard/device RPCs). Adding authenticated user IDs there is both an auth-model mismatch and a privacy regression. Reuse the *pattern*, not the endpoint.

## Design decision to resolve

`AuthContext` carries **no auth-method/channel field** — `UserIDInterceptor` (`AuthInterceptor.swift:59-90`) resolves `userID` from three credential types (Firebase JWT / PAT / client cert) but discards which. To split **dashboard MAU (JWT)** from **CLI-authenticated MAU (PAT/cert)**, add an optional auth-channel to `AuthContext`. Otherwise, ship a combined "authenticated product usage" metric first and split later.

## Constraints

- Migration lands in shared `services/migrations/` (applies regardless of Go/Swift split).
- Interceptor ordering: after `UserIDInterceptor` (same constraint documented for `RateLimitInterceptor`).
- Fire-and-forget writes only.
- Privacy: authenticated data, so `user_id`/`org_id` are acceptable, but keep to day-granularity + IDs — no per-RPC method/path/IP.

## Queries this unlocks

- **MAU:** `SELECT COUNT(DISTINCT user_id) FROM dashboard_activity WHERE activity_date >= NOW() - INTERVAL '30 days';`
- **Retention/cohort:** join `dashboard_activity` against `users.created_at` (e.g. the 27 recent signups) by signup week × subsequent active weeks.

## Open questions

- Split dashboard vs CLI-authenticated MAU now (add `AuthContext` auth-channel) or ship combined first?
- Retention window/policy for `dashboard_activity` rows (e.g. keep 13 months, then roll up)?
