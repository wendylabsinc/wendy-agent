# Public-Port Exposure Warning — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Warn in the agent log, on the monitor's periodic tick, when a running host-networking app is actually listening on a publicly-reachable address — deduped per exposure.

**Architecture:** A pure classification core (host-network predicate + public-bind predicate + collect/dedup) plus a thin containerd I/O wrapper `WarnPubliclyExposedPorts` on `*Client` that enumerates running containers, reads their entitlement labels, calls the existing `GetListeningPorts`, and logs new exposures. The container monitor invokes it each tick via a new optional capability interface (`PortExposureProber`), mirroring the existing `GroupRestarter`/`AppStateRebuilder` pattern so the large `ContainerdClient` interface and its mocks stay untouched.

**Tech Stack:** Go, containerd client, `net/netip`, zap logging, Go `testing`.

## Global Constraints

- Design doc: `specs/2026-07-05-mesh-port-exposure-warning-design.md`.
- Run all go commands from `/Users/joannisorlandos/git/wendy/wendyos-mesh/go`.
- Warn ONLY for host-networking apps: a `network` entitlement with mode `host`, `host-admin`, or omitted (`""`). This set is exactly `hasHostNetworkEntitlement`'s existing rule (`client.go:1725-1733`) — reuse it, do not re-derive. Isolated/mesh apps must NOT be warned (their non-loopback binds are on a private bridge, not the public LAN).
- "Publicly bound" = a listening socket whose bind address is non-loopback (a wildcard `0.0.0.0`/`::`, or a specific non-loopback interface IP). Loopback (`127.0.0.0/8`, `::1`) is private; an empty/unparseable address is NOT warned.
- Best-effort / fail-open: the probe never returns an error, never affects the monitor tick or container lifecycle; per-app read failures are skipped.
- Dedup: warn once per `(appID, protocol, port, address)` until it changes; a disappeared exposure is pruned so a later reappearance warns again.
- Do NOT change the default network behavior or any port-publishing code — this is advisory logging only.
- Commit messages end with:
  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq
  ```

---

### Task 1: Pure classification core (predicates + collect/dedup)

**Files:**
- Modify: `internal/agent/containerd/client.go` (add `entitlementsUseHostNetwork`; refactor `hasHostNetworkEntitlement` to delegate; add `isPubliclyBoundAddress`, `exposedPort`, `exposureKey`, `collectExposures`)
- Test: `internal/agent/containerd/client_test.go`

**Interfaces:**
- Consumes: `appconfig.Entitlement`, `appconfig.EntitlementNetwork`, `agentpb.PortEntry` (fields `Protocol string`, `Port uint32`, `Address string`).
- Produces:
  - `func entitlementsUseHostNetwork(ents []appconfig.Entitlement) bool`
  - `func isPubliclyBoundAddress(addr string) bool`
  - `type exposedPort struct { appID, protocol string; port uint32; address string }`
  - `func exposureKey(e exposedPort) string`
  - `func collectExposures(portsByApp map[string][]*agentpb.PortEntry) map[string]exposedPort`

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/containerd/client_test.go` (package `containerd`; ensure imports include `appconfig` and `agentpb` — they are already used in this test file):

