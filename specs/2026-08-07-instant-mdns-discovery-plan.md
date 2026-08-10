# Instant mDNS Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Devices seen in the last hour render instantly (<100ms) from a persisted cache in every discovery surface, while a streaming mDNS scan confirms/updates them and finds new devices in <1s on all platforms.

**Architecture:** A new `discoverycache` package persists recently-seen LAN devices to `~/.wendy/devices.json` (1h TTL). A new platform-independent stream engine (`discovery.StreamLAN`) emits `Cached/Found/Updated/Offline` events, fed by per-platform in-process backends (`mdnsStreamBackend`: dns_sd on macOS, Avahi-over-D-Bus on Linux with a hashicorp/mdns fallback, hashicorp/mdns on Windows) and an injected gRPC prober. Batch discovery (`DiscoverLAN`) is reimplemented on the stream with settle-based early exit. Surfaces (picker, discover TUI, one-shot/JSON, MCP, `resolveAddrOnce`) consume the stream or the cached collector.

**Tech Stack:** Go (module root = repo root, packages under `go/`), cgo `<dns_sd.h>` (existing, darwin), `github.com/godbus/dbus/v5` (existing dep), `github.com/hashicorp/mdns` (existing dep), Bubble Tea TUI (existing).

**Spec:** `specs/2026-08-07-instant-mdns-discovery-design.md` — read it first.

## Global Constraints

- **No child processes** anywhere in discovery — no `avahi-browse`, no `dns-sd` (spec: "orphans structurally impossible", cf. WDY-1831).
- Cache TTL is exactly **1 hour** (`discoverycache.TTL = time.Hour`).
- `--json` / MCP `device_list` output is **confirmed-only**: never emit a cache entry that wasn't confirmed by mDNS resolve or agent probe this run.
- Offline rows **stay listed and selectable** in interactive surfaces.
- All new timing knobs are unexported package `var`s so tests can shrink them.
- Branch: `ed/instant-mdns-discovery` (branched from `ed/instant-mdns-discovery-spec` so the spec is in-tree; that branch is 2 commits ahead of `origin/main`).
- Run tests from repo root: `go test ./go/internal/...`. Before every push: `gofmt -l .` from repo root must print nothing (`gofmt -w` what it lists).
- Cross-compile check after any discovery change: `GOOS=linux go build ./go/internal/shared/discovery/ && GOOS=windows go build ./go/internal/shared/discovery/` (both are CGO-free paths).
- `WENDY_MDNS_DEBUG=1` must report which backend a stream session used (existing `logMDNSQueryErr` pattern in `go/internal/shared/discovery/discovery.go:19`).

---

### Task 1: `discoverycache` package

**Files:**
- Create: `go/internal/shared/discoverycache/cache.go`
- Test: `go/internal/shared/discoverycache/cache_test.go`

**Interfaces:**
- Consumes: `models.LANDevice` (`go/internal/shared/models/devices.go:52`), `config.ConfigDir()` (`go/internal/shared/config/config.go:101`).
- Produces (later tasks call these exact names):

```go
package discoverycache

const TTL = time.Hour

type Entry struct {
    ID              string    `json:"id"`
    DisplayName     string    `json:"displayName"`
    Hostname        string    `json:"hostname"`
    IP              string    `json:"ip,omitempty"`
    Port            int       `json:"port"`
    MTLS            bool      `json:"mtls,omitempty"`
    AssetID         int32     `json:"assetId,omitempty"`
    OrgID           int32     `json:"orgId,omitempty"`
    MeshName        string    `json:"meshName,omitempty"`
    InterfaceName   string    `json:"interfaceName,omitempty"`
    AgentVersion    string    `json:"agentVersion,omitempty"`
    DeviceType      string    `json:"deviceType,omitempty"`
    OS              string    `json:"os,omitempty"`
    OSVersion       string    `json:"osVersion,omitempty"`
    CPUArchitecture string    `json:"cpuArchitecture,omitempty"`
    LastSeen        time.Time `json:"lastSeen"`
}

// Key is the cache identity: lowercased id, falling back to lowercased
// displayName. Must match the picker's dedup identity (TXT device id,
// fallback display name).
func Key(id, displayName string) string

type Cache struct { /* path string; mu sync.Mutex; entries map[string]Entry; dirty map[string]bool */ }

// Load opens ~/.wendy/devices.json. Missing, corrupt, or wrong-version files
// yield an empty cache and a nil error — the cache never fails a scan.
func Load() (*Cache, error)
func LoadFrom(path string) (*Cache, error)

// Fresh returns entries with LastSeen within TTL of now, any order.
func (c *Cache) Fresh(now time.Time) []Entry

// Upsert merges e into the cache under Key(e.ID, e.DisplayName) and stamps
// LastSeen=now. Merge rule: a non-zero incoming field replaces the stored
// one; a zero incoming field keeps the stored value (so a browse-only upsert
// never wipes a probed AgentVersion).
func (c *Cache) Upsert(e Entry, now time.Time)

// Flush persists: re-reads the file, overlays this cache's dirty entries,
// drops entries older than TTL, writes temp file + atomic os.Rename.
// Concurrent CLIs: last writer wins, lost writes are re-learned next scan.
func (c *Cache) Flush(now time.Time) error

func EntryFromDevice(dev models.LANDevice) Entry
// Device converts back; sets InterfaceType=string(models.InterfaceLAN),
// IsWendyDevice=true, NetworkInterface=e.InterfaceName, IsMTLS=e.MTLS.
func (e Entry) Device() models.LANDevice
```

File schema on disk: `{"version":1,"devices":[Entry...]}`. Reject `version != 1` as corrupt (treat as empty).

- [ ] **Step 1: Write failing tests**

