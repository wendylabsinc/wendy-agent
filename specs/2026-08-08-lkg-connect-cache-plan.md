# Last-Known-Good Connect Cache — Implementation Plan (delta over #1616)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the four gaps the base branch leaves in its device-cache connect fast path: the 1h TTL cliff, the unbounded dead-IP cost, plaintext-first/config-order ladder on cache hits, and write-back that clobbers the advertised mTLS port.

**Architecture:** All changes ride the existing machinery in `go/internal/cli/commands/helpers.go` (`cachedDeviceEntry`/`cachedDeviceIP`, `dialAgentLadder`, `cacheConnectSuccess`, seams `deviceCacheLoadFn`/`dialAgentLadderFn`/`cacheFastPathReachableFn`) plus one new `discoverycache` accessor. New pieces: `Entries()` (any-age), `rotateCertsForOrg`, `dialAgentLKG` (TCP pre-check + direct mTLS-port dial), and endpoint-faithful write-back via a new `AgentConnection.Addr` field. Spec: `specs/2026-08-08-lkg-connect-cache-design.md`.

**Tech Stack:** Go 1.26; no new dependencies.

## Global Constraints

- Branch `ed/lkg-connect-cache` is **stacked on `ed/instant-mdns-discovery`**; the PR targets that branch.
- Module root = repo root; packages under `go/...`; run `go` commands from repo root; specs/plans live in top-level `specs/` (root `docs/` is a symlink into embedded CLI assets).
- The fast path NEVER changes trust: same certs, same verifiers, same pins. It is routing only; every failure falls through to the existing flow.
- `lkgTCPConnectTimeout = 1 * time.Second` (exact name/value). The TCP pre-check bounds dead-IP cost; the handshake itself stays governed by `mtlsProbeTimeout`.
- Display surfaces keep using `Fresh` — the 1h TTL is untouched for the picker/discovery; ONLY the connect lookup goes any-age.
- `WENDY_TLS_DEBUG` (existing env) gates the new `[tls-debug] lkg …` lines; no new env vars.
- Existing base-branch tests may assert the OLD TTL-gated connect behavior — update those assertions to the new any-age contract, changing nothing else about them; never weaken unrelated assertions.
- Commit messages end with: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`
- Before pushing: `gofmt -l .` from repo root must be empty.

---

### Task 1: `Entries()` + any-age, most-recent-wins connect lookup

**Files:**
- Modify: `go/internal/shared/discoverycache/cache.go` (add `Entries`)
- Modify: `go/internal/cli/commands/helpers.go:1276-1306` (`cachedDeviceEntry` doc + body)
- Test: `go/internal/shared/discoverycache/cache_test.go`, `go/internal/cli/commands/helpers_test.go`

**Interfaces:**
- Consumes: existing `Cache.Fresh`, `normalizeMDNSHost`, `deviceCacheLoadFn` seam.
- Produces: `func (c *Cache) Entries() []Entry`; `cachedDeviceEntry(cache, host)` now any-age + most-recent-wins (same signature). Tasks 3-4 rely on the entry's `IP`/`Port`/`MTLS`/`OrgID` fields being reachable through it.

- [ ] **Step 1: Write the failing tests**

Append to `go/internal/shared/discoverycache/cache_test.go`:

```go
func TestEntriesIncludesStale(t *testing.T) {
	c, err := LoadFrom(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	now := time.Now()
	c.Upsert(Entry{ID: "fresh-dev", Hostname: "fresh.local", IP: "10.0.0.1"}, now)
	c.Upsert(Entry{ID: "stale-dev", Hostname: "stale.local", IP: "10.0.0.2"}, now.Add(-2*TTL))
	if got := len(c.Fresh(now)); got != 1 {
		t.Fatalf("Fresh = %d entries, want 1 (display TTL must be unchanged)", got)
	}
	if got := len(c.Entries()); got != 2 {
		t.Fatalf("Entries = %d, want 2 (stale included)", got)
	}
}
```

Append to `go/internal/cli/commands/helpers_test.go` (follow the file's existing pattern for seeding `deviceCacheLoadFn` with a `discoverycache.LoadFrom` temp file — see the existing `cacheConnectSuccess` tests around line 1446):

```go
func TestCachedDeviceEntryAnyAgeMostRecentWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	cache, err := discoverycache.LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	old := time.Now().Add(-3 * discoverycache.TTL)
	newer := time.Now().Add(-2 * discoverycache.TTL)
	// Two distinct device identities sharing one hostname (e.g. a device
	// re-provisioned under a new id): most recent LastSeen must win.
	cache.Upsert(discoverycache.Entry{ID: "dev-old", Hostname: "orin.local", IP: "10.0.0.8"}, old)
	cache.Upsert(discoverycache.Entry{ID: "dev-new", Hostname: "orin.local", IP: "10.0.0.9"}, newer)

	e, ok := cachedDeviceEntry(cache, "orin.local")
	if !ok {
		t.Fatal("stale entries not matched — connect lookup must be any-age")
	}
	if e.IP != "10.0.0.9" {
		t.Errorf("matched IP %q, want most-recent 10.0.0.9", e.IP)
	}
	// Bare-name form matches the .local stored form.
	if _, ok := cachedDeviceEntry(cache, "orin"); !ok {
		t.Error("bare hostname did not match .local entry")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/shared/discoverycache/ ./go/internal/cli/commands/ -run 'TestEntriesIncludesStale|TestCachedDeviceEntryAnyAge' -v`
Expected: FAIL — `Entries` undefined; any-age match fails.

- [ ] **Step 3: Implement**

`cache.go`, after `Fresh`:

```go
// Entries returns every cached entry regardless of age, in any order. The
// connect fast path uses it — a stale IP is still worth one bounded dial
// attempt, with fallback to fresh resolution — while display surfaces
// (picker, discovery) keep using Fresh so the TTL still bounds what users
// see as "recently seen".
func (c *Cache) Entries() []Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Entry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e)
	}
	return out
}
```

`helpers.go` — replace `cachedDeviceEntry`'s body and update its doc comment (drop the "fresh (within discoverycache.TTL)" claim):

```go
// cachedDeviceEntry returns the device-cache entry, if any, whose Hostname
// matches host (normalizeMDNSHost equality), regardless of the entry's age
// — the connect fast path deliberately uses stale entries too (a stale IP
// costs one bounded dial attempt; the stale-cache retry re-resolves). When
// several entries' hostnames normalize equal (e.g. a device re-provisioned
// under a new identity), the most recent LastSeen wins. Shared by
// cachedDeviceIP's lookup and cacheConnectSuccess's write path so a
// connect-success write always lands under a real device's existing
// identity.
func cachedDeviceEntry(cache *discoverycache.Cache, host string) (discoverycache.Entry, bool) {
	want := normalizeMDNSHost(host)
	var best discoverycache.Entry
	var found bool
	for _, e := range cache.Entries() {
		if normalizeMDNSHost(e.Hostname) != want {
			continue
		}
		if !found || e.LastSeen.After(best.LastSeen) {
			best, found = e, true
		}
	}
	return best, found
}
```

- [ ] **Step 4: Run tests + update any base tests asserting TTL-gated connect lookups**

Run: `go test ./go/internal/shared/discoverycache/ ./go/internal/cli/commands/ -count=1`
If a pre-existing test asserts that a stale entry is NOT matched by `cachedDeviceEntry`/`cachedDeviceIP`, invert that specific assertion to the new any-age contract (and say so in the report); all other tests must pass unchanged.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/discoverycache/ go/internal/cli/commands/
git commit -m "feat(cli): any-age, most-recent-wins device-cache lookup for connects

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `dialAgentLadderWithCerts` extraction + `rotateCertsForOrg`

**Files:**
- Modify: `go/internal/cli/commands/helpers.go:1540-1700` (`dialAgentLadder`)
- Test: `go/internal/cli/commands/helpers_test.go`

**Interfaces:**
- Consumes: existing `dialAgentLadder` body, `loadAllCLICerts`, `config.CertificateInfo`.
- Produces: `func dialAgentLadderWithCerts(ctx context.Context, plaintextAddr string, allCerts []config.CertificateInfo) (*grpcclient.AgentConnection, error, error)` (the old body, cert slice parameterized); `dialAgentLadder` keeps its exact signature/behavior as a one-line wrapper; `func rotateCertsForOrg(certs []config.CertificateInfo, orgID int32) []config.CertificateInfo`; `var dialAgentLadderWithCertsFn = dialAgentLadderWithCerts` (test seam, used by Task 3's `dialAgentLKG`).

- [ ] **Step 1: Write the failing test**

```go
func TestRotateCertsForOrg(t *testing.T) {
	certs := []config.CertificateInfo{
		{OrganizationID: 1}, {OrganizationID: 2}, {OrganizationID: 3}, {OrganizationID: 2},
	}
	orgs := func(cs []config.CertificateInfo) []int {
		out := make([]int, len(cs))
		for i, c := range cs {
			out[i] = c.OrganizationID
		}
		return out
	}
	cases := []struct {
		name  string
		orgID int32
		want  []int
	}{
		{"match moves first, stable within groups", 2, []int{2, 2, 1, 3}},
		{"zero org = unchanged", 0, []int{1, 2, 3, 2}},
		{"no match = unchanged", 9, []int{1, 2, 3, 2}},
	}
	for _, tc := range cases {
		got := orgs(rotateCertsForOrg(certs, tc.orgID))
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	// Input slice must not be mutated.
	if fmt.Sprint(orgs(certs)) != fmt.Sprint([]int{1, 2, 3, 2}) {
		t.Error("rotateCertsForOrg mutated its input")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/cli/commands/ -run TestRotateCertsForOrg -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement**

In `helpers.go`:

```go
// rotateCertsForOrg returns certs reordered so entries whose OrganizationID
// matches orgID come first, preserving relative order within both groups
// (a stable partition). orgID 0 (unknown) or no match returns certs
// unchanged. Never mutates the input.
func rotateCertsForOrg(certs []config.CertificateInfo, orgID int32) []config.CertificateInfo {
	if orgID == 0 {
		return certs
	}
	matched := make([]config.CertificateInfo, 0, len(certs))
	rest := make([]config.CertificateInfo, 0, len(certs))
	for _, c := range certs {
		if int32(c.OrganizationID) == orgID {
			matched = append(matched, c)
		} else {
			rest = append(rest, c)
		}
	}
	if len(matched) == 0 {
		return certs
	}
	return append(matched, rest...)
}
```

Rename the existing `dialAgentLadder` to `dialAgentLadderWithCerts` with the extra `allCerts []config.CertificateInfo` parameter, deleting only its internal `allCerts := loadAllCLICerts()` line (every other line unchanged, comments included), then add:

```go
// dialAgentLadder is dialAgentLadderWithCerts with the CLI's stored certs
// in config order — the shape every non-fast-path caller wants.
func dialAgentLadder(ctx context.Context, plaintextAddr string) (*grpcclient.AgentConnection, error, error) {
	return dialAgentLadderWithCerts(ctx, plaintextAddr, loadAllCLICerts())
}

// dialAgentLadderWithCertsFn is a seam over dialAgentLadderWithCerts for
// tests that need to observe the cert order the LKG fast path passes.
var dialAgentLadderWithCertsFn = dialAgentLadderWithCerts
```

- [ ] **Step 4: Run the full package suite**

Run: `go test ./go/internal/cli/commands/ -count=1`
Expected: all PASS (extraction is behavior-preserving).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/
git commit -m "refactor(cli): parameterize dial ladder certs + org-first rotation helper

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: LKG direct dial (TCP pre-check + mTLS-port-first) in the connect flow

**Files:**
- Modify: `go/internal/cli/commands/helpers.go:1293-1306` (`cachedDeviceIP` → entry-returning helper), `:1364-1383` (`connectWithAutoTLSDiagnostics` head)
- Test: `go/internal/cli/commands/helpers_test.go`

**Interfaces:**
- Consumes: Task 1's any-age `cachedDeviceEntry`, Task 2's `rotateCertsForOrg` + `dialAgentLadderWithCertsFn`, existing `deviceCacheLoadFn`, `hostPort`, `loadAllCLICerts`.
- Produces: `const lkgTCPConnectTimeout = 1 * time.Second`; `func cachedDeviceHostEntry(host string) (discoverycache.Entry, bool)` (loads the cache via `deviceCacheLoadFn` and delegates to `cachedDeviceEntry`); `func dialAgentLKG(ctx context.Context, e discoverycache.Entry) (*grpcclient.AgentConnection, error, bool)` + seam `var dialAgentLKGFn = dialAgentLKG`; `var tcpDialTimeoutFn = net.DialTimeout` (test seam). Task 5's integration tests drive these seams.

- [ ] **Step 1: Write the failing tests**

```go
func TestDialAgentLKGSkipsOnTCPPrecheckFailure(t *testing.T) {
	origTCP := tcpDialTimeoutFn
	tcpDialTimeoutFn = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		if timeout != lkgTCPConnectTimeout {
			t.Errorf("pre-check timeout = %v, want %v", timeout, lkgTCPConnectTimeout)
		}
		return nil, errors.New("no route to host")
	}
	ladderCalled := false
	origLadder := dialAgentLadderWithCertsFn
	dialAgentLadderWithCertsFn = func(ctx context.Context, addr string, certs []config.CertificateInfo) (*grpcclient.AgentConnection, error, error) {
		ladderCalled = true
		return nil, nil, errors.New("must not be reached")
	}
	t.Cleanup(func() { tcpDialTimeoutFn = origTCP; dialAgentLadderWithCertsFn = origLadder })

	_, _, ok := dialAgentLKG(context.Background(), discoverycache.Entry{IP: "10.0.0.9", Port: 50052, MTLS: true, OrgID: 2})
	if ok {
		t.Fatal("dialAgentLKG reported success despite failed TCP pre-check")
	}
	if ladderCalled {
		t.Error("ladder dial ran after failed pre-check — dead IP must cost only the pre-check")
	}
}

func TestDialAgentLKGRotatesCertsAndDialsMTLSPort(t *testing.T) {
	origTCP := tcpDialTimeoutFn
	tcpDialTimeoutFn = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go c2.Close()
		return c1, nil
	}
	var gotAddr string
	var gotOrgs []int
	origLadder := dialAgentLadderWithCertsFn
	dialAgentLadderWithCertsFn = func(ctx context.Context, addr string, certs []config.CertificateInfo) (*grpcclient.AgentConnection, error, error) {
		gotAddr = addr
		for _, c := range certs {
			gotOrgs = append(gotOrgs, c.OrganizationID)
		}
		return &grpcclient.AgentConnection{IsMTLS: true}, nil, nil
	}
	origCerts := loadAllCLICertsFn
	loadAllCLICertsFn = func() []config.CertificateInfo {
		return []config.CertificateInfo{{OrganizationID: 1}, {OrganizationID: 2}}
	}
	t.Cleanup(func() {
		tcpDialTimeoutFn = origTCP
		dialAgentLadderWithCertsFn = origLadder
		loadAllCLICertsFn = origCerts
	})

	conn, _, ok := dialAgentLKG(context.Background(), discoverycache.Entry{IP: "10.0.0.9", Port: 50052, MTLS: true, OrgID: 2})
	if !ok || conn == nil {
		t.Fatal("dialAgentLKG failed on the happy path")
	}
	if gotAddr != "10.0.0.9:50052" {
		t.Errorf("dialed %q, want the entry's mTLS endpoint 10.0.0.9:50052", gotAddr)
	}
	if fmt.Sprint(gotOrgs) != fmt.Sprint([]int{2, 1}) {
		t.Errorf("cert org order = %v, want entry-org-first [2 1]", gotOrgs)
	}
}

func TestDialAgentLKGFallsThroughOnPlaintextDowngrade(t *testing.T) {
	origTCP := tcpDialTimeoutFn
	tcpDialTimeoutFn = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go c2.Close()
		return c1, nil
	}
	origLadder := dialAgentLadderWithCertsFn
	dialAgentLadderWithCertsFn = func(ctx context.Context, addr string, certs []config.CertificateInfo) (*grpcclient.AgentConnection, error, error) {
		return grpcclient.NewFromConn(nil), nil, nil // IsMTLS=false: ladder fell to plaintext
	}
	origCerts := loadAllCLICertsFn
	loadAllCLICertsFn = func() []config.CertificateInfo { return []config.CertificateInfo{{OrganizationID: 1}} }
	t.Cleanup(func() {
		tcpDialTimeoutFn = origTCP
		dialAgentLadderWithCertsFn = origLadder
		loadAllCLICertsFn = origCerts
	})

	_, _, ok := dialAgentLKG(context.Background(), discoverycache.Entry{IP: "10.0.0.9", Port: 50052, MTLS: true})
	if ok {
		t.Fatal("LKG accepted a plaintext downgrade for an entry advertised as mTLS")
	}
}
```

Note: these tests require a `loadAllCLICertsFn` seam. If `loadAllCLICerts` is called directly today, add `var loadAllCLICertsFn = loadAllCLICerts` and use the seam inside `dialAgentLKG` only (the ladder keeps calling `loadAllCLICerts` directly via `dialAgentLadder`).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/cli/commands/ -run TestDialAgentLKG -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement**

In `helpers.go`:

```go
// lkgTCPConnectTimeout bounds the last-known-good fast path's TCP
// pre-check. A dead or reassigned cached IP must cost at most this before
// the connect falls through to fresh resolution — without the bound, the
// full ladder would burn its mtlsProbeTimeout budgets against a black hole.
const lkgTCPConnectTimeout = 1 * time.Second

// tcpDialTimeoutFn is a seam over net.DialTimeout for LKG fast-path tests.
var tcpDialTimeoutFn = net.DialTimeout

// loadAllCLICertsFn is a seam over loadAllCLICerts for LKG fast-path tests.
var loadAllCLICertsFn = loadAllCLICerts

// cachedDeviceHostEntry loads the device cache and returns the entry whose
// hostname matches host (any age, most recent wins). The empty entry and
// false when the cache is unavailable or nothing matches.
func cachedDeviceHostEntry(host string) (discoverycache.Entry, bool) {
	cache, err := deviceCacheLoadFn()
	if err != nil || cache == nil {
		return discoverycache.Entry{}, false
	}
	return cachedDeviceEntry(cache, host)
}

// dialAgentLKG is the last-known-good direct dial: one bounded attempt at a
// cached device's advertised mTLS endpoint with the entry's org's cert
// first. ok=false means "fall through to the ordinary connect flow" — the
// fast path never surfaces its own failures as the connect's outcome.
// Trust is unchanged: the same certs, verifiers, and pins run here as on
// the ordinary path; the cache contributes routing only.
func dialAgentLKG(ctx context.Context, e discoverycache.Entry) (*grpcclient.AgentConnection, error, bool) {
	addr := hostPort(e.IP, e.Port)
	tlsDebug := os.Getenv("WENDY_TLS_DEBUG") != ""
	raw, err := tcpDialTimeoutFn("tcp", addr, lkgTCPConnectTimeout)
	if err != nil {
		if tlsDebug {
			fmt.Fprintf(os.Stderr, "[tls-debug] lkg %s: tcp pre-check failed: %v\n", addr, err)
		}
		return nil, nil, false
	}
	raw.Close()
	certs := rotateCertsForOrg(loadAllCLICertsFn(), e.OrgID)
	if len(certs) == 0 {
		return nil, nil, false
	}
	conn, mtlsErr, err := dialAgentLadderWithCertsFn(ctx, addr, certs)
	if err != nil || conn == nil || !conn.IsMTLS {
		// The entry advertised mTLS; a plaintext downgrade here would be
		// surprising, so route it through the ordinary path instead.
		if conn != nil {
			conn.Close()
		}
		if tlsDebug {
			fmt.Fprintf(os.Stderr, "[tls-debug] lkg %s: direct dial failed: %v\n", addr, err)
		}
		return nil, mtlsErr, false
	}
	if tlsDebug {
		fmt.Fprintf(os.Stderr, "[tls-debug] lkg %s: connected\n", addr)
	}
	return conn, mtlsErr, true
}

// dialAgentLKGFn is a seam over dialAgentLKG for connect-flow tests.
var dialAgentLKGFn = dialAgentLKG
```

Rework the head of `connectWithAutoTLSDiagnostics` (keeping everything from `conn, mtlsErr, err := dialAgentLadderFn(...)` onward byte-identical):

```go
	originalAddr := plaintextAddr
	fromCache := false
	if plainHost, plainPort, splitErr := net.SplitHostPort(plaintextAddr); splitErr == nil && net.ParseIP(plainHost) == nil {
		if e, ok := cachedDeviceHostEntry(plainHost); ok && e.IP != "" {
			// Last-known-good direct dial: the advertised mTLS endpoint with
			// the entry's org's cert first, TCP-bounded so a dead IP costs at
			// most lkgTCPConnectTimeout. Failures fall through to the
			// cached-IP ladder + stale-cache retry below.
			if e.MTLS && e.Port > 0 {
				if conn, mtlsErr, ok := dialAgentLKGFn(ctx, e); ok {
					return conn, mtlsErr, nil
				}
			}
			plaintextAddr, fromCache = net.JoinHostPort(e.IP, plainPort), true
		}
	}
	if !fromCache {
		plaintextAddr = resolveAddrOnce(ctx, plaintextAddr)
	}
```

Delete `cachedDeviceIP` if this leaves it unreferenced outside tests (update those tests to `cachedDeviceHostEntry`); keep it as a thin wrapper only if other production call sites exist (check with grep and say which in the report).

- [ ] **Step 4: Run the full package suite**

Run: `go test ./go/internal/cli/commands/ -count=1`
Expected: all PASS (new + pre-existing, including the base branch's fast-path/stale-retry tests, which exercise the `fromCache` flow this change preserves).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/
git commit -m "feat(cli): last-known-good direct dial — TCP-bounded, mTLS-port-first, org-cert-first

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Endpoint-faithful write-back (fix the port-clobber wart)

**Files:**
- Modify: `go/internal/cli/grpcclient/client.go` (add `Addr` field; set in `Connect` and `ConnectWithTLSAndPins`)
- Modify: `go/internal/cli/commands/helpers.go:1494-1524` (`cacheConnectSuccess`)
- Test: `go/internal/cli/commands/helpers_test.go`

**Interfaces:**
- Consumes: existing `cacheConnectSuccess` call site (`helpers.go:964`, signature unchanged), `AgentConnection.IsMTLS`, `(*AgentConnection).ObservedServerOrg()`.
- Produces: `AgentConnection.Addr string` — the full `host:port` this connection dialed (set by `Connect` and `ConnectWithTLSAndPins`; empty for `ConnectUnix`/`NewFromConn`). `cacheConnectSuccess` stores the actually-connected port, `MTLS: conn.IsMTLS`, and `OrgID` from `ObservedServerOrg()` when non-zero.

- [ ] **Step 1: Write the failing test**

```go
func TestCacheConnectSuccessStoresActualEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	seed, err := discoverycache.LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	now := time.Now()
	// Discovery stored the advertised mTLS port; a connect via the plaintext
	// originalAddr port must NOT clobber it with 50051.
	seed.Upsert(discoverycache.Entry{ID: "dev-1", Hostname: "orin.local", IP: "10.0.0.9", Port: 50052, MTLS: true}, now)
	if err := seed.Flush(now); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	origLoad := deviceCacheLoadFn
	deviceCacheLoadFn = func() (*discoverycache.Cache, error) { return discoverycache.LoadFrom(path) }
	t.Cleanup(func() { deviceCacheLoadFn = origLoad })

	conn := &grpcclient.AgentConnection{Host: "10.0.0.9", IsMTLS: true, Addr: "10.0.0.9:50052"}
	cacheConnectSuccess("orin.local:50051", conn)

	after, _ := discoverycache.LoadFrom(path)
	e, ok := cachedDeviceEntry(after, "orin.local")
	if !ok {
		t.Fatal("entry missing after write-back")
	}
	if e.Port != 50052 {
		t.Errorf("Port = %d after mTLS connect on 50052, want 50052 (originalAddr's 50051 must not clobber)", e.Port)
	}
	if !e.MTLS {
		t.Error("MTLS flag lost on write-back")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/cli/commands/ -run TestCacheConnectSuccessStoresActualEndpoint -v`
Expected: FAIL — `Addr` undefined / Port clobbered to 50051.

- [ ] **Step 3: Implement**

`grpcclient/client.go`: add to the `AgentConnection` struct (near `Host`):

```go
	// Addr is the full host:port this connection dialed — the endpoint that
	// actually answered, mTLS port included. Empty for unix-socket and
	// pre-built (NewFromConn) connections.
	Addr string
```

Set `ac.Addr = address` in `Connect` and in `ConnectWithTLSAndPins` (next to the existing `ac.Host = hostFromAddress(address)` lines).

`helpers.go` — in `cacheConnectSuccess`, after the existing guards, derive the stored port from the connection's real endpoint and enrich the entry:

```go
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return
	}
	// Prefer the endpoint the connection actually dialed (mTLS port when the
	// ladder stepped to port+1) over originalAddr's port — otherwise a
	// default-device connect (plaintext :50051 in originalAddr) would clobber
	// discovery's advertised mTLS port in the cache on every command.
	if _, connPortStr, splitErr := net.SplitHostPort(conn.Addr); splitErr == nil {
		if connPort, convErr := strconv.Atoi(connPortStr); convErr == nil {
			port = connPort
		}
	}
	// (the existing deviceCacheLoadFn load + guards stay exactly as they are
	// between the port derivation above and the entry literal below)
	entry := discoverycache.Entry{
		Hostname: cacheHostnameForStorage(host),
		IP:       conn.Host,
		Port:     port,
		MTLS:     conn.IsMTLS,
	}
	if org, ok := conn.ObservedServerOrg(); ok {
		entry.OrgID = org
	}
```

(`MTLS: false` on a plaintext connect is a zero value, so Upsert's non-zero-wins merge keeps a previously-true flag — matching today's semantics for that field; the port fix is the behavioral change.)

- [ ] **Step 4: Run the suites**

Run: `go test ./go/internal/cli/commands/ ./go/internal/cli/grpcclient/ -count=1`
Expected: all PASS (existing `cacheConnectSuccess` tests construct `AgentConnection` literals without `Addr` — their fallback path keeps them green; update any that now assert the old clobbering behavior, noting it in the report).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/grpcclient/ go/internal/cli/commands/
git commit -m "fix(cli): connect write-back stores the actually-dialed endpoint + MTLS/org

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Connect-flow integration tests

**Files:**
- Test: `go/internal/cli/commands/helpers_lkg_test.go` (new)

**Interfaces:**
- Consumes: seams `deviceCacheLoadFn`, `dialAgentLKGFn`, `dialAgentLadderFn`, `osLookupHostFn`, `lanBrowseFn` (all existing or added in Tasks 1-3) and `connectWithAutoTLSDiagnostics` itself.

- [ ] **Step 1: Write the tests**

```go
package commands

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/discoverycache"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// seedLKGCache installs a deviceCacheLoadFn serving one entry, stale by
// stalenessFactor×TTL (0 = fresh).
func seedLKGCache(t *testing.T, e discoverycache.Entry, stale time.Duration) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devices.json")
	c, err := discoverycache.LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	now := time.Now().Add(-stale)
	c.Upsert(e, now)
	if err := c.Flush(now); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	orig := deviceCacheLoadFn
	deviceCacheLoadFn = func() (*discoverycache.Cache, error) { return discoverycache.LoadFrom(path) }
	t.Cleanup(func() { deviceCacheLoadFn = orig })
}

func TestConnectFastPathStaleEntryZeroResolution(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "orin.local", IP: "10.0.0.9", Port: 50052, MTLS: true, OrgID: 2,
	}, 3*discoverycache.TTL) // well past the display TTL

	resolverCalls := 0
	origLookup, origBrowse := osLookupHostFn, lanBrowseFn
	osLookupHostFn = func(ctx context.Context, host string) ([]string, error) {
		resolverCalls++
		return nil, errors.New("resolver must not run on an LKG hit")
	}
	lanBrowseFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		resolverCalls++
		return nil, errors.New("browse must not run on an LKG hit")
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry) (*grpcclient.AgentConnection, error, bool) {
		if e.IP != "10.0.0.9" || e.Port != 50052 {
			t.Errorf("LKG got entry %s:%d, want 10.0.0.9:50052", e.IP, e.Port)
		}
		return want, nil, true
	}
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error, error) {
		t.Errorf("general ladder ran despite LKG success (addr %s)", addr)
		return nil, nil, errors.New("unreachable")
	}
	t.Cleanup(func() {
		osLookupHostFn, lanBrowseFn = origLookup, origBrowse
		dialAgentLKGFn, dialAgentLadderFn = origLKG, origLadder
	})

	conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "orin.local:50051")
	if err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want the LKG connection", conn, err)
	}
	if resolverCalls != 0 {
		t.Errorf("resolver invoked %d times on a stale-entry LKG hit, want 0 (any-age fast path)", resolverCalls)
	}
}

func TestConnectFastPathLKGFailureFallsThroughToLadder(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "orin.local", IP: "10.0.0.9", Port: 50052, MTLS: true,
	}, 0)

	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry) (*grpcclient.AgentConnection, error, bool) {
		return nil, nil, false // dead IP: pre-check failed
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	var ladderAddr string
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error, error) {
		ladderAddr = addr
		return want, nil, nil
	}
	origReach := cacheFastPathReachableFn
	cacheFastPathReachableFn = func(ctx context.Context, conn *grpcclient.AgentConnection, err error) bool { return true }
	t.Cleanup(func() {
		dialAgentLKGFn, dialAgentLadderFn, cacheFastPathReachableFn = origLKG, origLadder, origReach
	})

	conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "orin.local:50051")
	if err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want ladder connection after LKG fall-through", conn, err)
	}
	if ladderAddr != "10.0.0.9:50051" {
		t.Errorf("ladder addr = %q, want cached-IP fallback 10.0.0.9:50051", ladderAddr)
	}
}

func TestConnectFastPathNonMTLSEntrySkipsLKG(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "pi.local", IP: "10.0.0.7", Port: 50051, MTLS: false,
	}, 0)
	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry) (*grpcclient.AgentConnection, error, bool) {
		t.Error("LKG ran for a non-mTLS entry")
		return nil, nil, false
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error, error) {
		return want, nil, nil
	}
	origReach := cacheFastPathReachableFn
	cacheFastPathReachableFn = func(ctx context.Context, conn *grpcclient.AgentConnection, err error) bool { return true }
	t.Cleanup(func() { dialAgentLKGFn, dialAgentLadderFn, cacheFastPathReachableFn = origLKG, origLadder, origReach })

	if conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "pi.local:50051"); err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want ladder connection", conn, err)
	}
}
```

Adjust seam names to the actual ones if they differ (`osLookupHostFn`/`lanBrowseFn` exist on the base branch in `helpers.go` — verify signatures before writing).

- [ ] **Step 2: Run**

Run: `go test ./go/internal/cli/commands/ -run 'TestConnectFastPath' -count=5 -v`
Expected: all PASS, no flakes.

- [ ] **Step 3: Full suite + commit**

Run: `go test ./go/internal/cli/commands/ -count=1`

```bash
git add go/internal/cli/commands/helpers_lkg_test.go
git commit -m "test(cli): LKG connect-flow integration coverage

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Full verification, push, stacked draft PR

- [ ] **Step 1: gofmt + build + suites**

```bash
gofmt -l .
go build ./go/...
go test ./go/internal/cli/commands/ ./go/internal/shared/discoverycache/ ./go/internal/cli/grpcclient/ -count=1
```
Expected: gofmt empty; build clean; all green. Report (don't broadly fix) any failure not caused by this branch's files.

- [ ] **Step 2: Push + PR**

```bash
git push -u origin ed/lkg-connect-cache
gh pr create --draft --base ed/instant-mdns-discovery --title "Last-known-good connect: any-age cache, bounded dead-IP cost, mTLS-port + org-cert first" --body "$(cat <<'EOF'
## Summary
Delta over #1616's device-cache connect fast path, closing its four remaining gaps:

- **Any-age lookup** for connects (`Entries()` + most-recent-wins): the 1h TTL stays a display bound; the first command after a quiet morning no longer pays ~1.3s of mDNS resolution.
- **Bounded dead-IP cost**: a 1s TCP pre-check (`lkgTCPConnectTimeout`) before the direct dial — previously a dead cached IP could burn up to ~17s of ladder probes before the stale retry.
- **mTLS-port-first + org-cert-first direct dial** (`dialAgentLKG`): skips the plaintext-port ladder step and, for multi-org users, the ~1-2s-per-wrong-cert device-side handshakes (`rotateCertsForOrg`).
- **Endpoint-faithful write-back**: `cacheConnectSuccess` now stores the actually-dialed port + `MTLS` + observed `OrgID` (new `AgentConnection.Addr`), fixing the base's clobbering of discovery's advertised mTLS port with 50051.

Trust unchanged: the cache is routing only — same certs, verifiers, pins; every failure falls through to the existing resolution + ladder + stale-retry flow. Composes with #1612's session resumption once both merge (same address ⇒ same ticket).

Spec: `specs/2026-08-08-lkg-connect-cache-design.md`

## Verification
- [x] Unit + connect-flow integration tests (any-age zero-resolution, dead-IP fall-through, non-mTLS skip, rotation, write-back fidelity)
- [ ] On-device: with a >1h-old cache entry, `wendy device info --device <name>.local` skips resolution; dead-IP fallback ≤1s + normal retry

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Update project memory** with the PR number and the on-device gate.

---

## Self-Review Notes

- **Spec coverage:** §1 any-age → Task 1; §2 direct dial/rotation/TCP bound/debug lines → Tasks 2-3; §3 write-back fidelity → Task 4; §4 trust (no changes — enforced by reusing the ladder verbatim); §5/§6 → Task 5 tests + Task 6's PR gate.
- **Seam inventory relied on:** existing on base — `deviceCacheLoadFn`, `dialAgentLadderFn`, `cacheFastPathReachableFn`, `osLookupHostFn`, `lanBrowseFn`; added here — `dialAgentLadderWithCertsFn`, `dialAgentLKGFn`, `tcpDialTimeoutFn`, `loadAllCLICertsFn`. Implementers must verify each existing seam's exact name/signature in `helpers.go` before use.
- **Known accepted behavior:** after an LKG failure the fromCache ladder may re-attempt the same mTLS endpoint once (redundant handshake in failure paths only); plaintext-downgrade during LKG closes the connection and falls through.