```go
func TestEntitlementsUseHostNetwork(t *testing.T) {
	host := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "host"}}
	hostAdmin := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "host-admin"}}
	omitted := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: ""}}
	mesh := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "mesh"}}
	none := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "none"}}
	noNet := []appconfig.Entitlement{{Type: appconfig.EntitlementGPU}}

	for _, tc := range []struct {
		name string
		ents []appconfig.Entitlement
		want bool
	}{
		{"host", host, true},
		{"host-admin", hostAdmin, true},
		{"omitted", omitted, true},
		{"mesh", mesh, false},
		{"none", none, false},
		{"no network entitlement", noNet, false},
	} {
		if got := entitlementsUseHostNetwork(tc.ents); got != tc.want {
			t.Errorf("%s: entitlementsUseHostNetwork = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsPubliclyBoundAddress(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"0.0.0.0", true},          // IPv4 wildcard = all interfaces
		{"::", true},               // IPv6 wildcard
		{"192.168.1.10", true},     // specific non-loopback
		{"127.0.0.1", false},       // IPv4 loopback
		{"127.0.0.53", false},      // loopback range
		{"::1", false},             // IPv6 loopback
		{"", false},                // empty
		{"garbage", false},         // unparseable
	} {
		if got := isPubliclyBoundAddress(tc.addr); got != tc.want {
			t.Errorf("isPubliclyBoundAddress(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestCollectExposures(t *testing.T) {
	portsByApp := map[string][]*agentpb.PortEntry{
		"web": {
			{Protocol: "tcp", Port: 8080, Address: "0.0.0.0"},   // public
			{Protocol: "tcp", Port: 9000, Address: "127.0.0.1"}, // private, skipped
		},
		"api": {
			{Protocol: "tcp", Port: 443, Address: "192.168.1.5"}, // public
		},
	}
	got := collectExposures(portsByApp)
	if len(got) != 2 {
		t.Fatalf("expected 2 exposures, got %d: %v", len(got), got)
	}
	if _, ok := got[exposureKey(exposedPort{appID: "web", protocol: "tcp", port: 8080, address: "0.0.0.0"})]; !ok {
		t.Error("web:8080/0.0.0.0 should be an exposure")
	}
	if _, ok := got[exposureKey(exposedPort{appID: "api", protocol: "tcp", port: 443, address: "192.168.1.5"})]; !ok {
		t.Error("api:443/192.168.1.5 should be an exposure")
	}
	for k := range got {
		if strings.Contains(k, "9000") {
			t.Errorf("loopback port 9000 must not be an exposure (key %q)", k)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/containerd/ -run 'TestEntitlementsUseHostNetwork|TestIsPubliclyBoundAddress|TestCollectExposures' -v`
Expected: compile failure — the new functions/types are undefined.

- [ ] **Step 3: Refactor `hasHostNetworkEntitlement` and add the predicates**

In `internal/agent/containerd/client.go`, replace the existing `hasHostNetworkEntitlement` (lines 1725-1733) with a delegating version plus the new slice predicate:

```go
func hasHostNetworkEntitlement(appCfg *appconfig.AppConfig) bool {
	return entitlementsUseHostNetwork(appCfg.Entitlements)
}

// entitlementsUseHostNetwork reports whether the entitlements put the container
// on the HOST network namespace — a network entitlement with mode host,
// host-admin, or omitted (empty), matching applyNetwork's host-netns selection.
// Such a container's non-loopback listening ports are reachable on the device's
// real interfaces.
func entitlementsUseHostNetwork(ents []appconfig.Entitlement) bool {
	for _, e := range ents {
		if e.Type == appconfig.EntitlementNetwork && (e.Mode == "host" || e.Mode == "host-admin" || e.Mode == "") {
			return true
		}
	}
	return false
}
```

Then add the exposure helpers (place them near `RebuildAppStateCaches` / the other cache helpers, e.g. after `rebuildCachesFromLabels`). Add `"net/netip"` to the `client.go` import block if not already present:

```go
// isPubliclyBoundAddress reports whether a listening socket's bind address is
// reachable from outside the host — a wildcard (0.0.0.0 / ::) or a specific
// non-loopback interface address. Loopback (127.0.0.0/8, ::1) is private; an
// empty or unparseable address is treated as not-public (we only warn on a
// definite exposure).
func isPubliclyBoundAddress(addr string) bool {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	return !a.IsLoopback()
}

// exposedPort identifies one publicly-bound listening socket of an app, used as
// the dedup unit for exposure warnings.
type exposedPort struct {
	appID    string
	protocol string
	port     uint32
	address  string
}

// exposureKey is the stable dedup key for an exposedPort.
func exposureKey(e exposedPort) string {
	return fmt.Sprintf("%s|%s|%d|%s", e.appID, e.protocol, e.port, e.address)
}

// collectExposures returns the publicly-bound listening sockets across the given
// host-network apps, keyed by exposureKey. Pure (no containerd, no lock) so it
// is unit-testable; the caller supplies each host-network app's listening ports.
func collectExposures(portsByApp map[string][]*agentpb.PortEntry) map[string]exposedPort {
	out := make(map[string]exposedPort)
	for appID, ports := range portsByApp {
		for _, p := range ports {
			if !isPubliclyBoundAddress(p.Address) {
				continue
			}
			e := exposedPort{appID: appID, protocol: p.Protocol, port: p.Port, address: p.Address}
			out[exposureKey(e)] = e
		}
	}
	return out
}
```

