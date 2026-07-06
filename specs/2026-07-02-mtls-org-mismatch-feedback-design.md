# mTLS org-mismatch feedback

**Date:** 2026-07-02
**Branch:** `jo/mtls-org-mismatch-feedback`

## Problem

When `wendy` connects to a device over mTLS and the handshake is rejected, the
user gets a generic message —
*"TLS handshake rejected by device (possible clock skew or cert mismatch)...
rerun with WENDY_TLS_DEBUG=1"* (`go/internal/cli/commands/helpers.go:66`). A
common real cause is that the device belongs to a **different organization** than
the one the CLI is operating as. The information needed to say so plainly is
present during the handshake — the device's server certificate carries its org
in a SAN URI (`urn:wendy:org:<org>:...`) — but we don't surface it.

The CLI already compares orgs and raises `certs.OrgMismatchError`
(*"server certificate belongs to org X, expected org Y"*) inside the
`VerifyConnection` callback (`go/internal/shared/certs/mldsa.go:196-207`). But
that comparison only runs **after** chain verification succeeds, so it never
fires on the two rejection paths that matter:

1. **Cross-org with a different CA** — chain verification (`mldsa.go:173`) fails
   first and returns before the org check is reached; the user sees the generic
   message even though we hold the server cert (with its org).
2. **The device rejects us** — the device's own server-side mTLS interceptor
   refuses our client cert (`remote error: tls: bad certificate`). In TLS 1.3 our
   `VerifyConnection` runs *before* that rejection, so we observed the server's
   org, but we don't capture it and it's lost by the time the error surfaces.

## Goal

On an mTLS rejection, capture the device's org from its server certificate,
compare it to the org the CLI is operating as, and — when they differ — tell the
user plainly and actionably.

## Constraints (imposed by existing code)

- **Org names are not available offline.** Local config
  (`go/internal/shared/config/config.go`) stores only numeric `OrganizationID`;
  the only source of a human-readable name is the cloud `ListOrganizations` RPC
  (`org_picker.go`). This design uses **numeric org IDs** and makes no network
  call.
- **Switching org is `wendy auth list-orgs` → press `d`.** There is no one-shot
  `--org` operating flag (the `--org` that exists is only for `--api-key` login,
  `auth.go:86`). The default org is `Config.DefaultOrgID`, set/cleared by the
  `list-orgs` picker (`org_picker.go:164-190`) and consumed by `ResolveAuth`
  (`auth_resolve.go:59-68`).
- **`grpc.NewClient` is lazy.** The TLS handshake — and therefore our
  `VerifyConnection` — runs on the first RPC, which is the `GetAgentVersion`
  probe (`helpers.go:1060`). So the server org is observable before the
  rejection error returns from that probe, on both failure paths above.

## Design

### Component A — Capture the server org in the verifier
`go/internal/shared/certs/mldsa.go`

Add one optional field to `ServerVerifyOpts`:

```go
OnServerIdentity func(WendyIdentity) // best-effort; fired before chain/org checks
```

In the `VerifyConnection` closure, extract the identity from the leaf and fire
the sink **at the top**, before chain verification — so it captures on success,
on chain-verify failure, and before any client-cert rejection:

```go
leaf := state.PeerCertificates[0]
if opts.OnServerIdentity != nil {
    if id, ok, err := IdentityFromCert(leaf); ok && err == nil {
        opts.OnServerIdentity(id)
    }
}
// ... existing chain verification, org-mismatch check, pin check unchanged
```

The existing `OrgMismatchError` behavior is unchanged. Capture is best-effort: if
`IdentityFromCert` fails, nothing is captured and behavior is exactly as today.

### Component B — Thread the captured org out to the caller
`go/internal/cli/grpcclient/client.go`

`ConnectWithTLSAndPins` allocates a capture sink, wires `OnServerIdentity` into
`ServerVerifyOpts`, and exposes the observed org on the returned
`*AgentConnection`:

```go
func (c *AgentConnection) ObservedServerOrg() (int32, bool)
```

The closure runs on the handshake goroutine; the caller reads only after the
probe RPC returns. Back the value with `sync/atomic` (an `atomic.Int64` holding
`orgID+1`, 0 = unset, or an `atomic.Int32` + `atomic.Bool`) so the cross-goroutine
read is race-free rather than relying on ordering by convention.

### Component C — Enrich the error
`go/internal/cli/commands/helpers.go`

In `connectWithAutoTLSDiagnostics`, when the probe fails and is classified as a
cert rejection (`isCertRejectionError`), read `conn.ObservedServerOrg()`. If an
org was observed and differs from the cert's `ExpectedOrgID`, construct an
org-mismatch error (Component D message) instead of the generic
`errTLSHandshakeRejected`.

Also add an `errors.As(err, &certs.OrgMismatchError{})` check on this path: the
*client-rejects-device* case raises `OrgMismatchError`, whose text has no
`remote error: tls:` marker, so it is currently swallowed into the generic
"handshake rejected" bucket on the LAN path (BLE already handles it via
`errors.As`). Detecting it here routes it to the same clear message.

### Component D — Remedy lookup + message
Small helper in `go/internal/cli/commands/helpers.go`.

**Important:** `connectWithAutoTLSDiagnostics` calls `loadAllCLICerts()`
(`helpers.go:1014`) — every cert across **all** the user's orgs — and its loop
(`helpers.go:1044`) tries *all of them*, ignoring `DefaultOrgID`. So if the user
has a usable cert for the device's org, the connection simply succeeds; the
rejection branch is reached only when **none** of the user's certs work for that
org. Consequently "switch your default org" is *not* a valid remedy on this path
(switching the default changes nothing about which certs are tried). The message
reflects what actually helps: log in with an account that can access the org, or
refresh a stale cert.

The new error fires for exactly one situation — the device's org is one the
user holds **no** cert for (a genuine cross-org mismatch):

> This device belongs to org `<X>`; your credentials cover org(s) `<Y…>`. Your
> account isn't a member of org `<X>` — run `wendy cloud login` with an account
> that can access org `<X>`.

Gate: build this error only when the observed device org is set **and not**
among the user's cert orgs. Two adjacent cases are deliberately left to existing
handling:

- **Observed org *is* one the user holds** (same-org failure — clock skew, or an
  expired/invalid cert for that org): falls through to the existing
  `errTLSHandshakeRejected` message, which `connectToAgent` already post-processes
  with the clock-skew retry and the `wendy auth refresh-certs` offer
  (`offerCertRefreshAndRetry`). Component D must not duplicate that.
- **No org observed** (device presented no Wendy identity, or a non-cert
  transport failure): falls through to `errTLSHandshakeRejected` as today.

## Scope

- Component A lives in the shared verifier, so **all** callers (LAN, cloud
  tunnel, MCP, BLE) capture the server org automatically.
- Components C and D target the **primary LAN/direct device path**
  (`connectWithAutoTLSDiagnostics` → `connectToAgent`). BLE already surfaces
  `OrgMismatchError` via `errors.As`. Cloud-tunnel and MCP use the same seam and
  can adopt `ObservedServerOrg()` in a later pass — not built here.
- No cloud name lookup (offline, fast, IDs only).

## Testing

- **`certs` package** — table-driven test that `OnServerIdentity` fires with the
  correct org on (a) full success, (b) chain-verify failure, (c) org mismatch,
  constructing test certs the way existing `mldsa` tests do.
- **Message builder** — unit test mapping `(observedOrg, expectedOrg,
  hasLocalCert)` to the expected copy.

## Isolation

Implemented in a dedicated git worktree (`../wendyos-wt-mtls-org`, branch
`jo/mtls-org-mismatch-feedback`) because the main tree is shared by concurrent
sessions that switch branches mid-run.