```go
package discoverycache

import (
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestKeyFallback(t *testing.T) {
    if Key("Dev-ID", "orin") != "dev-id" {
        t.Fatalf("id should win, lowercased")
    }
    if Key("", "Orin-Nano") != "orin-nano" {
        t.Fatalf("displayName fallback, lowercased")
    }
}

func TestUpsertMergeAndTTL(t *testing.T) {
    dir := t.TempDir()
    c, err := LoadFrom(filepath.Join(dir, "devices.json"))
    if err != nil {
        t.Fatal(err)
    }
    now := time.Now()
    c.Upsert(Entry{ID: "a", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.5", Port: 50051, AgentVersion: "0.19.1"}, now.Add(-2*time.Hour))
    c.Upsert(Entry{ID: "a", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.9", Port: 50051}, now) // browse-only: no version
    fresh := c.Fresh(now)
    if len(fresh) != 1 {
        t.Fatalf("want 1 fresh entry, got %d", len(fresh))
    }
    if fresh[0].IP != "10.0.0.9" || fresh[0].AgentVersion != "0.19.1" {
        t.Fatalf("merge broke: %+v (new IP must win, old version must survive)", fresh[0])
    }

    // stale entries are not fresh
    c.Upsert(Entry{ID: "b", DisplayName: "old", Hostname: "old.local", Port: 50051}, now.Add(-61*time.Minute))
    if got := len(c.Fresh(now)); got != 1 {
        t.Fatalf("61-minute-old entry must not be fresh, got %d entries", got)
    }
}

func TestFlushRoundTripAndEviction(t *testing.T) {
    path := filepath.Join(t.TempDir(), "devices.json")
    c, _ := LoadFrom(path)
    now := time.Now()
    c.Upsert(Entry{ID: "a", DisplayName: "orin", Hostname: "orin.local", Port: 50051}, now)
    c.Upsert(Entry{ID: "b", DisplayName: "stale", Hostname: "stale.local", Port: 50051}, now.Add(-2*time.Hour))
    if err := c.Flush(now); err != nil {
        t.Fatal(err)
    }
    c2, _ := LoadFrom(path)
    if got := len(c2.Fresh(now)); got != 1 {
        t.Fatalf("stale entry must be evicted on flush, got %d", got)
    }
}

func TestFlushMergesConcurrentWriter(t *testing.T) {
    path := filepath.Join(t.TempDir(), "devices.json")
    now := time.Now()
    other, _ := LoadFrom(path)
    other.Upsert(Entry{ID: "other", DisplayName: "other", Hostname: "other.local", Port: 50051}, now)
    if err := other.Flush(now); err != nil {
        t.Fatal(err)
    }
    // c was loaded before other's flush (empty file), upserts one entry;
    // Flush must re-read and keep other's entry too.
    c, _ := LoadFrom(path) // loaded fresh here for simplicity; the re-read is what's under test
    c.Upsert(Entry{ID: "mine", DisplayName: "mine", Hostname: "mine.local", Port: 50051}, now)
    if err := c.Flush(now); err != nil {
        t.Fatal(err)
    }
    c2, _ := LoadFrom(path)
    if got := len(c2.Fresh(now)); got != 2 {
        t.Fatalf("flush must merge with on-disk entries, got %d", got)
    }
}

func TestCorruptFileIsEmptyCache(t *testing.T) {
    path := filepath.Join(t.TempDir(), "devices.json")
    if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
        t.Fatal(err)
    }
    c, err := LoadFrom(path)
    if err != nil || len(c.Fresh(time.Now())) != 0 {
        t.Fatalf("corrupt file must load as empty cache with nil error, err=%v", err)
    }
    // and Flush must replace it
    c.Upsert(Entry{ID: "a", DisplayName: "a", Hostname: "a.local", Port: 50051}, time.Now())
    if err := c.Flush(time.Now()); err != nil {
        t.Fatal(err)
    }
}

func TestEntryDeviceRoundTrip(t *testing.T) {
    dev := EntryFromDevice(modelsLANFixture()).Device()
    if dev.ID != "dev-1" || dev.IPAddress != "10.0.0.5" || !dev.IsMTLS || dev.OrgID != 7 ||
        dev.AssetID != 3 || dev.InterfaceType != "lan" || !dev.IsWendyDevice || dev.NetworkInterface != "en0" {
        t.Fatalf("round trip lost fields: %+v", dev)
    }
}
```

Add the fixture helper in the test file (real values for every Entry field: `ID: "dev-1"`, `IPAddress: "10.0.0.5"`, `IsMTLS: true`, `OrgID: 7`, `AssetID: 3`, `NetworkInterface: "en0"`, etc.). Check `string(models.InterfaceLAN)` — it is `"lan"` if the test above fails, fix the literal to match the constant, not the code.

- [ ] **Step 2: Run tests, verify they fail to compile** — `go test ./go/internal/shared/discoverycache/` → "undefined: Key" etc.
- [ ] **Step 3: Implement `cache.go`** per the interface block above. `Load()` = `LoadFrom(filepath.Join(dir, "devices.json"))` with `dir` from `config.ConfigDir()`. Atomic write: `os.CreateTemp(filepath.Dir(path), ".devices-*.json")` → write → `Close` → `os.Rename`. Guard all entry access with the mutex.
- [ ] **Step 4: Run tests, verify pass** — `go test ./go/internal/shared/discoverycache/ -v`
- [ ] **Step 5: gofmt + commit** — `git add go/internal/shared/discoverycache && git commit -m "feat: discoverycache package for recently-seen LAN devices"`

---

### Task 2: Stream types + MDNSService→LANDevice mapper

**Files:**
- Modify: `go/internal/shared/discovery/mdns.go` (add `InterfaceName` to `MDNSService`, add mapper)
- Create: `go/internal/shared/discovery/stream.go` (types only in this task)
- Test: `go/internal/shared/discovery/stream_types_test.go`

**Interfaces:**
- Produces:

```go
// mdns.go — add field:
type MDNSService struct {
    InstanceName  string
    Hostname      string
    IPAddress     string
    Port          int
    TXTRecords    map[string]string
    InterfaceName string // OS interface the answer arrived on ("" if unknown)
}

// mdns.go — the single place TXT records become a LANDevice. Mirrors the
// per-platform logic being replaced (darwin discovery_darwin.go:69-101,
// linux discovery_linux.go:146-174): displayname/id/wendyosdevice/tls/
// assetid/orgid/name TXT keys, NetworkInterface from InterfaceName.
func lanDeviceFromService(svc MDNSService) models.LANDevice

// stream.go:
type LANEventKind int

const (
    LANCached  LANEventKind = iota // cache entry, not yet verified this run
    LANFound                       // live-confirmed (mDNS resolve or probe)
    LANUpdated                     // an already-emitted device changed
    LANOffline                     // cached entry failed verification
)

type LANEvent struct {
    Kind   LANEventKind
    Device models.LANDevice
    // Probed: Device's AgentVersion/OS/IsMTLS were confirmed by a live agent
    // probe (not just mDNS TXT records).
    Probed bool
}

// LANProber verifies a device by talking to its agent. On success the
// returned device carries refreshed AgentVersion/DeviceType/OS/OSVersion/
// CPUArchitecture and IsMTLS reflecting the actual connection.
type LANProber func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error)

type StreamOptions struct {
    UseCache bool      // emit cached entries and persist discoveries
    Prober   LANProber // nil = no probing (mDNS-only confirmation)
}
```

- [ ] **Step 1: Write failing test** for the mapper (table-driven; this pins TXT semantics for every backend):

