# TLS 1.3 Session Resumption for CLI → Agent mTLS Connections

**Date:** 2026-08-07
**Status:** Approved (design), implementation pending
**Branch:** `ed/tls-session-resumption`

## Problem

Every `wendy` CLI invocation is a fresh process that performs a full mTLS
handshake against the agent. Provisioned devices use ML-DSA (post-quantum)
certificate chains, and the full handshake — multi-KB certificate flights plus
custom ML-DSA chain verification — costs **~2.2s on a quiet Jetson/Pi and can
exceed 3s under load** (documented at `go/internal/cli/commands/helpers.go`,
`mtlsProbeTimeout`). This cost is paid on *every* command, because nothing is
cached across CLI processes: the client sets no `tls.Config.ClientSessionCache`.

A resumed TLS 1.3 handshake is PSK-only: no certificate exchange, no ML-DSA
math on the device, one round trip of symmetric crypto. Repeat connects should
drop from ~2.2s to single-digit/low-tens of milliseconds on LAN.

## Goals

- Repeat CLI → agent connects skip the certificate exchange via TLS 1.3
  session tickets, persisted across CLI process invocations.
- Security posture: full ML-DSA chain trust is anchored at the original
  handshake; resumed connections get **cheap re-checks** (client cert validity
  window) and stale tickets **downgrade to a full handshake**, never a
  connection error.
