# Instant mDNS Device Discovery — Design

**Date**: 2026-08-07
**Status**: Approved (design review with Ethan, 2026-08-07)

## Problem

Finding Wendy devices on the LAN takes many seconds. mDNS itself is not the
bottleneck — answers typically arrive in 100ms–1s (and daemon caches answer
instantly). The latency is policy in our code:

- **Batch semantics**: `DiscoverLAN` returns nothing until the whole scan
  finishes (3–5s timeouts). One-shot/JSON always pays the full timeout.
- **Sequential resolves** (macOS): after the browse, instances resolve
  one-by-one with a 2s cap each inside the browse callback; one dead instance
  stalls everyone behind it.
- **No streaming off macOS**: `BrowseMDNSServicesContinuous` returns
  `ErrUnsupported` on Linux/Windows, so the picker polls 3s batch scans.
- **Sequential per-interface queries** in the Linux hashicorp/mdns fallback:
  each interface blocks for the full timeout (`mdns_linux.go`).
- Nothing is remembered between invocations, so every command starts from
  zero even for a device we talked to seconds ago.

## Goal

Devices the CLI has seen recently appear **instantly** (<100ms, from a
persisted cache) in every discovery surface, verified in the background; new
devices stream in as mDNS answers arrive (typically <1s) instead of after a
batch timeout. All platforms. No child processes, no background daemon.

## Decisions made in review

- Cache **is** the point: cached rows render immediately with a "verifying"
  state (target <100ms), live scan confirms/updates in the background.
- All surfaces: discover TUI, device picker, one-shot/`--json`, MCP
  `device_list`, and default-device resolution (`resolveAddrOnce`).
- A cached device that does not respond flips to an explicit **offline**
  marker but stays listed and selectable.
- `--json`/MCP output stays **confirmed-only** (no cached-unconfirmed
  entries); its speedup is streaming + early exit.
- Cache **browse-only devices too** (not just probed ones); TTL is **1 hour**
  — the cache is short-term "recently seen" memory.
- Linux streaming uses **Avahi over D-Bus** (godbus is already a dependency),
  not a long-running `avahi-browse` child — orphan processes must be
  structurally impossible (cf. WDY-1831 on macOS).

## Design

### 1. Device cache (`discoverycache` package)

**Storage**: `~/.wendy/devices.json`, next to `config.json` (same
`config.ConfigDir()`). Derived state — safe to delete at any time. Not merged
into `config.json`, which holds user intent.

**Schema** (version 1):

```json
{
  "version": 1,
  "devices": [
    {
      "id": "<wendyosdevice TXT value, falling back to display name>",
      "displayName": "orin-nano",
      "hostname": "orin-nano.local",
      "ip": "192.168.1.42",
      "port": 50051,
      "mtls": true,
      "assetId": 3,
      "orgId": 7,
      "interfaceName": "en0",
      "agentVersion": "0.19.1",
      "os": "WendyOS",
      "osVersion": "0.19",
      "lastSeen": "2026-08-07T09:00:00Z"
    }
  ]
}
```

Entries are keyed by the same identity the picker dedups on (TXT device ID,
falling back to display name) so cached rows and their live confirmations
merge instead of duplicating.

**Write points**:
- every successful mDNS browse+resolve (browse-only devices included),
- every successful agent probe (adds/refreshes `agentVersion`/`os`),
- every successful direct connect in `resolveAddrOnce`-driven commands, so
  plain `wendy run` keeps the cache warm without a discovery UI.

**Concurrency**: load whole file, merge, write temp file + atomic `rename`.
Last-writer-wins; a lost write is re-learned on the next scan. No locking.

**Eviction**: entries with `lastSeen` older than **1 hour** are dropped on
save and ignored on load.

**Trust boundary**: cached `agentVersion`/`os` render greyed/"verifying"
until a live probe confirms; update hints (`agentBehindCLI`) never fire from
cached metadata alone.

### 2. Streaming discovery core

New primary primitive in `discovery`:

```go
type LANEventKind int // Cached | Found | Updated | Offline

type LANEvent struct {
    Kind   LANEventKind
    Device models.LANDevice
}

type StreamOptions struct { /* room for filters; empty at first */ }

// StreamLAN emits fresh cache entries immediately, then live mDNS results
// and probe outcomes as they arrive, until ctx is cancelled.
func StreamLAN(ctx context.Context, opts StreamOptions) <-chan LANEvent
```

- `Cached`: emitted synchronously at startup for each cache entry ≤1h old.
- `Found`: live-confirmed device (new, or cached entry confirmed by probe or
  mDNS resolve).
- `Updated`: an already-emitted device changed (IP/port/TXT moved, or probe
  filled in version/OS).
- `Offline`: a cached device failed verification (see §3).

**Platform backends** (all gain true streaming, all in-process):

- **macOS** (`dnssd_darwin.go`): keep `dnssdBrowseStream`; move resolves out
  of the browse callback into a bounded worker pool (4) so a slow resolve no
  longer serializes the rest. Resolve timeout 2s → 1s (live daemons answer in
  ms; the timeout only ever pays for dead instances).