```go
func TestLANDeviceFromService(t *testing.T) {
    svc := MDNSService{
        InstanceName:  "orin",
        Hostname:      "orin.local",
        IPAddress:     "10.0.0.5",
        Port:          50051,
        InterfaceName: "en0",
        TXTRecords: map[string]string{
            "displayname":   "Orin Nano",
            "wendyosdevice": "dev-1",
            "tls":           "true",
            "assetid":       "3",
            "orgid":         "7",
            "name":          "brave-dolphin",
        },
    }
    dev := lanDeviceFromService(svc)
    if dev.ID != "dev-1" || dev.DisplayName != "Orin Nano" || !dev.IsMTLS ||
        dev.AssetID != 3 || dev.OrgID != 7 || dev.MeshName != "brave-dolphin" ||
        dev.Hostname != "orin.local" || dev.IPAddress != "10.0.0.5" || dev.Port != 50051 ||
        !dev.IsWendyDevice || dev.InterfaceType != string(models.InterfaceLAN) {
        t.Fatalf("mapper: %+v", dev)
    }

    // fallbacks: no TXT id → "id" key → display name; no displayname → hostname sans .local
    bare := lanDeviceFromService(MDNSService{InstanceName: "x", Hostname: "orin.local", Port: 50051, TXTRecords: map[string]string{}})
    if bare.DisplayName != "orin" || bare.ID != "orin" {
        t.Fatalf("fallbacks: %+v", bare)
    }
    // assetid/orgid: 0 or unparseable stays 0 (matches setAssetID at discovery_darwin.go:107)
    z := lanDeviceFromService(MDNSService{Hostname: "a.local", Port: 1, TXTRecords: map[string]string{"assetid": "0", "orgid": "junk"}})
    if z.AssetID != 0 || z.OrgID != 0 {
        t.Fatalf("zero/invalid ids must stay 0: %+v", z)
    }
}
```