- Zero behavior change when no ticket exists, the ticket is invalid, the agent
  restarted, or either side predates this feature (old CLI ↔ new agent and new
  CLI ↔ old agent both fall back to today's full handshake).

## Non-goals

- Per-device "last known good" connection-parameter cache (separate follow-up;
  note: resumption makes the *successful* rung of the cert×port probe ladder
  nearly free, but wasted rungs before it still pay full handshakes).
- QUIC/HTTP-3 transport (separate spike + benchmark after this ships).
- Persisting agent-side ticket keys across restarts (weakens forward secrecy
  for key-management convenience; an agent restart costing one full handshake
  per device is acceptable).
- Proto or RPC changes (there are none).

## Design

### 1. Client session cache — new package `go/internal/cli/tlscache`

Implements `tls.ClientSessionCache`, disk-backed under
`~/.wendy/tls-sessions/` (via `config.ConfigDir()`).

- **Keying:** each `ConnectWithTLSAndPins` call constructs a cache instance
  bound to a precomputed disk key
  `SHA256(host:port | SHA256(client leaf cert DER))`. The `sessionKey` string
  Go passes to `Get`/`Put` (remote address) is ignored. Keying by client cert
  is a **correctness requirement**: the ticket embeds the client identity
  verified at the original handshake, so a ticket obtained with org A's cert
  must never be offered when dialing with org B's cert.
- **Serialization:** `ClientSessionState.ResumptionState()` →
  `SessionState.Bytes()` on `Put`; `tls.ParseSessionState` +
  `tls.NewResumptionState` on `Get` (Go 1.21+ APIs; module is on Go 1.26).
- **Files:** `<hex(diskKey)>.tlssession`, mode `0600`, directory `0700`.
  Atomic writes (temp file + rename); concurrent CLI processes are
  last-writer-wins, which is safe. Opportunistic pruning of files older than
  7 days (Go's `maxSessionTicketLifetime`) on `Put`.
- **Failure behavior:** any read/parse/IO error returns "no session" (and
  best-effort deletes the bad file). The worst case is always a full
  handshake — i.e. today's behavior. Cache errors are never surfaced.
- **Self-refresh:** Go's TLS 1.3 server issues a fresh ticket on every
  connection, including resumed ones, so the file is rewritten on each
  connect and never goes stale while in regular use.

### 2. Client wiring — `go/internal/cli/grpcclient/client.go`

`ConnectWithTLSAndPins` sets
`tlsCfg.ClientSessionCache = tlscache.ForTarget(address, certInfo)`.

- The existing `certs.BuildServerVerifyConnection` verifier runs on resumed
  connections too (Go restores `PeerCertificates` into `ConnectionState` from
  the cached session), so server-identity checks, pin checks, org checks, and
  the `OnServerIdentity` sink are unchanged. Its ML-DSA re-verification costs
  ~1ms on the desktop CLI; the device side is what resumption saves.
- Under `WENDY_TLS_DEBUG`, log `resumed=true/false` from
  `ConnectionState.DidResume` (observed inside the verify-connection hook).

### 3. Server — `go/internal/agent/mtls/server.go`

`NewTLSConfig` gains `WrapSession`/`UnwrapSession` implementations built on
the exported `(*tls.Config).EncryptTicket` / `DecryptTicket` helpers:

- **WrapSession** (original handshake): append a small encoding of the
  verified client leaf's `{NotBefore, NotAfter}` to `SessionState.Extra`,
  then `EncryptTicket`. Encoding: a `wendy-mtls/1:`-prefixed entry holding the
  two Unix-seconds values (`Extra` is a shared list — the prefix lets our
  entry coexist with any other component's and makes unknown versions
  detectable, which `UnwrapSession` treats as "decline").
- **UnwrapSession** (resumption attempt): `DecryptTicket`; if the ticket is
  undecryptable, the Extra metadata is missing/garbled, or the validity
  window has lapsed — applying the same `notBeforeFloor` clock-skew
  semantics the existing `buildVerifyPeerCertificate` uses — **decline
  resumption by returning `(nil, nil)`**. Go then continues with a full
  handshake, where `VerifyPeerCertificate` re-runs the complete ML-DSA
  verification and surfaces the existing, well-understood errors if the cert
  is genuinely bad. A stale ticket therefore self-heals silently instead of
  producing a hard failure loop.
- Go's server already refuses to resume a session carrying no client
  certificate when `ClientAuth >= RequireAnyClientCert`, and the per-RPC mTLS
  interceptors keep seeing `PeerCertificates` (restored from the ticket), so
  org enforcement is unchanged. Both facts are pinned by integration tests.
- Ticket keys remain Go's per-process defaults: agent restart invalidates all
  outstanding tickets (one full handshake per device afterwards).

### 4. Mesh bonus — `NewClientTLSConfig`

The agent→agent mesh client config gets a one-line in-memory
`tls.NewLRUClientSessionCache`; the agent is a long-lived process, so
in-memory is sufficient there and reconnects benefit for free.

## Error handling summary

| Failure | Outcome |
| --- | --- |
| No/corrupt/unreadable session file | Full handshake (today's path) |
| Agent restarted (ticket keys rotated) | Ticket undecryptable → full handshake |
| Client cert window lapsed inside ticket | Server declines → full handshake → normal cert errors if genuinely expired |
| Ticket from wrong client cert | Never offered (disk key includes cert fingerprint) |
| Old agent (no Wrap/Unwrap) ↔ new CLI | Default ticket flow or none; full handshake at worst |
| Concurrent CLI processes | Atomic rename, last-writer-wins |

## Testing

**Unit (`tlscache`):** Put/Get round-trip across two cache instances
(simulates separate CLI processes); cert-fingerprint keying isolation (cert A's
ticket invisible when bound to cert B); corrupt file → nil + file removed;
pruning of >7-day files; `0600`/`0700` permissions; atomic write.

**Integration (in-process TLS client + `mtls.NewTLSConfig` server over a real
listener, plus a gRPC-level pass using `mtls.NewServer`):**

1. Second connection resumes: `DidResume == true` on both ends;
   `VerifyPeerCertificate` counter shows exactly one full verification.
2. mTLS interceptors still observe the peer identity on a resumed connection.
3. Expired/garbled ticket metadata → `DidResume == false`, connection still
   succeeds via full handshake (decline, not error).
4. `notBeforeFloor` semantics honored in `UnwrapSession`.
5. Session-tickets-disabled server (old-agent stand-in) → full handshake both
   times, no errors.

**On-device verification (PR gate):** before/after `WENDY_TIMING` runs on the
Orin Nano showing the mTLS-attempts phase drop from ~2.2s to milliseconds on
repeat connects.

## Expected effect

Warm connect ≈ TCP (1 RTT) + TLS 1.3 PSK (1 RTT) + first RPC (1 RTT) —
roughly 5–30ms on LAN versus ~2.2s today, on every repeat `wendy` command
against a provisioned device.
