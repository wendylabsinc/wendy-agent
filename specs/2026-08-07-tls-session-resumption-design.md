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
  connection error. Full re-verification recurs at least weekly by
  construction, not merely by ticket lifetime: Go's TLS 1.3 server reissues a
  fresh ticket on *every* connection, including resumed ones, so a client
  that simply overwrote its cached ticket on each connect could chain
  resumptions indefinitely and never trigger another full handshake. Instead,
  `tlscache.Cache` keeps only the ticket produced by its **last full
  handshake** and discards (does not persist) any ticket minted on a resumed
  connection. Combined with Go's own `maxSessionTicketLifetime` (7 days,
  enforced by both client and server), that forces a full handshake — and a
  full ML-DSA re-verification — at least once a week, even for a client that
  connects every day; the agent's per-resumption client-cert-window check
  (§3) is a second, independent layer that also catches a cert expiring
  mid-week. Mesh agent-to-agent dials get the property differently: a resumed
  mesh connection re-runs the peer identity pin and full chain check on every
  connection (`NewClientTLSConfigExpectingPeer`'s `VerifyConnection`), so
  ticket chaining is harmless there and the in-memory mesh session cache
  needs no equivalent no-overwrite rule.
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
- Secret-Service (Linux) / DPAPI (Windows) ticket-store backends — the
  `sessionStore` interface leaves room for them, but the `0600` file default
  matches those platforms' existing key-storage posture for now.
- Moving the client certificate/private key out of `~/.wendy/config.json`
  into platform secret stores — **planned follow-up PR** (own spec: Keychain
  on macOS, migration of existing configs, headless/Linux behavior). The
  `sessionStore` interface introduced here should be designed so that PR can
  reuse it.
- Proto or RPC changes (there are none).

## Design

### 1. Client session cache — new package `go/internal/cli/tlscache`

Implements `tls.ClientSessionCache` on top of a small `sessionStore`
interface (`Get(key) []byte`, `Put(key, blob)`, `Delete(key)`), with
platform-appropriate backends. A session ticket is a bearer resumption
secret — possession lets the holder connect as the original client identity
for up to 7 days — so it goes into the platform secret store where one
exists.

- **Keying:** each `ConnectWithTLSAndPins` call constructs a cache instance
  bound to a precomputed store key
  `SHA256(host:port | SHA256(client leaf cert DER))`. The `sessionKey` string
  Go passes to `Get`/`Put` (remote address) is ignored. Keying by client cert
  is a **correctness requirement**: the ticket embeds the client identity
  verified at the original handshake, so a ticket obtained with org A's cert
  must never be offered when dialing with org B's cert.
- **Serialization:** `ClientSessionState.ResumptionState()` →
  `SessionState.Bytes()` on `Put`; `tls.ParseSessionState` +
  `tls.NewResumptionState` on `Get` (Go 1.21+ APIs; module is on Go 1.26).
- **macOS backend: file, same as every other platform.** *(Revised
  2026-08-09; this bullet originally made the Keychain the darwin default.
  See "Why the Keychain is not the default" below.)*
- **Keychain backend (opt-in, `WENDY_TLS_SESSION_STORE=keychain`)**, via
  `/usr/bin/security add-generic-password -U` / `find-generic-password -w` /
  `delete-generic-password` — the same subprocess pattern
  `wifi_scan_darwin.go` already uses. Service name `wendy-tls-session`,
  account = hex store key, data = base64 session blob. Items are created so
  that our own reads never trigger a Keychain prompt (no `-A`
  any-application grant; exact ACL flags verified on a real Mac during
  implementation — a prompting hot path would be a regression, and any
  prompt-or-denied read is treated as a cache miss). The Secure Enclave
  itself is not applicable: it protects asymmetric keys, not arbitrary
  secrets; the Keychain is the right primitive for a ticket blob. The
  `security` subprocess costs ~30–80ms on `Get` — noise next to the ~2.2s
  it saves, and the file backend is an env-var flip away if it ever matters.
  Honest caveat: a Keychain item's ACL trusts `/usr/bin/security` itself, so
  any process running as the same macOS user can read the item promptlessly
  via the same CLI we use — this is not protection against same-user
  malware. What the Keychain buys over a `0600` file is at-rest encryption
  while the keychain is locked (screen-locked/powered-off device) plus
  exclusion from Time Machine and iCloud backups; it is not a stronger
  same-user access boundary than the file backend.
- **Why the Keychain is not the default.** `/usr/bin/security` offers no way
  to suppress user interaction — `add-generic-password` has no
  no-interaction flag and `security` has no global one (checked against
  `security help`). In any context where the keychain search list does not
  resolve (a sandboxed process, a non-login session), macOS answers the write
  with a blocking **"A keychain cannot be found to store …"** modal. Because
  `Put` runs on a background goroutine that discards its result, that modal
  surfaces with no CLI context and nothing to correlate it to, and the user's
  only obvious escape ("Reset To Defaults") rewrites their keychain search
  list. A latency optimization whose fallback is a full handshake must never
  be able to interrupt the user, so the prompting path cannot be the one
  people get by default. Dropping it also removes three subprocess spawns per
  connection from a feature whose whole point is speed. The security delta is
  acceptable on the reasoning already stated two bullets up: the ticket is a
  7-day bearer secret derived from a client identity whose ML-DSA private key
  is itself unencrypted in `~/.wendy/config.json` on macOS too. Anyone who
  wants at-rest encryption while the keychain is locked can opt in, accepting
  that the write may prompt.
- **Linux/Windows backend: file**,
  `~/.wendy/tls-sessions/<hex(storeKey)>.tlssession`, mode `0600`, directory
  `0700`, atomic temp-file + rename writes (concurrent CLI processes are
  last-writer-wins, which is safe), opportunistic pruning of files older
  than 7 days (Go's `maxSessionTicketLifetime`) on `Put`. Rationale: the
  client's ML-DSA private key already lives unencrypted in
  `~/.wendy/config.json`, so a `0600` ticket file adds no new exposure class
  on these platforms; a Secret-Service (D-Bus) or DPAPI backend can slot
  into the same interface later without touching callers.
- **Override:** `WENDY_TLS_SESSION_STORE=keychain|file|off` forces a backend
  or disables ticket caching entirely (`off` is also the right setting for
  CI).
- **Async `Put`:** Go invokes `Put` while processing the server's
  post-handshake `NewSessionTicket` message; a blocking Keychain write there
  would stall the connection's read loop. `Put` snapshots the blob and
  persists on a background goroutine. If the process exits first, the ticket
  is lost and the next connect does a full handshake — harmless.
- **Failure behavior:** any read/parse/store error returns "no session" (and
  best-effort deletes the bad entry). The worst case is always a full
  handshake — i.e. today's behavior. Cache errors are never surfaced.
- **Self-refresh:** Go's TLS 1.3 server issues a fresh ticket on every
  connection, including resumed ones, so the stored blob is rewritten on
  each connect and never goes stale while in regular use.

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
| No/corrupt/unreadable session entry | Full handshake (today's path) |
| Keychain read prompts, is denied, or `security` fails (opt-in backend only) | Treated as cache miss → full handshake |
| Keychain *write* in a context with no resolvable keychain (opt-in backend only) | macOS raises a blocking modal; unavoidable via `security`, which is why this backend is not the default |
| Process exits before async `Put` persists | Ticket lost → full handshake next time |
| Agent restarted (ticket keys rotated) | Ticket undecryptable → full handshake |
| Client cert window lapsed inside ticket | Server declines → full handshake → normal cert errors if genuinely expired |
| Ticket from wrong client cert | Never offered (disk key includes cert fingerprint) |
| Old agent (no Wrap/Unwrap) ↔ new CLI | Default ticket flow or none; full handshake at worst |
| Concurrent CLI processes | Atomic rename, last-writer-wins |

## Testing

**Unit (`tlscache`):** cache logic tested against an in-memory fake
`sessionStore`: Put/Get round-trip across two cache instances (simulates
separate CLI processes); cert-fingerprint keying isolation (cert A's ticket
invisible when bound to cert B); corrupt blob → nil + entry deleted; async
`Put` completion. File backend: pruning of >7-day files, `0600`/`0700`
permissions, atomic write, and that the darwin default resolves to the file
backend rather than the Keychain. Keychain backend: unit-tested against a
faked `security` runner (argument construction, miss/denial handling) and
asserted to require an explicit opt-in; its real prompting behavior is no
longer on the hot path, so it is not part of the verification gate below.

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