(`fmt` is already imported in `client.go`; `netip` may need adding.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/containerd/ -run 'TestEntitlementsUseHostNetwork|TestIsPubliclyBoundAddress|TestCollectExposures' -v && go build ./...`
Expected: PASS; module builds (confirms the `hasHostNetworkEntitlement` refactor didn't break its caller).

- [ ] **Step 5: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-mesh
git add go/internal/agent/containerd/client.go go/internal/agent/containerd/client_test.go
git commit -m "feat(agent): add public-port exposure classification helpers

$(printf 'Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq')"
```

---

### Task 2: Probe method + monitor hook

**Files:**
- Modify: `internal/agent/containerd/client.go` (add `warnedExposures` field to `Client` struct + init; add `WarnPubliclyExposedPorts` method)
- Modify: `internal/agent/services/interfaces.go` (add `PortExposureProber` interface)
- Modify: `internal/agent/container/monitor.go` (call the prober on each tick)
- Test: `internal/agent/container/monitor_checkcontainers_test.go`

**Interfaces:**
- Consumes: `entitlementsUseHostNetwork`, `collectExposures`, `exposedPort`, `exposureKey` (Task 1); existing `c.client.Containers`, `c.withNamespace`, `c.containerIsRunning`, `c.GetListeningPorts`, `parseEntitlementsFromAnnotations`, `labelKeyAppID`.
- Produces:
  - `func (c *Client) WarnPubliclyExposedPorts(ctx context.Context)`
  - `type PortExposureProber interface { WarnPubliclyExposedPorts(ctx context.Context) }`

- [ ] **Step 1: Write the failing test (monitor hook)**

The `WarnPubliclyExposedPorts` containerd I/O is not unit-testable (concrete `c.client`); Task 1's `collectExposures` carries the classification coverage. This step verifies the monitor invokes the prober.

Add to `fakeContainerd` in `internal/agent/container/monitor_checkcontainers_test.go`: a field `probeCalls int` (next to the other counters) and a method:

```go
func (f *fakeContainerd) WarnPubliclyExposedPorts(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probeCalls++
}
```

Then add the test:

```go
func TestProbeExposedPortsInvokesProber(t *testing.T) {
	f := &fakeContainerd{}
	m := newMonitorWithClient(f)
	m.probeExposedPorts(context.Background())

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.probeCalls != 1 {
		t.Fatalf("WarnPubliclyExposedPorts called %d times, want 1", f.probeCalls)
	}
}
```

(`newMonitorWithClient` is the existing test helper used by the boot-reconcile tests in this file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/container/ -run TestProbeExposedPortsInvokesProber -v`
Expected: FAIL — `m.probeExposedPorts` undefined.

- [ ] **Step 3: Add the `PortExposureProber` interface**

In `internal/agent/services/interfaces.go`, next to `GroupRestarter` / `AppStateRebuilder`:

```go
// PortExposureProber is the optional capability to scan running host-network
// apps for publicly-bound listening ports and log a warning for each new
// exposure. The container monitor calls it once per health tick; the
// implementation dedups so a given exposure is logged once. Kept separate from
// ContainerdClient so the large interface and its mocks stay untouched
// (mirrors GroupRestarter / AppStateRebuilder).
type PortExposureProber interface {
	WarnPubliclyExposedPorts(ctx context.Context)
}
```

- [ ] **Step 4: Add the monitor hook**

In `internal/agent/container/monitor.go`, add a helper method and call it from the `Run` tick. Add the method:

```go
// probeExposedPorts asks the containerd client (if it supports it) to warn
// about publicly-bound ports on running host-network apps. Optional capability,
// mirroring the AppStateRebuilder hook.
func (m *ContainerMonitor) probeExposedPorts(ctx context.Context) {
	if p, ok := m.containerd.(services.PortExposureProber); ok {
		p.WarnPubliclyExposedPorts(ctx)
	}
}
```

Then call it in `Run`'s ticker case, right after `m.checkContainers(ctx)`:

```go
		case <-ticker.C:
			m.checkContainers(ctx)
			m.probeExposedPorts(ctx)
```

(`services` is already imported in `monitor.go` for `GroupRestarter`.)

- [ ] **Step 5: Run the monitor test to verify it passes**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/container/ -run TestProbeExposedPortsInvokesProber -v`
Expected: PASS.

- [ ] **Step 6: Implement `WarnPubliclyExposedPorts` + dedup state on `Client`**

In `internal/agent/containerd/client.go`, add the dedup field to the `Client` struct (next to `appIsolation` around line 72):

```go
	// warnedExposures dedups public-port exposure warnings, keyed by
	// exposureKey. Protected by mu. Rebuilt each probe so a vanished exposure
	// is pruned (and re-warned if it returns).
	warnedExposures map[string]struct{}
```

Add the method (near `RebuildAppStateCaches`):

```go
// WarnPubliclyExposedPorts scans running host-network apps and logs a WARN for
// each newly-observed publicly-bound listening port, so operators notice a
// service exposed on the device's real interfaces. Best-effort: any failure
// logs and returns without affecting the caller. Deduped per
// (appID, protocol, port, address); a vanished exposure is pruned so it warns
// again if it reappears.
func (c *Client) WarnPubliclyExposedPorts(ctx context.Context) {
	ctx = c.withNamespace(ctx)
	ctrs, err := c.client.Containers(ctx, fmt.Sprintf("labels.%q", labelKeyAppID))
	if err != nil {
		c.logger.Warn("port-exposure probe: listing containers failed", zap.Error(err))
		return
	}

	// Gather unique host-network appIDs among running containers (outside the lock).
	hostNetApps := make(map[string]struct{})
	for _, ctr := range ctrs {
		if !c.containerIsRunning(ctx, ctr) {
			continue
		}
		info, infoErr := ctr.Info(ctx)
		if infoErr != nil {
			c.logger.Warn("port-exposure probe: reading container info failed",
				zap.String("id", ctr.ID()), zap.Error(infoErr))
			continue
		}
		appID := info.Labels[labelKeyAppID]
		if appID == "" {
			continue
		}
		if entitlementsUseHostNetwork(parseEntitlementsFromAnnotations(info.Labels)) {
			hostNetApps[appID] = struct{}{}
		}
	}

	// Read each host-network app's listening ports (outside the lock).
	portsByApp := make(map[string][]*agentpb.PortEntry, len(hostNetApps))
	for appID := range hostNetApps {
		ports, portErr := c.GetListeningPorts(ctx, appID)
		if portErr != nil {
			c.logger.Warn("port-exposure probe: reading listening ports failed",
				zap.String(logfields.AppID, appID), zap.Error(portErr))
			continue
		}
		portsByApp[appID] = ports
	}

	current := collectExposures(portsByApp)

	c.mu.Lock()
	defer c.mu.Unlock()
	for key, e := range current {
		if _, warned := c.warnedExposures[key]; warned {
			continue
		}
		c.logger.Warn("app is listening on a publicly reachable address; the port is exposed on the device's network (network mode: host). For private cross-device access, use a \"mesh\" network entitlement.",
			zap.String(logfields.AppID, e.appID),
			zap.String("protocol", e.protocol),
			zap.Uint32("port", e.port),
			zap.String("bind_address", e.address))
	}
	c.warnedExposures = current
}
```

Ensure `logfields` and `zap` are imported in `client.go` (they are — used elsewhere in the file). If the `Client` struct is constructed with a struct literal that initializes the other maps (search `appIsolation:` in the constructor), a nil `warnedExposures` is fine because the method assigns it wholesale; no constructor change is required.

- [ ] **Step 7: Run tests + build + vet**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go build ./... && go vet ./internal/agent/... && go test ./internal/agent/containerd/ ./internal/agent/container/ ./internal/agent/services/`
Expected: PASS across all three packages; module builds. (Confirms the interface addition and monitor change didn't break existing mocks/consumers.)

- [ ] **Step 8: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-mesh
git add go/internal/agent/containerd/client.go go/internal/agent/services/interfaces.go go/internal/agent/container/monitor.go go/internal/agent/container/monitor_checkcontainers_test.go
git commit -m "feat(agent): warn on publicly-exposed host-network ports each monitor tick

$(printf 'Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq')"
```

---

### Task 3: Docs — port exposure model

**Files:**
- Modify: `go/internal/cli/assets/docs/apps/wendy.json.md` (network entitlement section, ~lines 186-202)

**Interfaces:** none (docs only).

- [ ] **Step 1: Read the current network section**

Run: `sed -n '184,205p' /Users/joannisorlandos/git/wendy/wendyos-mesh/go/internal/cli/assets/docs/apps/wendy.json.md`
Expected: shows the `network` heading and the mode table with rows `*(omitted)*`, `"host"`, `"host-admin"`, and a security note.

- [ ] **Step 2: Correct the mode table and add a Port exposure note**

In the `network` section of `go/internal/cli/assets/docs/apps/wendy.json.md`:

- Change the `*(omitted)*` row so it no longer says "Default isolated network". Replace its description with: `Host networking (same as \`"host"\`) — the container binds directly on the device's network interfaces, so its ports are reachable from the LAN.`
- Add a `"mesh"` row to the mode table: `Isolated network namespace; ports are private and reachable from other devices in the org by name (\`device-<id>.cloud.wendy.dev\`) via the mesh, not from the LAN directly.`
- Immediately after the mode table (before or after the existing security note), add:

```markdown
> **Port exposure:** With `host` / `host-admin` (and the current omitted default), a port your app binds on `0.0.0.0` is reachable from the device's network (LAN). With `"mesh"`, ports stay private (loopback + cross-device mesh). The agent logs a `WARN` when a host-network app is listening on a public address, so you can spot unintended exposure in `wendy device logs`.
```

(If the repo also documents `mode: "bridge"` by the time this lands — a separate PR adds it — leave that row as-is; do not remove it.)

- [ ] **Step 3: Verify the doc renders as intended (spot check)**

Run: `sed -n '184,212p' /Users/joannisorlandos/git/wendy/wendyos-mesh/go/internal/cli/assets/docs/apps/wendy.json.md`
Expected: the omitted row now describes host networking, a `"mesh"` row exists, and the Port exposure note is present.

- [ ] **Step 4: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-mesh
git add go/internal/cli/assets/docs/apps/wendy.json.md
git commit -m "docs(wendy.json): document port exposure model and mesh mode

$(printf 'Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq')"
```

---

## Self-Review

**Spec coverage:**
- Host-network classification reusing the existing predicate → Task 1 (`entitlementsUseHostNetwork`, `hasHostNetworkEntitlement` delegates).
- Public-bind classification (wildcard/specific non-loopback = public; loopback/empty = not) → Task 1 (`isPubliclyBoundAddress`).
- Behavioral probe via `GetListeningPorts`, host-mode only, isolated/mesh skipped → Task 2 (`WarnPubliclyExposedPorts` filters on `entitlementsUseHostNetwork`).
- Runs on the monitor's periodic tick → Task 2 (`probeExposedPorts` in `Run`).
- Dedup per (app,proto,port,addr); vanished exposure pruned → Task 1 (`exposureKey`, `collectExposures`) + Task 2 (`warnedExposures` diff, wholesale reassign).
- Best-effort / fail-open → Task 2 (`WarnPubliclyExposedPorts` returns nothing, per-app skip).
- Optional interface, not added to `ContainerdClient` → Task 2 (`PortExposureProber`).
- Docs: fix omitted-mode claim, add mesh mode, port-exposure note → Task 3.

**Placeholder scan:** none — every code/test/doc step has concrete content.

**Type consistency:** `entitlementsUseHostNetwork([]appconfig.Entitlement) bool`, `isPubliclyBoundAddress(string) bool`, `exposedPort{appID,protocol string; port uint32; address string}`, `exposureKey(exposedPort) string`, `collectExposures(map[string][]*agentpb.PortEntry) map[string]exposedPort` (Task 1) are used with matching signatures in `WarnPubliclyExposedPorts` and the tests (Task 2). `PortExposureProber.WarnPubliclyExposedPorts(ctx context.Context)` matches the `*Client` method and the fake (Task 2). `PortEntry` fields (`Protocol`, `Port uint32`, `Address`) match the proto (`wendy_agent_v1_container_service.pb.go:2490-2492`).

**Note on concurrent branches:** Task 2 edits `client.go` (`WarnPubliclyExposedPorts`, `warnedExposures`), `monitor.go`, and `interfaces.go`, which the separate `jo/network-bridge-mode` branch (PR #1362) also touches in `client.go`/`interfaces.go`. Both target `jo/mesh-foundation`; a merge reconciliation between the two branches in `client.go` is expected and normal — not a blocker for authoring this on `jo/mesh-foundation`.
