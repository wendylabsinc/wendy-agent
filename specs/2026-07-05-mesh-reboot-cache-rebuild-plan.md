# Mesh Reboot-Gap Cache Rebuild — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After an agent restart, rebuild the in-memory `appIsolation` / `appServices` caches from persisted container labels so isolated/meshed containers regain CNI networking, `/etc/hosts`, and mesh egress on reboot without an `wendy run` re-create.

**Architecture:** Persist the two currently-unrecoverable data points (isolation mode, per-service `dependsOn`) as container labels at create time. Add a pure, containerd-free reconstruction core plus a thin I/O wrapper (`RebuildAppStateCaches`) on the containerd `Client`. Invoke it once at startup from `ReconcileBootContainers` via a new optional capability interface (`AppStateRebuilder`), mirroring the existing `GroupRestarter` pattern so the large `ContainerdClient` interface and its mocks stay untouched.

**Tech Stack:** Go, containerd client, zap logging, Go standard `testing`.

## Global Constraints

- Design doc: `specs/2026-07-05-mesh-reboot-cache-rebuild-design.md`.
- Working dir for all `go` commands: `/Users/joannisorlandos/git/wendy/wendyos-mesh/go`.
- Label key names (verbatim): isolation = `sh.wendy/isolation`, depends-on = `sh.wendy/depends-on`. Follow the existing `sh.wendy/*` convention in `helpers.go`.
- Best-effort, fail-open: rebuild must never abort boot recovery; per-container read failures log a warning and continue; a missing isolation label means "non-isolated" (today's default).
- `primaryPIDs` is NOT persisted or rebuilt — it is re-derived at runtime by `StartContainer` + the existing `primaryTaskAlive` staleness check.
- Do not persist build-time `ServiceConfig` fields (Context, Env, Resources, Frameworks) — only `DependsOn` is read after create.
- Commit messages end with the repo's Co-Authored-By / Claude-Session trailers (match existing history).

---

### Task 1: Persist isolation + depends-on as container labels

**Files:**
- Modify: `internal/agent/containerd/helpers.go` (label key consts near lines 60–103; `wendyLabels` at line 245; add `parseDependsOn`)
- Modify: `internal/agent/containerd/client.go:828` (the sole `wendyLabels` caller)
- Test: `internal/agent/containerd/helpers_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `const labelKeyIsolation = "sh.wendy/isolation"`, `const labelKeyDependsOn = "sh.wendy/depends-on"`
  - `func wendyLabels(appName, serviceName, version string, restartPolicy *agentpb.RestartPolicy, entitlements []appconfig.Entitlement, isolation string, dependsOn []string) map[string]string`
  - `func parseDependsOn(v string) []string`

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/containerd/helpers_test.go`:

```go
func TestWendyLabels_IsolationAndDependsOn(t *testing.T) {
	labels := wendyLabels("myapp", "web", "1.0.0", nil, nil, "isolated", []string{"db", "cache"})
	if got := labels[labelKeyIsolation]; got != "isolated" {
		t.Fatalf("isolation label = %q, want %q", got, "isolated")
	}
	if got := labels[labelKeyDependsOn]; got != "db,cache" {
		t.Fatalf("depends-on label = %q, want %q", got, "db,cache")
	}
}

func TestWendyLabels_OmitsWhenEmpty(t *testing.T) {
	labels := wendyLabels("myapp", "", "1.0.0", nil, nil, "", nil)
	if _, ok := labels[labelKeyIsolation]; ok {
		t.Fatal("isolation label should be absent when isolation is empty")
	}
	if _, ok := labels[labelKeyDependsOn]; ok {
		t.Fatal("depends-on label should be absent when dependsOn is empty")
	}
}

func TestParseDependsOn(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"db", []string{"db"}},
		{"db,cache", []string{"db", "cache"}},
		{"db,,cache", []string{"db", "cache"}}, // tolerate stray empties
	}
	for _, tc := range cases {
		got := parseDependsOn(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("parseDependsOn(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseDependsOn(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/containerd/ -run 'TestWendyLabels_IsolationAndDependsOn|TestWendyLabels_OmitsWhenEmpty|TestParseDependsOn' -v`
Expected: compile failure — `wendyLabels` takes too few args / `parseDependsOn` and the consts are undefined.

- [ ] **Step 3: Add the label key constants**

In `internal/agent/containerd/helpers.go`, next to the other `labelKey*` consts (after `labelKeyServiceName` around line 82):

```go
// labelKeyIsolation persists the app's namespace isolation mode so the agent
// can rebuild appIsolation after a restart (the in-memory cache is otherwise
// only written at container-create time). Absent means non-isolated.
const labelKeyIsolation = "sh.wendy/isolation"

// labelKeyDependsOn persists a service's dependsOn list (comma-separated) so
// appServices can be rebuilt after a restart for shared-namespace stop-order /
// group restart. Absent means no declared dependencies.
const labelKeyDependsOn = "sh.wendy/depends-on"
```

- [ ] **Step 4: Extend `wendyLabels` and add `parseDependsOn`**

In `internal/agent/containerd/helpers.go`, change the `wendyLabels` signature and body (line 245):

```go
func wendyLabels(appName, serviceName, version string, restartPolicy *agentpb.RestartPolicy, entitlements []appconfig.Entitlement, isolation string, dependsOn []string) map[string]string {
	labels := map[string]string{
		labelKeyAppVersion: version,
		labelKeyAppID:      appName,
	}

	if serviceName != "" {
		labels[labelKeyServiceName] = serviceName
	}

	if isolation != "" {
		labels[labelKeyIsolation] = isolation
	}

	if len(dependsOn) > 0 {
		labels[labelKeyDependsOn] = strings.Join(dependsOn, ",")
	}

	if restartPolicy != nil {
		policyStr := restartPolicyToLabel(restartPolicy)
		if policyStr != "" {
			labels[labelKeyRestartPolicy] = policyStr
		}
	}

	for _, e := range entitlements {
		if e.Type == appconfig.EntitlementMCP && e.Port > 0 {
			labels[labelKeyMCPPort] = strconv.FormatUint(uint64(e.Port), 10)
			break
		}
	}

	for k, v := range appconfig.BuildEntitlementAnnotations(entitlements) {
		labels[k] = v
	}

	return labels
}

// parseDependsOn decodes the comma-separated labelKeyDependsOn value back into
// a service list, tolerating and dropping any stray empty entries. It is the
// inverse of the strings.Join in wendyLabels.
func parseDependsOn(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

(`strings` is already imported in `helpers.go` — used by `parseEntitlementsFromAnnotations`.)

- [ ] **Step 5: Update the `wendyLabels` caller**

In `internal/agent/containerd/client.go`, replace the `labels := wendyLabels(...)` line at 828 with:

```go
	// Persist isolation + this service's dependsOn so appIsolation/appServices
	// can be rebuilt after an agent restart (RebuildAppStateCaches). Single-
	// service apps have no Services entry, so dependsOn stays nil.
	var dependsOn []string
	if serviceName != "" && appCfg.Services != nil {
		if sc := appCfg.Services[serviceName]; sc != nil {
			dependsOn = sc.DependsOn
		}
	}
	labels := wendyLabels(appID, serviceName, version, req.GetRestartPolicy(), appCfg.Entitlements, appCfg.Isolation, dependsOn)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/containerd/ -run 'TestWendyLabels_IsolationAndDependsOn|TestWendyLabels_OmitsWhenEmpty|TestParseDependsOn' -v && go build ./...`
Expected: PASS, and the whole module still builds (confirms the caller update compiles).

- [ ] **Step 7: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-mesh
git add go/internal/agent/containerd/helpers.go go/internal/agent/containerd/helpers_test.go go/internal/agent/containerd/client.go
git commit -m "feat(agent): persist isolation + dependsOn as container labels

$(printf 'Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq')"
```

---

### Task 2: Pure cache-reconstruction core

**Files:**
- Modify: `internal/agent/containerd/client.go` (add `rebuildCachesFromLabels`)
- Test: `internal/agent/containerd/client_test.go`

**Interfaces:**
- Consumes: `labelKeyAppID`, `labelKeyServiceName`, `labelKeyIsolation`, `labelKeyDependsOn`, `parseDependsOn` (Task 1).
- Produces:
  - `func rebuildCachesFromLabels(containerLabels []map[string]string) (isolation map[string]string, servicesByApp map[string]map[string]*appconfig.ServiceConfig)`

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/containerd/client_test.go`:

```go
func TestRebuildCachesFromLabels(t *testing.T) {
	labels := []map[string]string{
		// isolated single-service app
		{labelKeyAppID: "cam", labelKeyServiceName: "cam", labelKeyIsolation: "isolated"},
		// shared-namespace group: two services, one with a dependency
		{labelKeyAppID: "stack", labelKeyServiceName: "web", labelKeyIsolation: "shared-network", labelKeyDependsOn: "db"},
		{labelKeyAppID: "stack", labelKeyServiceName: "db", labelKeyIsolation: "shared-network"},
		// non-isolated app: no isolation label
		{labelKeyAppID: "plain", labelKeyServiceName: "plain"},
		// junk row with no appID is ignored
		{labelKeyServiceName: "orphan"},
	}

	isolation, services := rebuildCachesFromLabels(labels)

	if isolation["cam"] != "isolated" {
		t.Fatalf("cam isolation = %q, want isolated", isolation["cam"])
	}
	if isolation["stack"] != "shared-network" {
		t.Fatalf("stack isolation = %q, want shared-network", isolation["stack"])
	}
	if _, ok := isolation["plain"]; ok {
		t.Fatal("plain must not have an isolation entry")
	}
	if len(services["stack"]) != 2 {
		t.Fatalf("stack services = %d, want 2", len(services["stack"]))
	}
	web := services["stack"]["web"]
	if web == nil || len(web.DependsOn) != 1 || web.DependsOn[0] != "db" {
		t.Fatalf("stack/web dependsOn = %+v, want [db]", web)
	}
	if db := services["stack"]["db"]; db == nil || len(db.DependsOn) != 0 {
		t.Fatalf("stack/db dependsOn = %+v, want empty", db)
	}
	if _, ok := services[""]; ok {
		t.Fatal("orphan row (no appID) must be ignored")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/containerd/ -run TestRebuildCachesFromLabels -v`
Expected: compile failure — `rebuildCachesFromLabels` undefined.

- [ ] **Step 3: Implement `rebuildCachesFromLabels`**

Add to `internal/agent/containerd/client.go` (near the other cache helpers, e.g. after `getIsolation` around line 235):

```go
// rebuildCachesFromLabels reconstructs the appIsolation and appServices caches
// from a list of per-container label maps. Pure (no containerd calls, no lock)
// so it is unit-testable without a live containerd, mirroring the
// containerd-free split used for mesh resolv.conf recreation. A container with
// no appID label is skipped; a blank isolation label yields no isolation entry
// (non-isolated, the default). Only DependsOn is reconstructed for services —
// it is the sole ServiceConfig field read after create (len + ServiceTopoOrder).
func rebuildCachesFromLabels(containerLabels []map[string]string) (
	isolation map[string]string,
	servicesByApp map[string]map[string]*appconfig.ServiceConfig,
) {
	isolation = make(map[string]string)
	servicesByApp = make(map[string]map[string]*appconfig.ServiceConfig)
	for _, labels := range containerLabels {
		appID := labels[labelKeyAppID]
		if appID == "" {
			continue
		}
		if iso := labels[labelKeyIsolation]; iso != "" {
			isolation[appID] = iso
		}
		if svc := labels[labelKeyServiceName]; svc != "" {
			if servicesByApp[appID] == nil {
				servicesByApp[appID] = make(map[string]*appconfig.ServiceConfig)
			}
			servicesByApp[appID][svc] = &appconfig.ServiceConfig{
				DependsOn: parseDependsOn(labels[labelKeyDependsOn]),
			}
		}
	}
	return isolation, servicesByApp
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/containerd/ -run TestRebuildCachesFromLabels -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-mesh
git add go/internal/agent/containerd/client.go go/internal/agent/containerd/client_test.go
git commit -m "feat(agent): add pure cache-reconstruction core for reboot recovery

$(printf 'Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq')"
```

---

### Task 3: `RebuildAppStateCaches` method + startup hook

**Files:**
- Modify: `internal/agent/containerd/client.go` (add `RebuildAppStateCaches` method)
- Modify: `internal/agent/services/interfaces.go` (add `AppStateRebuilder` interface)
- Modify: `internal/agent/container/monitor.go:150` (call at top of `ReconcileBootContainers`)
- Test: `internal/agent/container/monitor_checkcontainers_test.go`

**Interfaces:**
- Consumes: `rebuildCachesFromLabels` (Task 2); `c.client.Containers`, `c.withNamespace`, `labelKeyAppID` (existing).
- Produces:
  - `func (c *Client) RebuildAppStateCaches(ctx context.Context)`
  - `type AppStateRebuilder interface { RebuildAppStateCaches(ctx context.Context) }`

- [ ] **Step 1: Write the failing test**

The concrete `c.client` (`*containerd.Client`) cannot be faked, so the method's containerd I/O is not unit-tested directly; the pure core (Task 2) carries the reconstruction coverage. This step tests that `ReconcileBootContainers` fires the rebuild hook before restarting containers.

First, make the test fake satisfy the new optional interface. Add to `fakeContainerd` in `internal/agent/container/monitor_checkcontainers_test.go` (alongside `migrateCalls`):

```go
func (f *fakeContainerd) RebuildAppStateCaches(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rebuildCalls++
}
```

Add the field to the `fakeContainerd` struct (next to `migrateCalls  int`):

```go
	rebuildCalls int
```

Then add the test:

```go
func TestReconcileBootContainers_RebuildsCaches(t *testing.T) {
	f := &fakeContainerd{}
	m := newMonitorWithClient(f)
	m.ReconcileBootContainers(context.Background())

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rebuildCalls != 1 {
		t.Fatalf("RebuildAppStateCaches called %d times, want 1", f.rebuildCalls)
	}
}
```

(`newMonitorWithClient` is the existing test helper used by the neighbouring boot-reconcile tests in this file, e.g. `TestReconcileBootContainers_NothingToDo`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/container/ -run TestReconcileBootContainers_RebuildsCaches -v`
Expected: FAIL — `rebuildCalls` is 0 (the hook doesn't exist yet).

- [ ] **Step 3: Add the `AppStateRebuilder` optional interface**

In `internal/agent/services/interfaces.go`, next to the `GroupRestarter` interface:

```go
// AppStateRebuilder is the optional capability a ContainerdClient may provide to
// rebuild its in-memory per-app caches (isolation mode + service graph) from
// persisted container labels. ReconcileBootContainers type-asserts for it and
// calls it before listing boot containers, so the caches are warm before any
// StartContainer runs (an empty appIsolation after a reboot would otherwise make
// StartContainer skip CNI networking + mesh egress for isolated apps). Kept
// separate from ContainerdClient so the large interface and its mocks stay
// untouched, mirroring GroupRestarter.
type AppStateRebuilder interface {
	RebuildAppStateCaches(ctx context.Context)
}
```

- [ ] **Step 4: Implement `RebuildAppStateCaches` on `Client`**

Add to `internal/agent/containerd/client.go` (below `rebuildCachesFromLabels`):

```go
// RebuildAppStateCaches repopulates the appIsolation and appServices caches
// from persisted container labels. The maps are otherwise written only at
// container-create time, so after an agent restart (reboot) they start empty
// and StartContainer skips isolated-container wiring (CNI, /etc/hosts, mesh
// egress). Best-effort: any failure logs and returns without blocking boot
// recovery. Idempotent — merges into the caches, so it is safe to call more
// than once and never clobbers a concurrently-created live entry.
func (c *Client) RebuildAppStateCaches(ctx context.Context) {
	ctx = c.withNamespace(ctx)
	ctrs, err := c.client.Containers(ctx, fmt.Sprintf("labels.%q", labelKeyAppID))
	if err != nil {
		c.logger.Warn("rebuild app-state caches: listing containers failed", zap.Error(err))
		return
	}

	// Read labels outside the lock — Info() is containerd I/O.
	labelSets := make([]map[string]string, 0, len(ctrs))
	for _, ctr := range ctrs {
		info, infoErr := ctr.Info(ctx)
		if infoErr != nil {
			c.logger.Warn("rebuild app-state caches: reading container info failed",
				zap.String("id", ctr.ID()), zap.Error(infoErr))
			continue
		}
		labelSets = append(labelSets, info.Labels)
	}

	isolation, servicesByApp := rebuildCachesFromLabels(labelSets)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appIsolation == nil {
		c.appIsolation = make(map[string]string)
	}
	for appID, iso := range isolation {
		c.appIsolation[appID] = iso
	}
	if c.appServices == nil {
		c.appServices = make(map[string]map[string]*appconfig.ServiceConfig)
	}
	for appID, svcs := range servicesByApp {
		c.appServices[appID] = svcs
	}
	c.logger.Info("Rebuilt app-state caches from labels",
		zap.Int("apps_isolation", len(isolation)), zap.Int("apps_services", len(servicesByApp)))
}
```

- [ ] **Step 5: Call the hook from `ReconcileBootContainers`**

In `internal/agent/container/monitor.go`, at the top of `ReconcileBootContainers` (before the `MigrateStoppedByUserOnce` call at line 155):

```go
	// Warm the isolation/service caches from persisted labels before anything
	// starts a container: after a reboot these in-memory caches are empty, and
	// StartContainer would otherwise skip CNI networking + mesh egress for
	// isolated apps. Optional capability, mirroring GroupRestarter.
	if r, ok := m.containerd.(services.AppStateRebuilder); ok {
		r.RebuildAppStateCaches(ctx)
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/container/ ./internal/agent/containerd/ -run 'TestReconcileBootContainers|TestRebuildCachesFromLabels|TestWendyLabels|TestParseDependsOn' -v && go build ./...`
Expected: PASS across both packages; module builds.

- [ ] **Step 7: Full package regression + vet**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go vet ./internal/agent/... && go test ./internal/agent/containerd/ ./internal/agent/container/ ./internal/agent/services/`
Expected: PASS (confirms no existing `wendyLabels` caller or interface consumer broke).

- [ ] **Step 8: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-mesh
git add go/internal/agent/containerd/client.go go/internal/agent/services/interfaces.go go/internal/agent/container/monitor.go go/internal/agent/container/monitor_checkcontainers_test.go
git commit -m "feat(agent): rebuild isolation/service caches on boot reconcile

Closes the mesh reboot gap: meshed/isolated containers regain CNI
networking and mesh egress after a reboot without a wendy run re-create.

$(printf 'Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq')"
```

---

## Self-Review

**Spec coverage:**
- Persist `sh.wendy/isolation` + `sh.wendy/depends-on` labels → Task 1.
- `RebuildAppStateCaches` scans containers, rebuilds both maps → Tasks 2 (core) + 3 (I/O + wiring).
- Called at top of `ReconcileBootContainers` → Task 3 Step 5 (via `AppStateRebuilder` optional interface — a refinement of the design's "added to the containerd interface", chosen to avoid mock churn, mirroring the existing `GroupRestarter` capability pattern; behavior is identical to the spec).
- `primaryPIDs` not persisted → honored (no task touches it; re-derived at runtime).
- Build-time `ServiceConfig` fields not persisted → only `DependsOn` reconstructed (Task 2).
- Best-effort / fail-open → Task 3 Step 4 (warn + return; per-container continue).
- Backward compat (pre-existing containers → non-isolated) → covered by the "no appID / blank isolation" cases in Task 2's test and the fail-open default.

**Placeholder scan:** none — every code and test step is complete.

**Type consistency:** `wendyLabels` new params `(isolation string, dependsOn []string)` match caller (Task 1 Step 5). `rebuildCachesFromLabels` return types match their use in `RebuildAppStateCaches` (Task 3 Step 4). `AppStateRebuilder.RebuildAppStateCaches(ctx context.Context)` matches the `*Client` method and the fake's method (Task 3 Steps 1/3/4). `parseDependsOn` produced in Task 1, consumed in Task 2.