- [ ] **Step 2: Run** `go test ./go/internal/shared/discovery/ -run TestLANDeviceFromService` → FAIL (undefined).
- [ ] **Step 3: Implement** the field, the mapper (copy the exact TXT precedence from `discovery_darwin.go:75-99`: `displayname` else hostname-sans-`.local`; id = `wendyosdevice` else `id` else displayName; positive-only `assetid`/`orgid` parse; `name` → MeshName), and the stream.go type declarations. NetworkInterface: set `dev.NetworkInterface = svc.InterfaceName` directly here; platform display-name prettification is layered on later (Task 5).
- [ ] **Step 4: Run** the test → PASS. Also `go build ./go/...` (all platforms' shared code still compiles).
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: LAN stream event types + shared mDNS service mapper"`

---

### Task 3: Stream engine (the core)

**Files:**
- Modify: `go/internal/shared/discovery/stream.go`
- Test: `go/internal/shared/discovery/stream_test.go`

**Interfaces:**
- Consumes: Task 1's `discoverycache` API, Task 2's types.
- Produces:

```go
// StreamLAN emits fresh cache entries immediately (when opts.UseCache), then
// live results and probe outcomes until ctx is cancelled. The channel closes
// when the session ends.
func StreamLAN(ctx context.Context, opts StreamOptions) <-chan LANEvent

// Package seams (stream.go):
var (
    lanBackendFn      = mdnsStreamBackend            // per-platform, Tasks 5-7
    cacheLoadFn       = discoverycache.Load          // tests: LoadFrom(tempdir)
    offlineGrace      = 4 * time.Second              // cached & silent → Offline
    offlineRetryDelay = 30 * time.Second             // one re-probe after Offline
    probeTimeout      = 1500 * time.Millisecond      // per-probe ctx budget
    probeWorkers      = 4                            // concurrent probes/resolves
    cacheFlushDelay   = time.Second                  // debounce for cache writes
    backendRetryDelay = 2 * time.Second              // backend died mid-session
    backendRetries    = 3                            // ...restart attempts before giving up
)

// runLANStream drives one session. probesDone (may be nil) is closed once
// every cached entry's initial probe has concluded — Task 4's settle gate.
func runLANStream(ctx context.Context, opts StreamOptions, out chan<- LANEvent, probesDone chan struct{})
```

Until Task 5 lands, declare a temporary `mdnsStreamBackend` stub in stream.go so darwin builds (`func mdnsStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error { <-ctx.Done(); return nil }`) — it is replaced per-platform in Tasks 5–7. Tests always override `lanBackendFn`.

**Engine behavior (this is the contract the tests below pin):**
1. On start with `UseCache`: load cache, emit `LANCached` for each `Fresh` entry (as `entry.Device()`), and schedule a probe for each (if `Prober != nil`).
2. Backend emissions (`lanDeviceFromService`) are deduped by `discoverycache.Key(dev.ID, dev.DisplayName)`. First sighting of an uncached key → `LANFound` (Probed=false) + probe scheduled. Sighting of a cached-but-unconfirmed key → `LANFound` (mDNS confirmation) + upsert. Sighting that changes IP/Port/Hostname/TXT of an already-emitted key → `LANUpdated` + retarget any pending probe result to the new address (schedule a fresh probe if the previous one failed).
3. Probe success → `LANFound` (first confirmation) or `LANUpdated` (already confirmed), with `Probed: true`, + cache upsert with version fields.
4. A cached entry goes `LANOffline` only when its probe has failed AND `offlineGrace` has elapsed since session start without an mDNS sighting. After `offlineRetryDelay`, one re-probe; success flips it back via `LANFound`.
5. Every successful resolve/probe upserts the cache; `Flush` runs at most once per `cacheFlushDelay` and once at session end (only when `UseCache`).
6. All state lives on one goroutine (select loop over backend channel, probe results channel, timers, ctx). Probes run in a `probeWorkers`-cap semaphore pool.
7. If the backend function returns a non-nil error while ctx is still live (mDNSResponder restart, D-Bus disconnect), restart it after `backendRetryDelay`, up to `backendRetries` times per session; already-emitted state is kept. After the last retry fails, log (`log.Printf("discovery: LAN stream backend stopped: %v", err)`, matching `discovery_darwin.go:209`) and keep serving probe/timer events until ctx ends — spec §5.

- [ ] **Step 1: Write failing tests** (all with `lanBackendFn`/`cacheLoadFn` overridden and timing vars shrunk via `t.Cleanup` restore; helper `collectEvents(ch, n, timeout)` reads n events or fails):

```go
// Fake backend: emits scripted services on demand.
type fakeBackend struct{ ch chan MDNSService }

func (f *fakeBackend) fn(ctx context.Context, serviceType string, emit func(MDNSService)) error {
    for {
        select {
        case <-ctx.Done():
            return nil
        case svc := <-f.ch:
            emit(svc)
        }
    }
}

// Fully written — the pattern every other test here follows.
func TestStreamCachedThenProbeConfirms(t *testing.T) {
    path := filepath.Join(t.TempDir(), "devices.json")
    seed, _ := discoverycache.LoadFrom(path)
    seed.Upsert(discoverycache.Entry{ID: "dev-1", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.5", Port: 50051}, time.Now())
    if err := seed.Flush(time.Now()); err != nil {
        t.Fatal(err)
    }

    fb := &fakeBackend{ch: make(chan MDNSService)} // stays silent
    restore := func(origBackend func(context.Context, string, func(MDNSService)) error, origLoad func() (*discoverycache.Cache, error)) {
        lanBackendFn, cacheLoadFn = origBackend, origLoad
    }
    defer restore(lanBackendFn, cacheLoadFn)
    lanBackendFn = fb.fn
    cacheLoadFn = func() (*discoverycache.Cache, error) { return discoverycache.LoadFrom(path) }

    prober := func(_ context.Context, dev models.LANDevice) (models.LANDevice, error) {
        dev.AgentVersion = "9.9.9"
        dev.IsMTLS = true
        return dev, nil
    }

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    events := StreamLAN(ctx, StreamOptions{UseCache: true, Prober: prober})

    first := <-events
    if first.Kind != LANCached || first.Device.ID != "dev-1" || first.Probed {
        t.Fatalf("first event must be the cached row: %+v", first)
    }
    second := <-events
    if second.Kind != LANFound || !second.Probed || second.Device.AgentVersion != "9.9.9" || !second.Device.IsMTLS {
        t.Fatalf("second event must be the probe confirmation: %+v", second)
    }
}

func TestStreamCachedProbeFailsThenOffline(t *testing.T) {
    // offlineGrace = 50ms for the test. prober: always errors. backend: silent.
    // expect: LANCached, then LANOffline for the same key. No LANFound ever.
}

func TestStreamOfflineDeviceReturnsViaMDNS(t *testing.T) {
    // as above, but after LANOffline the fake backend emits the device.
    // expect: LANFound (mDNS confirmation) after the offline event.
}

func TestStreamNewDeviceFoundThenProbedUpdate(t *testing.T) {
    // empty cache. backend emits a service. prober succeeds.
    // expect: LANFound{Probed:false}, then LANUpdated{Probed:true}.
}

func TestStreamDedupAndAddressChange(t *testing.T) {
    // backend emits same instance twice with same fields → exactly one LANFound.
    // then emits it with a new IP → LANUpdated with the new IP.
}

func TestStreamNoCacheNoCachedEvents(t *testing.T) {
    // UseCache:false with a populated cache file → no LANCached events,
    // and the cache file is not modified (check mtime/content).
}

func TestStreamConfirmedOnlyUpsertsPersist(t *testing.T) {
    // UseCache:true, backend emits one device, ctx cancelled after events drain.
    // Re-load cache file: entry exists with LastSeen ~now (browse-only upsert).
}

func TestStreamBackendRestarts(t *testing.T) {
    // backendRetryDelay = 10ms for the test. Backend fn: first call emits one
    // service then returns an error; second call emits a second service and
    // blocks until ctx. Expect LANFound for BOTH services (state survives the
    // restart), and the backend fn invoked exactly twice.
}
```

Write these as real tests, not comments — each spawns `StreamLAN`, drives the fake backend/prober, asserts exact event sequences per key (use a map key → []LANEventKind collected until quiesce; assert order per key, not global interleave).

- [ ] **Step 2: Run** `go test ./go/internal/shared/discovery/ -run TestStream` → FAIL (undefined StreamLAN).
- [ ] **Step 3: Implement the engine** per the behavior contract. Keep it in one select loop; no mutexes on the state map.
- [ ] **Step 4: Run** stream tests until green, then the whole package: `go test ./go/internal/shared/discovery/`. Run with `-race`.
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: StreamLAN engine — cached-instant + probe-verified LAN discovery"`

---

### Task 4: `CollectLAN` batch on the stream

**Files:**
- Modify: `go/internal/shared/discovery/stream.go` (add CollectLAN)
- Test: `go/internal/shared/discovery/stream_test.go` (extend)

**Interfaces:**
- Produces:

```go
// stream.go:
var collectSettle = 500 * time.Millisecond

// CollectLAN gathers confirmed devices (LANFound/LANUpdated only — never
// unconfirmed cache entries) and returns when: (a) all cached probes have
// concluded AND collectSettle has passed with no new confirmation, or
// (b) timeout elapses. Devices merge by cache key (later events win).
func CollectLAN(ctx context.Context, opts StreamOptions, timeout time.Duration) ([]models.LANDevice, error)
```

CollectLAN uses `runLANStream` directly with a non-nil `probesDone` channel (that's why it lives in the package).

**Sequencing note:** `DiscoverLAN` and `Discover` do NOT switch onto CollectLAN in this task — the real platform backends land in Tasks 5–7, and until then `mdnsStreamBackend` is a blocking stub. The cutover happens in Task 8, keeping the CLI fully functional at every commit on the branch. In this task CollectLAN is exercised only by tests with a fake backend.

- [ ] **Step 1: Write failing tests:**

```go
func TestCollectLANSettlesEarly(t *testing.T) {
    // empty cache, backend emits 2 devices immediately, collectSettle=50ms,
    // timeout=5s. Assert: returns 2 devices in well under 1s (time the call).
}

func TestCollectLANConfirmedOnly(t *testing.T) {
    // cache has a fresh entry; prober always fails; backend silent;
    // timeout=200ms. Assert: returns zero devices — cached-unconfirmed never
    // leaks into batch output.
}

func TestCollectLANWaitsForCachedProbes(t *testing.T) {
    // cache has one entry; prober succeeds after 80ms; backend silent;
    // collectSettle=20ms; timeout=5s. Assert: result includes the probed
    // device (settle must not fire before cached probes conclude).
}
```

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run package tests + `-race` → PASS.**
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: settle-based CollectLAN batch on the LAN stream"`

---

### Task 5: macOS backend (parallel resolves, in-process dns_sd)

**Files:**
- Modify: `go/internal/shared/discovery/mdns_darwin.go` (implement `mdnsStreamBackend`, delete stub from stream.go), `go/internal/shared/discovery/dnssd_darwin.go` (resolve timeout var)
- Test: `go/internal/shared/discovery/mdns_darwin_test.go` (extend the existing `dnssdRegister`-based tests)

**Interfaces:**
- Consumes: `dnssdBrowseStream` (`dnssd_darwin.go:175`), `dnssdResolveInstance` (`dnssd_darwin.go:287`), `preferIPv4Addr` (`mdns.go:57`).
- Produces: `func mdnsStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error` — darwin implementation. Package var `dnssdResolveTimeout = 1 * time.Second` (was a 2s literal).

Behavior: `dnssdBrowseStream` callback pushes `browseResult`s into a channel; `probeWorkers` resolver goroutines pull, call `dnssdResolveInstance` with `dnssdResolveTimeout`, look up the IP via `net.DefaultResolver.LookupHost` + `preferIPv4Addr` (as `resolveMDNSService` at `mdns_darwin.go:94` does today), fill `MDNSService.InterfaceName` from the browse result, and call `emit`. Resolve failures emit a hostname-less `MDNSService{InstanceName, InterfaceName}` only if the instance name is a valid hostname label (mirroring `deviceFromBrowse` at `discovery_darwin.go:139`) — otherwise skip. The browse callback must never block on a resolve (that's the current bug — resolves happen inside the callback at `mdns_darwin.go:67-88`).

Darwin interface prettification: after mapping, `lanDeviceFromService` output gets `NetworkInterface`/`USB` refined in the engine via a per-platform hook. Add to stream.go:

```go
// newLANAnnotator returns a func applied to each device before it is
// emitted; platform files override the default no-op.
var newLANAnnotator = func(ctx context.Context) func(*models.LANDevice) { return func(*models.LANDevice) {} }
```

In `discovery_darwin.go`, set it in an `init()`: build `darwinInterfaceDisplayNameMap(ctx)` + linkSpeeds map once per session, then per device call `setLANNetworkInterface(dev, dev.NetworkInterface, displayNames[...], darwinCachedInterfaceLinkSpeed(...))` — the exact calls `discoverLAN` makes at `discovery_darwin.go:34-51` today. Linux equivalent in Task 6/7 (`setLANNetworkInterface(dev, iface, "", linuxInterfaceLinkSpeed(iface))`, see `discovery_linux.go:146`).

- [ ] **Step 1: Write failing test** — extend the existing register-then-browse integration test: register 3 instances via `dnssdRegister` (`dnssd_darwin.go:254`), run `mdnsStreamBackend` with a 5s ctx, assert all 3 emit with TXT + port intact and that emissions arrive without waiting for the ctx to end (record arrival times; all < 3s). Skip in CI if the existing dnssd tests already carry a short-circuit guard (mirror their pattern).
- [ ] **Step 2: Run** `go test ./go/internal/shared/discovery/ -run TestMDNSStreamBackend -v` → FAIL.
- [ ] **Step 3: Implement**; delete the stub `mdnsStreamBackend` from stream.go (it moves to per-platform files; give linux/windows their own temporary stubs in `mdns_linux.go`/`mdns_windows.go` so all GOOS build until Tasks 6–7).
- [ ] **Step 4: Run** full package tests + `-race`; cross-builds (`GOOS=linux`, `GOOS=windows`).
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: darwin streaming mDNS backend with parallel resolves"`

---

### Task 6: hashicorp/mdns streaming backend (Windows primary, Linux fallback)

**Files:**
- Create: `go/internal/shared/discovery/backend_hashicorp.go` (build tag `//go:build linux || windows`)
- Modify: `go/internal/shared/discovery/mdns_windows.go` (wire `mdnsStreamBackend` = hashicorp), `go/internal/shared/discovery/mdns_linux.go` (keep stub pointing here until Task 7 adds avahi)
- Test: `go/internal/shared/discovery/backend_hashicorp_test.go` (same build tag)

**Interfaces:**
- Consumes: `mdns.Query`/`mdns.DefaultParams` (existing usage at `mdns_linux.go:211-217`), `mdnsEntryMatchesServiceType` + `splitDNSSDLabels` (`mdns_match.go`), `parseMDNSInfoFields` (`discovery_linux.go:194` — move it into `backend_hashicorp.go` since windows needs it too; it's currently linux-only).
- Produces:

```go
var hashicorpRequeryDelay = 2 * time.Second
var hashicorpSweepTimeout = 3 * time.Second

// hashicorpStreamBackend re-queries in a loop until ctx ends. Each sweep
// queries every eligible interface IN PARALLEL (plus the nil default on
// windows, mirroring mdns_windows.go BrowseMDNSServices), forwarding entries
// to emit AS THEY ARRIVE on the entries channel — not after Query returns.
func hashicorpStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error
```

Eligibility: up, multicast, non-loopback (`mdns_linux.go:138-143`); windows reuses its `isMDNSInterfaceEligible`. Entry→MDNSService conversion copies the existing logic at `mdns_linux.go:168-207` (service-type filter, instance from first DNS-SD label, IPv4 preferred, IPv6 zone suffix for link-local, TXT via `parseMDNSInfoFields`), with `InterfaceName` set. The engine dedups repeated emissions across sweeps (Task 3 behavior), so the backend just emits everything.

- [ ] **Step 1: Write failing test** (runs under `GOOS=linux`/`windows` only; use a local `mdns.NewMDNSService`+`mdns.NewServer` fixture from hashicorp/mdns to advertise a test service in-process, as hashicorp's own tests do): assert the backend emits the advertised service within one sweep and again after a re-query, and that two interfaces are queried concurrently (inject an `ifaceListFn` seam returning fakes; assert sweep wall-clock < 2× single-interface timeout using shrunk vars).
- [ ] **Step 2: Run on linux** — `GOOS=linux go vet ./go/internal/shared/discovery/` for compile, and if on macOS run the tests in a linux container or lean on CI; minimum bar: `GOOS=linux go build` + `GOOS=windows go build` pass and the fixture test passes wherever it can execute.
- [ ] **Step 3: Implement.** **Step 4: builds + tests green.**
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: streaming hashicorp mdns backend (parallel interfaces, live forwarding)"`

---

### Task 7: Linux Avahi-over-D-Bus backend

**Files:**
- Create: `go/internal/shared/discovery/avahi_dbus_linux.go`
- Modify: `go/internal/shared/discovery/mdns_linux.go` (`mdnsStreamBackend` = avahi, falling back to hashicorp)
- Test: `go/internal/shared/discovery/avahi_dbus_linux_test.go`

**Interfaces:**
- Consumes: `github.com/godbus/dbus/v5` (already in go.mod).
- Produces:

```go
// avahiStreamBackend browses via the Avahi daemon's D-Bus API. Returns
// errAvahiUnavailable when the system bus or Avahi service is absent.
func avahiStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error

var errAvahiUnavailable = errors.New("avahi d-bus service unavailable")

// mdns_linux.go:
func mdnsStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error {
    err := avahiStreamBackend(ctx, serviceType, emit)
    if errors.Is(err, errAvahiUnavailable) {
        logMDNSQueryErr("avahi-dbus", err) // WENDY_MDNS_DEBUG visibility
        return hashicorpStreamBackend(ctx, serviceType, emit)
    }
    return err
}
```

D-Bus specifics (exact calls):
1. `conn, err := dbus.SystemBus()` — error → `errAvahiUnavailable`.
2. Probe availability: `conn.Object("org.freedesktop.Avahi", "/").CallWithContext(ctx, "org.freedesktop.Avahi.Server.GetVersionString", 0)` — error → `errAvahiUnavailable`.
3. Subscribe BEFORE creating the browser (signals race the reply): `conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.Avahi.ServiceBrowser"))`, `sigCh := make(chan *dbus.Signal, 64)`, `conn.Signal(sigCh)`.
4. `ServiceBrowserNew(int32(-1) /*IF_UNSPEC*/, int32(-1) /*PROTO_UNSPEC*/, serviceType, "local", uint32(0))` → `dbus.ObjectPath`; filter incoming signals on `sig.Path == browserPath`.
5. `ItemNew` body: `int32 iface, int32 proto, string name, string type, string domain, uint32 flags`. For each, resolve synchronously (bounded worker pool of `probeWorkers`, mirroring Task 5): `ResolveService(iface, proto, name, type, domain, int32(0) /*aprotocol=INET → IPv4 answer*/, uint32(0))`; on failure retry once with `aprotocol=-1` (IPv6-only devices). Reply: `(int32, int32, string name, string type, string domain, string host, int32 aprotocol, string address, uint16 port, [][]byte txt, uint32 flags)`.
6. TXT `[][]byte` → `map[string]string` via a small `txtFromByteSlices` (split each on first `=`; first key wins — same rule as `parseTXTRecord` at `mdns.go:26`).
7. `InterfaceName` from `net.InterfaceByIndex(int(iface))` (Avahi iface index == kernel ifindex). IPv6 link-local addresses get the `%iface` zone suffix (rule at `mdns_linux.go:111`).
8. `ItemRemove` → emit nothing (the engine's offline logic owns removals; a future enhancement can plumb it, YAGNI now).
9. Cleanup: on ctx done call `Free()` on the browser object path (`org.freedesktop.Avahi.ServiceBrowser.Free`), `conn.RemoveSignal(sigCh)`, close conn.

- [ ] **Step 1: Write failing tests.** Two layers: (a) pure-function tests for `txtFromByteSlices` and the ItemNew-body → resolve-call plumbing extracted as a function taking an interface `avahiConn { Call(method string, args ...any) ([]any, error) }` with a scripted fake (this is where ordering, path filtering, and the IPv4-then-unspec retry are pinned); (b) an opt-in live test gated on `os.Getenv("WENDY_AVAHI_LIVE_TEST") != ""` that registers via avahi and browses (runs on a real Linux box; on-device verify).
- [ ] **Step 2:** `GOOS=linux go build ./go/internal/shared/discovery/` + run fake-conn tests (they're pure Go; if they can't build on darwin due to the build tag, run them via CI or a container — minimum bar as Task 6).
- [ ] **Step 3: Implement.** **Step 4: builds + tests green.**
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: avahi d-bus streaming backend — no child processes on linux"`

---

### Task 8: Cut everything over to the backends — `DiscoverLAN`, `Discover`, and the generic browse APIs

All three platforms now have real `mdnsStreamBackend` implementations; this task makes them the only path.

**Files:**
- Modify: `go/internal/shared/discovery/discovery.go` (DiscoverLAN + DiscoveryOptions), `go/internal/shared/discovery/mdns.go` (shared generic implementations), `mdns_darwin.go`, `mdns_linux.go`, `mdns_windows.go` (delete per-platform `BrowseMDNSServices`/`BrowseMDNSServicesContinuous`)
- Test: `go/internal/shared/discovery/mdns_test.go` (extend)

**Part A — wendy discovery cutover:**

```go
// discovery.go — DiscoverLAN keeps its exact signature, now stream-backed:
func DiscoverLAN(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
    if timeout == 0 { timeout = defaultTimeout }
    return CollectLAN(ctx, StreamOptions{}, timeout)
}

// discovery.go — DiscoveryOptions gains a LAN field (zero value = old behavior):
type DiscoveryOptions struct {
    Types   []models.InterfaceType
    Timeout time.Duration
    LAN     StreamOptions // cache/prober config for the LAN scan
}
// Discover's LAN goroutine changes from discoverLAN(ctx, timeout) to
// CollectLAN(ctx, opts.LAN, timeout).
```

`go build ./go/...` — `Discover` callers (`cmd/wendy-agent/main.go:645`, `internal/agent/services/mesh_dialer.go:203`) compile unchanged (zero-value `LAN` field, nil prober, no cache).

**Part B — generic browse APIs:**

**Interfaces:**
- Consumers that must keep working unchanged: `go/internal/cli/providers/microwendy.go:60` (`BrowseMDNSServices(ctx, type, 3*time.Second)`) and `:120` (`BrowseMDNSServicesContinuous(ctx, type)`).
- Produces (single shared implementation in mdns.go, all platforms):

```go
func BrowseMDNSServicesContinuous(ctx context.Context, serviceType string) (<-chan MDNSService, error) {
    ch := make(chan MDNSService, 16)
    go func() {
        defer close(ch)
        _ = mdnsStreamBackend(ctx, serviceType, func(svc MDNSService) {
            select { case ch <- svc: case <-ctx.Done(): }
        })
    }()
    return ch, nil
}

// BrowseMDNSServices collects from the stream with dnssdBrowseSettle-style
// early exit (500ms after the last new service) capped at timeout; dedup key
// InstanceName+Hostname+Port as today (mdns_darwin.go:39).
func BrowseMDNSServices(ctx context.Context, serviceType string, timeout time.Duration) ([]MDNSService, error)
```

Move `dnssdBrowseSettle` (`discovery_darwin.go:66`) to mdns.go as `browseSettle`. Delete: darwin `BrowseMDNSServices`/`BrowseMDNSServicesContinuous` (`mdns_darwin.go:15-91`), linux versions (`mdns_linux.go:23-39` + `browseMDNSAvahi` + `parseAvahiMDNSService` + `browseMDNSHashicorp` + `queryInterfaceMDNS`), windows versions. Keep `parseAvahiTXT`/`avahiUnescape` only if still referenced (they die fully in Task 13 with `discoverLANAvahi`).

- [ ] **Step 1: Write failing test** — these APIs call `mdnsStreamBackend` directly, so add one seam `var browseBackendFn = mdnsStreamBackend` used by both generic functions; test with a fake backend: continuous forwards everything until cancel; batch settles early (time it) and dedups. Part A needs no new test (CollectLAN behavior was pinned in Task 4; this is rewiring).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement Part A + Part B deletions.** **Step 4:** package tests + `go build ./go/...` + both cross-builds → green (microwendy compiles untouched). Manual smoke on macOS: `wendy discover --timeout 3s` finds a live device (first commit where the shipped path runs on the new engine).
- [ ] **Step 5: gofmt + commit** — `git commit -m "refactor: discovery cutover — DiscoverLAN and generic browse APIs on streaming backends"`

---

### Task 9: `tui.ProbeOffline` state

**Files:**
- Modify: `go/internal/cli/tui/picker.go` (`ProbeState` enum at line 42, `probeColumnValue` at line 76)
- Test: `go/internal/cli/tui/picker_test.go` (extend existing probe-column tests; find them with `grep -n probeColumnValue go/internal/cli/tui/*_test.go`)

**Interfaces:**
- Produces: `ProbeOffline ProbeState` (add after `ProbeFailed`); `probeColumnValue(ProbeOffline, ...)` renders the literal string `offline` (the table's existing dim styling applies; no new color codes).

- [ ] **Step 1: Failing test** — `if got := probeColumnValue(ProbeOffline, "1.2.3", "·"); got != "offline" { t.Fatalf(...) }` plus: version text must be suppressed for offline rows.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test ./go/internal/cli/tui/` → PASS.
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: offline probe state for device rows"`

---

### Task 10: Picker + discover TUI consume StreamLAN

**Files:**
- Modify: `go/internal/cli/commands/helpers.go` (pickDevice at :2302, new `lanProber`), `go/internal/cli/commands/discover.go` (model at :284)
- Test: `go/internal/cli/commands/discover_stream_test.go` (new)

**Interfaces:**
- Consumes: `discovery.StreamLAN`, `discovery.LANEvent`, `tui.ProbeOffline`, `resolveLANVersion` (`helpers.go:709`).
- Produces:

```go
// helpers.go — the CLI's prober: agent version probe + mTLS truth.
func lanProber(ctx context.Context, dev models.LANDevice) (models.LANDevice, error) {
    resolved, isMTLS, err := resolveLANVersion(ctx, dev)
    if err != nil {
        return dev, err
    }
    resolved.IsMTLS = isMTLS
    return resolved, nil
}

// helpers.go — seam for tests (replaces the deleted DiscoverLANContinuous usage):
var lanStreamFn = discovery.StreamLAN
```

**Picker changes** (`helpers.go:2338-2403`): replace the `lanCh` goroutine block (DiscoverLANContinuous + per-device probe + 5×2s retry loop — all of it) with:

```go
events := lanStreamFn(discoverCtx, discovery.StreamOptions{UseCache: true, Prober: lanProber})
go func() {
    for ev := range events {
        switch ev.Kind {
        case discovery.LANCached:
            sendLANItem(ev.Device, false, tui.ProbePending)
        case discovery.LANFound, discovery.LANUpdated:
            if ev.Probed {
                sendLANItem(ev.Device, !ev.Device.IsMTLS, tui.ProbeOK)
            } else {
                sendLANItem(ev.Device, false, tui.ProbePending)
            }
        case discovery.LANOffline:
            sendLANItem(ev.Device, false, tui.ProbeOffline)
        }
    }
}()
```

The engine's offline-retry (Task 3) replaces the hand-rolled 5-attempt loop. `sendLANItem`'s `hint` logic keeps its "suppress while pending" guard (treat `ProbeOffline` like a concluded probe: hint allowed).

**Discover TUI changes** (`discover.go`): delete `scanLAN`, `probeLANCmd`, `lanScanMsg`, `lanProbeMsg` and their `Update` handling; add:

```go
type lanEventMsg struct {
    ev discovery.LANEvent
    ch <-chan discovery.LANEvent
}

func waitLANEvent(ch <-chan discovery.LANEvent) tea.Cmd {
    return func() tea.Msg {
        ev, ok := <-ch
        if !ok {
            return nil
        }
        return lanEventMsg{ev: ev, ch: ch}
    }
}
```

`Init` (when LAN included): start `lanStreamFn(m.ctx, discovery.StreamOptions{UseCache: true, Prober: lanProber})`, issue `waitLANEvent`. In `Update` on `lanEventMsg`: upsert `ev.Device` into `m.collection.LANDevices` keyed by `discoverycache.Key(dev.ID, dev.DisplayName)` (replace-in-place or append; keep `sortLANDevicesForDiscover` + `refreshTable()`), set `m.probe[strings.ToLower(dev.DisplayName)]` to `ProbePending` (Cached / unprobed Found), `ProbeOK` (Probed), or `ProbeOffline`; re-issue `waitLANEvent(msg.ch)`. The 3s LAN re-scan interval logic dies; USB/Ethernet/BLE/External cadences untouched.

- [ ] **Step 1: Write failing test** — drive the model directly (bubbletea models are pure): fake `lanStreamFn` returning a prepared channel; send `Init`'s cmd msgs through `Update`; assert: a `LANCached` event puts a row with `ProbePending`, a probed `LANFound` for the same key updates (not duplicates) the row to `ProbeOK`, `LANOffline` sets `ProbeOffline`, and row count stays 1 throughout.
- [ ] **Step 2: Run** `go test ./go/internal/cli/commands/ -run TestDiscoverStream` → FAIL.
- [ ] **Step 3: Implement both surfaces.** **Step 4:** `go test ./go/internal/cli/commands/` (whole package — picker tests must still pass) → PASS.
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: picker and discover TUI stream LAN devices — cached rows instant"`

---

### Task 11: One-shot/JSON, MCP `device_list`, `fleet lan` use the cached collector

**Files:**
- Modify: `go/internal/cli/commands/discover.go` (discoverOnce :147, discoverJSON :122, newDiscoverCmd :39), `go/internal/cli/mcp/server.go` (:33), `go/internal/cli/commands/fleet_lan.go` (:110), the `mcp.New` call site (find: `grep -rn "mcp.New(" go/internal/cli/commands/`)
- Test: extend `go/internal/cli/commands/discover_stream_test.go`; `go/internal/cli/mcp/server_test.go` if present

**Interfaces:**
- Produces:

```go
// commands — one place defines the CLI's LAN stream options:
func cliLANStreamOptions() discovery.StreamOptions {
    return discovery.StreamOptions{UseCache: true, Prober: lanProber}
}

// mcp/server.go — exported setter:
func (s *mcpServer) SetLANDiscoverer(fn func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error))
```

Changes:
1. `newDiscoverCmd`: set `opts.LAN = cliLANStreamOptions()` for both the JSON and one-shot paths (the continuous TUI path was rewired in Task 10 and no longer reads `opts.Timeout` for LAN).
2. `discoverOnce`: delete the `collection.LANDevices = resolveLANVersions(ctx, collection.LANDevices)` line (:155) — the stream's prober already filled versions; a probe-failed device keeps an empty version exactly as before.
3. `discoverJSON`: nothing beyond (1) — `CollectLAN` is confirmed-only by construction.
4. MCP: at the `mcp.New` call site add `srv.SetLANDiscoverer(func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) { return discovery.CollectLAN(ctx, cliLANStreamOptions(), timeout) })`.
5. `fleet_lan.go:110`: `discovery.CollectLAN(ctx, cliLANStreamOptions(), timeout)`.
6. `helpers.go:535` `discoverLANDevices` and `:1144` `lanBrowseFn` package vars: point both at `func(ctx, timeout) { return discovery.CollectLAN(ctx, cliLANStreamOptions(), timeout) }` so every remaining batch consumer gets cache-probe acceleration. Tests overriding these vars keep working (signature unchanged).

- [ ] **Step 1: Failing test** — for `discoverOnce`'s worker: fake `lanStreamFn`? No — these paths go through `discovery.Discover`/`CollectLAN`; instead test at the seam: override `lanBrowseFn` consumers indirectly is already covered; the new test asserts `cliLANStreamOptions().UseCache == true` and `Prober != nil`, and (compile-level) that `resolveLANVersions` is no longer referenced from discover.go (`grep -c resolveLANVersions go/internal/cli/commands/discover.go` == 0 — enforce by deleting; if `resolveLANVersions` loses its last caller, delete the function and its test).
- [ ] **Step 2-4: Implement, full `go test ./go/internal/...` green.**
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: cached+probed LAN collection for one-shot, JSON, MCP, fleet"`

---

### Task 12: `resolveAddrOnce` cache fast path + connect upserts

**Files:**
- Modify: `go/internal/cli/commands/helpers.go` (`connectWithAutoTLSDiagnostics` at :1252, near `resolveAddrOnce` at :1181)
- Test: `go/internal/cli/commands/helpers_test.go` (extend; existing tests already fake `osLookupHostFn`/`lanBrowseFn`)

**Interfaces:**
- Consumes: `discoverycache.Load`, `normalizeMDNSHost` (`helpers.go:1223`).
- Produces:

```go
// cachedDeviceIP returns the cached IP for host when a fresh cache entry's
// hostname matches (normalizeMDNSHost equality), else "".
// Seam for tests: var deviceCacheLoadFn = discoverycache.Load
func cachedDeviceIP(host string) string
```

In `connectWithAutoTLSDiagnostics` (:1261 today):

```go
resolved := resolveAddrOnce(ctx, plaintextAddr)
```

becomes:

```go
fromCache := false
plainHost, plainPort, splitErr := net.SplitHostPort(plaintextAddr)
if splitErr == nil && net.ParseIP(plainHost) == nil {
    if ip := cachedDeviceIP(plainHost); ip != "" {
        plaintextAddr, fromCache = net.JoinHostPort(ip, plainPort), true
    }
}
if !fromCache {
    plaintextAddr = resolveAddrOnce(ctx, plaintextAddr)
}
```

…and at the function's connection-failure exit: if `fromCache`, re-resolve via `resolveAddrOnce(ctx, originalAddr)` and retry the dial sequence **once** (a stale cached IP must never make a reachable device unreachable — spec §4). On successful connect (wherever the function returns a live conn), upsert `{ID: host, DisplayName: normalizeMDNSHost(host), Hostname: host, IP: dialedIP, Port: port}` + `Flush` — keeps the cache warm from plain `wendy run`/`device info` with no discovery UI. Read the function fully before editing; place the retry so the mTLS/plaintext fallback ladder is preserved verbatim on both passes.

- [ ] **Step 1: Failing tests** — (a) fresh cache entry for `orin.local` → dial target is the cached IP and `osLookupHostFn` is never called (assert via counting fake); (b) cache miss → old path (`osLookupHostFn` called); (c) cached IP + failing dialer → second attempt uses mDNS-resolved address (fake `lanBrowseFn` provides the new IP). Use the existing fake-dialer pattern in helpers tests if present; otherwise seam the dial via the existing grpcclient connect var if one exists — check `grep -n "connectFn\|dialFn" go/internal/cli/commands/helpers.go` and follow the file's established seam style.
- [ ] **Step 2-4: Implement, package tests green.**
- [ ] **Step 5: gofmt + commit** — `git commit -m "feat: instant default-device connects from device cache"`

---

### Task 13: Cleanup — delete the dead batch/continuous machinery

**Files:**
- Modify/delete within: `go/internal/shared/discovery/discovery.go`, `discovery_darwin.go`, `discovery_linux.go`, `discovery_windows.go`, `mdns_linux.go`
- Test: existing suites keep passing; deleted code's tests are deleted with it

Delete, verifying zero references first (`grep -rn <name> go/ | grep -v _test`):
- `DiscoverLANContinuous` (discovery.go:163) + `discoverLANContinuous` (all three platforms) — the picker was its only consumer (Task 10 removed that).
- Per-platform `discoverLAN` implementations + `discoverLANAvahi` + `parseAvahiResolveLine` + `avahiUnescape` + `parseAvahiTXT` + `hasAvahiBrowse` (linux), `deviceFromBrowse`/`dnssdResolve`/`dnssdBrowse` remnants (darwin) — `DiscoverLAN` is stream-backed since Task 4; keep whatever `mdnsStreamBackend`/annotators still use (`dnssdBrowseStream`, `dnssdResolveInstance`, `darwinInterfaceDisplayNameMap`, `setLANNetworkInterface`, `linuxInterfaceLinkSpeed`, `isValidHostnameLabel`).
- The `avahi-browse` exec invocations — after this task `grep -rn "exec.Command\|exec.LookPath" go/internal/shared/discovery/mdns*.go go/internal/shared/discovery/dnssd*.go` must return nothing, and `discovery_linux.go` must have no avahi-browse exec left (Global Constraint: no child processes in the mDNS browse path). Bluetooth/Ethernet/USB/serial shell-outs (`bluetoothctl`, `networksetup`, `ifconfig`, `system_profiler`, powershell) are explicitly out of scope per the spec and stay.

- [ ] **Step 1:** For each symbol: grep, delete symbol + its tests, build.
- [ ] **Step 2:** Full suite: `go test ./go/internal/... -race` → PASS. Cross-builds pass. `grep -rn "exec.Command" go/internal/shared/discovery/` → empty.
- [ ] **Step 3:** `gofmt -l .` → empty.
- [ ] **Step 4: Commit** — `git commit -m "refactor: remove batch-era mDNS machinery replaced by StreamLAN"`

---

### Task 14: End-to-end verification (manual, on hardware)

Not a code task — the acceptance pass before PR:

- [ ] macOS + a live device (Orin Nano or Pi): `wendy discover` twice — second run shows the device instantly with a spinner that resolves to ProbeOK; power the device off, re-run within an hour: row appears then flips to `offline` in ~4s and stays listed.
- [ ] `wendy discover --timeout 5s --json` with a device up: returns in well under 5s; entry has live agentVersion; power device off + cache fresh: JSON contains **no** entry for it (confirmed-only).
- [ ] Picker (`wendy run` with no default): cached device selectable immediately.
- [ ] Default device set: `wendy device info` connects with no multi-second mDNS pause (compare `time wendy device info` before/after).
- [ ] Linux host (Ubuntu box or container with avahi): `WENDY_MDNS_DEBUG=1 wendy discover` — confirms avahi-dbus backend; stop avahi-daemon → falls back to hashicorp and still finds the device; `ps` shows no avahi-browse/dns-sd children at any point.
- [ ] `gofmt -l .` clean, full `go test ./go/internal/...` green, then PR from `ed/instant-mdns-discovery` (base: main, after the spec branch merges or with the spec commits included).

## Self-review notes (done at write time)

- Spec coverage: cache §1→Task 1; stream core §2→Tasks 2-8 (batch-on-stream lands in 4, cuts over in 8); verification §3→Task 3; surfaces §4→Tasks 10-12; error handling §5→Tasks 1/3/7 (incl. backend restart-with-backoff in Task 3 behavior 7); testing §6→each task's step 1; generic-API consumers (microwendy)→Task 8; cleanup promise ("shell-out deleted")→Task 13.
- Sequencing: every commit on the branch leaves the CLI functional — the old batch paths keep serving `DiscoverLAN` until Task 8, when all three platform backends exist.
- Type consistency: `discoverycache.Key/Entry/Fresh/Upsert/Flush`, `LANEvent{Kind,Device,Probed}`, `StreamOptions{UseCache,Prober}`, `mdnsStreamBackend(ctx, serviceType, emit)`, `CollectLAN(ctx, opts, timeout)`, `lanProber`, `cliLANStreamOptions` — names used identically across tasks.
- Known judgment calls left to implementers: engine-internal channel shapes (Task 3), exact placement of the retry in `connectWithAutoTLSDiagnostics` (Task 12 — read the whole function first), CI skip guards for network-dependent tests (mirror existing patterns in the same files).
