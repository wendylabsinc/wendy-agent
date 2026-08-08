# Last-Known-Good Connect Cache

**Date:** 2026-08-08
**Status:** Approved (design), implementation pending
**Branch:** `ed/lkg-connect-cache` (stacked on `ed/instant-mdns-discovery`, PR #1616)

## Problem

With TLS session resumption (PR #1612) in place, the mTLS handshake on a
repeat connect costs ~141ms — but connecting to a device by its `.local`
name still takes ~1.5s, measured live on macOS (Jetson Orin Nano over LAN):
mDNS name resolution contributes a constant ~1.2–1.4s per CLI invocation,
and on a cache-cold path the sequential cert × port probe ladder in
`connectWithAutoTLSDiagnostics` adds more. The device's address, mTLS port,
and org were almost always known from a previous command — nothing reuses
them.

PR #1616 already persists exactly this data: `discoverycache`
(`~/.wendy/devices.json`) stores per-device `Hostname`, `IP`, `Port`,
`MTLS`, `OrgID`, `LastSeen` with best-effort merge-on-flush semantics. This
feature makes the connect path a reader (and refresher) of that cache.

## Decisions (made during design review)

- **Stacked on PR #1616** and reusing `discoverycache` — one cache, one
  file; discovery pre-warms connects, connects refresh discovery. Merge
  order: #1616 first.
- **Any-age fast path**: connect attempts use a cached IP regardless of the
  cache's 1h TTL (the TTL remains a *display*-freshness bound for the
  picker). Rationale: a stale IP costs ≤1s (bounded TCP connect) against a
  guaranteed ~1.3s saving, and the fallback path does fresh resolution, so
  staleness self-heals.

## Design

### 1. `discoverycache` additions: hostname lookup + refresh-only write-back

- `func (c *Cache) ByHostname(host string) (Entry, bool)` — returns the
  entry whose `Hostname` matches `host` after normalization on both sides
  (lowercase, strip trailing dot, strip `.local` suffix — same rules as
  `normalizeMDNSHost` in `helpers.go`). Requires a non-empty `IP`; when
  several entries match (shouldn't happen, but the cache is keyed by device
  id, not hostname), the most recent `LastSeen` wins.
- Connect-path write-back **only refreshes existing entries**: after a
  successful connect to hostname H at ip:port, look up `ByHostname(H)` and
  `Upsert` that entry with the fresh `IP`, `Port`, `LastSeen` (and `OrgID`
  when learned from the server cert). If no entry matches, write nothing —
  discovery owns entry creation; synthesizing hostname-keyed entries would
  pollute the picker's device-id identity space.

### 2. Connect fast path in `connectWithAutoTLSDiagnostics`

Located after the `WENDY_AGENT_SOCKET` bypass and before `resolveAddrOnce`:

- Applies only when the target host is a NAME (IP-literal targets and the
  unix-socket path are untouched) and the CLI holds at least one client
  cert.
- On a cache hit: one direct mTLS attempt at the entry's `IP:Port` (the
  cache stores the advertised port, which is the mTLS port for provisioned
  devices — skip the fast path when the entry's `MTLS` flag is false).
- **Cert rotation:** order the cert list so the cert whose org matches the
  entry's `OrgID` is tried first (falling back to config order). This also
  removes the N-cert sequential ladder for multi-org users on the fast
  path.
- **Budget split:** the fast-path dial uses a custom dialer with a ~1s TCP
  connect timeout (`lkgTCPConnectTimeout`); once TCP is established, the
  existing `mtlsProbeTimeout` governs the handshake + `GetAgentVersion`
  probe, so a loaded device's slow full handshake is not artificially cut.
  A dead/stale IP therefore costs ≤1s before falling through.
- **Fallback:** on ANY fast-path failure (no cache entry, TCP timeout,
  handshake rejection, probe failure) the function continues into today's
  path unchanged — `resolveAddrOnce` (fresh resolution) + the full
  cert × port ladder. Under `WENDY_TLS_DEBUG`, the fast path logs one line
  stating hit/miss and the failure reason.
- On fast-path success: the §1 write-back refreshes the entry, and the
  connection is returned exactly as a ladder success would be (same
  `AgentConnection` fields, `IsMTLS`, observed-org plumbing).

### 3. Trust model: the cache is a routing hint only

Nothing about verification changes. The fast path presents the same client
cert material, runs the same `BuildServerVerifyConnection` (ML-DSA chain +
org + pins + `OnServerIdentity`), the same post-connect `enforceDevicePin`,
and composes with #1612's session resumption (the resumption ticket cache
is keyed by `address|cert`, so a cached-IP dial hits the same ticket the
previous connect stored). A hijacked or reassigned IP fails server
verification and falls through to fresh resolution — the failure mode is
today's behavior plus ≤1s.

### 4. Expected effect (measured baselines, 2026-08-08)

| Path | Today | With fast path |
| --- | --- | --- |
| `.local` name, warm ticket | ~1.5s (resolution-dominated) | ~150–300ms |
| `.local` name, cold ticket | ~2.2–2.9s | ~1s (direct dial, full handshake) |
| IP literal | ~141ms resumed | unchanged |
| Stale cached IP | n/a | today + ≤1s |

### 5. Testing

- Unit (`discoverycache`): `ByHostname` normalization matrix
  (`Wendy-X.local.` vs `wendy-x`, empty-IP exclusion, most-recent-wins),
  refresh-only write-back (no entry → no write).
- Unit (`commands`): cert rotation by org (match first, stable order
  otherwise, no-match = unchanged order).
- Integration (fake TLS agent on 127.0.0.1, as in the #1612 test
  patterns): (a) fast-path hit connects with zero resolver invocations
  (inject a resolver spy); (b) entry pointing at a dead IP falls back
  within the budget and connects via the ladder; (c) entry with a stale IP
  while the device answers on a new IP self-heals (fallback connects,
  write-back stores the new IP).
- On-device gate: repeat `wendy device info --device <name>.local` on this
  LAN drops from ~1.5s to sub-300ms in the mTLS-attempts phase (after one
  discovery or prior connect).

## Non-goals

- Creating cache entries from the connect path (discovery owns creation).
- Changing the TTL or any picker/discovery behavior from #1616.
- Caching across trust changes — pins and cert verification stay the
  arbiters; no "trusted IP" concept exists.
- QUIC (separate spike, queued after this).
