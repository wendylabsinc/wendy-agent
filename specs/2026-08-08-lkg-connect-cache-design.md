# Last-Known-Good Connect Cache (delta over PR #1616)

**Date:** 2026-08-08
**Status:** Approved (design); revised after base-branch exploration — the
approved outcome and decisions are unchanged, but PR #1616's branch already
implements the resolution half of this feature, so this spec now describes
only the remaining delta.
**Branch:** `ed/lkg-connect-cache` (stacked on `ed/instant-mdns-discovery`,
PR #1616)

## Problem

With TLS session resumption (PR #1612), a repeat mTLS handshake costs
~141ms, but connecting by `.local` name measured ~1.5s on macOS —
resolution-dominated (~1.2–1.4s), plus the sequential cert × port probe
ladder.

**What the base branch (PR #1616) already provides** in
`connectWithAutoTLSDiagnostics` (`helpers.go`):

- `cachedDeviceIP(host)` — skips `resolveAddrOnce` entirely when a
  device-cache entry's hostname matches (via `cachedDeviceEntry`, which is
  **TTL-gated**: `cache.Fresh`, 1h).
- A stale-cache retry: on an unreachable fast-path dial, re-resolve and
  re-run the ladder once.
- `cacheConnectSuccess` write-back (refresh under the existing discovery
  identity; mDNS-shaped-host guards) — but it stores **`originalAddr`'s
  port** (usually the 50051 plaintext port for a stored default device) and
  never sets `MTLS`/`OrgID`, so a successful connect can clobber
  discovery's advertised mTLS port in the cache via Upsert's
  non-zero-wins merge.

**Remaining gaps this PR closes:**

1. **The 1h TTL cliff**: the first connect after any >1h gap pays full
   resolution again — exactly the "morning first command" case.
2. **Dead-IP cost**: the fast path runs the full ladder against the cached
   IP with no TCP bound — an unreachable IP can burn up to ~2×7s mTLS
   probes + 3s plaintext probe before the stale retry.
3. **Ladder order**: the fast path still tries the plaintext port first and
   every cert sequentially in config order; each wrong-cert attempt on the
   mTLS port costs the device a full ML-DSA handshake (~1–2s each for
   multi-org users).
4. **Write-back fidelity**: the port-clobber wart above, and the cache
   never learns `MTLS`/`OrgID` from connects.

## Decisions (made during design review; unchanged)

- **Stacked on PR #1616**, reusing `discoverycache` — one cache; discovery
  pre-warms connects, connects refresh discovery. Merge order: #1616 first.
- **Any-age fast path** for connects: a cached IP is worth one bounded
  attempt regardless of the 1h TTL (which remains the *display*-freshness
  bound for the picker). A stale IP costs ≤1s against a ~1.3s+ saving, and
  the existing stale-cache retry does fresh resolution, so staleness
  self-heals.

## Design (delta)

### 1. Any-age connect lookup

`discoverycache` gains `func (c *Cache) Entries() []Entry` (all entries,
any age). The connect path's `cachedDeviceEntry` switches from
`cache.Fresh(now)` to `cache.Entries()` and, when several entries'
hostnames normalize equal, picks the most recent `LastSeen`. The picker and
discovery keep using `Fresh` — display freshness is unchanged.

### 2. LKG direct dial with cert rotation and a TCP bound

When the matched entry has a non-empty `IP`, `Port > 0`, and `MTLS: true`,
`connectWithAutoTLSDiagnostics` attempts, before its existing fast-path
ladder:

- **TCP pre-check**: `net.DialTimeout("tcp", ip:port, lkgTCPConnectTimeout)`
  with `lkgTCPConnectTimeout = 1s`, via a shared `lkgTCPAlive(addr)` helper
  (see below).
- **Direct dial**: run the existing ladder mechanics against `ip:entry.Port`
  (the advertised mTLS port) with the cert list rotated so certs whose
  `OrganizationID` matches the entry's `OrgID` come first (stable order
  otherwise; unknown/zero `OrgID` = unchanged order). Implemented by
  extracting `dialAgentLadderWithCerts(ctx, addr, certs)` from
  `dialAgentLadder` (which keeps its signature and behavior) plus a pure
  `rotateCertsForOrg(certs, orgID)` helper.
- **Three-state fallback**: `dialAgentLKG` reports which of three outcomes it
  hit (`lkgConnected`, `lkgDeadTCP`, `lkgHandshakeFailed`), and
  `connectWithAutoTLSDiagnostics` branches on it:
  - `lkgConnected` → return the connection as-is.
  - `lkgHandshakeFailed` (TCP answered but the ladder didn't produce a
    usable mTLS connection — ladder error, nil conn, or a surprising
    plaintext downgrade) → the host is proven alive, so this falls through
    to the existing cached-IP ladder + stale-retry flow **verbatim** (one
    redundant handshake attempt against the same mTLS port in that failure
    path is accepted).
  - `lkgDeadTCP` (the pre-check itself failed) → the cached IP never enters
    the cached-IP ladder at all. `fromCache` stays `false`, so the flow
    proceeds straight to fresh resolution against the *original* host:port —
    exactly what the stale-cache retry would have produced, minus the
    wasted ladder attempt against a black hole. This is what actually bounds
    the dead-IP worst case at ~1s instead of many seconds of ladder probes;
    the base's naive "any LKG failure falls into the cached-IP ladder"
    behavior would otherwise still pay the full ladder cost against a dead
    IP before ever re-resolving.
  `WENDY_TLS_DEBUG` logs one line for the LKG attempt: hit/skip and the
  failure reason.
- **LKG-ineligible cache dials get the same bound**: entries that don't
  qualify for the direct dial (`MTLS: false` or `Port == 0`) never call
  `dialAgentLKG`, but their cached-IP fromCache dial is otherwise exactly as
  exposed to a dead IP as the LKG path is. `connectWithAutoTLSDiagnostics`
  therefore runs the same `lkgTCPAlive` pre-check against `ip:plainPort`
  before committing to `fromCache` for these entries too: alive → proceed
  fromCache as before; dead → fall through to fresh resolution. Without this
  bound, any-age lookup (§1) would let a stale LKG-ineligible entry (e.g. an
  unprovisioned device that moved) feed an unbounded ladder attempt — a
  regression versus the base's TTL gate.

### 3. Write-back fidelity

`cacheConnectSuccess` gains the connection's actual endpoint facts,
threaded from its call site (`helpers.go:964`, where the successful
`conn` is in hand): the **actually-connected port** (not `originalAddr`'s),
`MTLS: conn.IsMTLS`, and `OrgID` from `conn.ObservedServerOrg()` when
non-zero. This both feeds §2's direct dial and fixes the base's
port-clobber wart (a connect can no longer overwrite discovery's advertised
mTLS port with the plaintext port).

### 4. Trust model: unchanged (cache is a routing hint only)

The direct dial presents the same client certs, runs the same
`BuildServerVerifyConnection` (ML-DSA chain + org + pins), the same
post-connect `enforceDevicePin`, and composes with #1612's resumption once
both merge (the ticket cache is keyed by `address|cert`, so the cached-IP
dial hits the ticket the previous connect stored). A hijacked or reassigned
IP fails verification and falls through; the failure mode is today's
behavior plus ≤1s.

### 5. Expected effect (measured baselines, 2026-08-08)

| Path | Base (#1616) | With this delta |
| --- | --- | --- |
| `.local`, cache fresh (<1h) | fast (resolution skipped) | same, minus plaintext-port ladder step |
| `.local`, cache stale (>1h) | ~1.5s (full resolution) | ~fast-path cost (any-age) |
| Dead/stale cached IP | up to ~17s of ladder probes before retry | ≤1s pre-check, then straight to fresh resolution (cached-IP ladder never runs against a dead IP) |
| Multi-org, wrong-cert-first | +1–2s per wrong cert (device-side full handshake) | org-matched cert first |
| IP literal / unix socket | unchanged | unchanged |

### 6. Testing

- Unit (`discoverycache`): `Entries()` returns stale + fresh; existing
  `Fresh` untouched.
- Unit (`commands`): most-recent-wins hostname match across duplicate
  hostnames; `rotateCertsForOrg` (match-first, stable, no-match =
  unchanged); write-back stores actual port/MTLS/OrgID and no longer
  clobbers an advertised mTLS port with a plaintext port.
- Integration (fake TLS agent, existing test-seam style —
  `deviceCacheLoadFn`, `cacheFastPathReachableFn`, resolver fns): (a)
  any-age hit connects with zero resolver invocations; (b) a dead-IP LKG
  entry never enters the cached-IP ladder — the pre-check (~1s) sends it
  straight to fresh resolution, and the ladder is spied to assert it only
  ever ran against the freshly-resolved address, never the dead cached IP
  (`lkgDeadTCP`); a live-TCP-but-failed-handshake entry (`lkgHandshakeFailed`)
  still gets the cached-IP ladder + stale retry, unchanged; (c) direct dial
  uses the rotated cert first (spy on cert order); (d) an LKG-ineligible
  entry (`MTLS: false`) gets the same pre-check bound before its fromCache
  dial: dead → fresh resolution, alive → fromCache ladder at the cached IP,
  same as before this delta.
- On-device gate: with a >1h-old cache entry, `wendy device info --device
  <name>.local` skips resolution (fast path) on this LAN; dead-IP fallback
  stays snappy.

## Non-goals

- Creating cache entries from the connect path beyond the base's existing
  behavior (discovery owns creation; the base's synthesized-identity
  fallback for never-discovered hosts is kept as-is).
- Changing the picker/discovery TTL or any #1616 display behavior.
- Caching across trust changes — verification and pins stay the arbiters.
- QUIC (separate spike, queued after this).