- **Linux**: talk to `avahi-daemon` over D-Bus (`godbus/dbus/v5`, already a
  direct dependency):
  `org.freedesktop.Avahi.Server.ServiceBrowserNew("_wendyos._udp")` →
  `ItemNew`/`ItemRemove` signals; each `ItemNew` → `ResolveService` for
  hostname/IP/port/TXT. Avahi's daemon cache answers immediately on
  subscription. The D-Bus socket dies with our process, so orphaned browsers
  are structurally impossible. `ItemRemove` (goodbye packets) is decoded — so
  a malformed body cannot panic downstream — and then ignored: removals are
  owned by the engine's own probe/grace logic (§3), which is the only path
  that behaves identically on every platform.
- **Linux fallback / Windows**: hashicorp/mdns re-query loop with
  per-interface queries **in parallel** (fixes the sequential-interface bug
  in `browseMDNSHashicorp`) and entries forwarded off the entries channel as
  they arrive rather than after `Query` returns.

**Batch derived from stream**: `DiscoverLAN` (used by `fleet lan`, `tour`,
agent mesh, one-shot/JSON) is reimplemented on top of `StreamLAN`: collect
`Found`/`Updated`, return at settle (500ms with no new `Found`, after all
cached-device probes conclude) or the timeout cap, whichever is first.
`DiscoverLANContinuous` call sites migrate to `StreamLAN`; the old function
and the `avahi-browse` shell-out are then deleted.

### 3. Verification layer

Owned by `StreamLAN` (surfaces only render events — removes the duplicated
probe/retry choreography in `pickDevice` and the discover TUI).

For each fresh cache entry, two confirmations race:

1. **Direct probe**: `resolveLANVersion` (existing gRPC probe) against the
   cached `ip:port`, ~1.5s dial timeout. A live device answers in
   ~50–200ms — usually before mDNS says anything. Success → `Found` + cache
   upsert. mTLS vs plaintext follows the cached `mtls` flag.
2. **mDNS confirmation**: the live browse resolves the same identity →
   `Updated` if the address moved (probe retargets to the new address).

**Offline**: emitted only when the direct probe failed **and** a ~4s grace
window passed without mDNS seeing the device. The row stays listed and
selectable with an offline marker; a later mDNS appearance flips it back via
`Found`. One probe retry cycle runs 30s after `Offline` (devices mid-boot);
no further polling.

Probes run through a bounded worker pool (4 concurrent).

New (uncached) devices: browse → resolve → `Found` with pending-probe state →
probe fills version/OS via `Updated`.

### 4. Surface integration

- **Picker (`pickDevice`)**: replace `DiscoverLANContinuous` + hand-rolled
  probe goroutines with one `StreamLAN` consumer. `Cached` → row with
  "verifying" spinner (existing `ProbePending`), `Found`/`Updated` → in-place
  row update (existing `PickerAddMsg` dedup-merge), `Offline` → new offline
  `ProbeState` rendering. Warm cache ⇒ picker fully populated on first frame.
- **`wendy discover` TUI**: same consumer; replaces the 3s `scanLAN` poll
  loop and `lanScanMsg` batching. USB/Ethernet/BLE/External cadences
  unchanged — LAN is the only transport changing.
- **One-shot/`--json`/MCP `device_list`/`fleet lan`**: the stream-derived
  batch above. Confirmed-only output, early exit at settle; typical run drops
  from fixed 5s to ~1–2s. No MCP schema change.
- **`resolveAddrOnce`**: before any mDNS browse, a fresh cache hit for the
  target name returns its IP immediately; the connect attempt itself is the
  verification. Dial failure falls through to today's mDNS path.

### 5. Error handling

- Corrupt/unreadable/wrong-version cache file → treated as empty, replaced on
  next save; never fails a scan.
- Avahi D-Bus unavailable → hashicorp streaming fallback;
  `WENDY_MDNS_DEBUG=1` reports which backend ran.
- Backend dies mid-stream (mDNSResponder restart, D-Bus disconnect) →
  already-emitted rows stay; `StreamLAN` reconnects with backoff (2s ×3)
  before closing the channel (existing "log + list stops growing" behavior
  as the last resort).
- Probe failures never remove rows; they only gate the offline marker.

### 6. Testing

- `discoverycache`: table-driven tests for 1h TTL eviction, atomic save,
  corrupt-file recovery, identity merge.
- Stream lifecycle: fake backend + fake prober → assert Cached→Found /
  Cached→Offline transitions, 4s grace, dedup across cache+live, settle-based
  batch termination.
- macOS: extend the `dnssdRegister` integration test to parallel resolves and
  the streaming path.
- Linux: D-Bus interface fake (pattern established by the BlueZ work).
- Existing `parseAvahi*`/`parseTXTRecord` tests stand until the batch
  shell-out is deleted.

## Out of scope (YAGNI)

- No background daemon or long-lived helper processes.
- No unicast QU mDNS implementation (the direct gRPC probe fills that role).
- No agent-side advertising changes.
- No BLE/USB/Ethernet discovery changes.
