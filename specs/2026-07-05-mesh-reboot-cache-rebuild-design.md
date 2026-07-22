# Reboot Gap: Rebuild Isolation/Service Caches After Restart — Design

Date: 2026-07-05
Status: Approved (design review with Joannis)
Builds on: `jo/mesh-foundation` (mesh data plane; see
`2026-07-02-mesh-data-plane-design.md` "Known limitation: reboot")

## Problem

The agent's `containerd.Client` keeps three in-memory maps keyed by appID:

- `appIsolation[appID]` — the app's namespace isolation mode (`""`,
  `"isolated"`, or a shared-namespace mode).
- `appServices[appID]` — the app's service configs, used for group stop-order
  and shared-namespace restarts.
- `primaryPIDs[appID]` — the live PID of the namespace-owning ("primary")
  container in a shared-namespace group.

`appIsolation` and `appServices` have a **single write site**:
`CreateContainerWithProgress` (`client.go:989-1000`). After any agent process
restart — a device reboot being the common case — containers survive (containerd
persists their definitions) but these maps start empty. Boot recovery runs
`ReconcileBootContainers → StartContainer`, *not* `CreateContainer`, so:

- `StartContainer` reads `isolation := c.getIsolation(appID)` and sees `""`
  (`client.go:1184`). The entire `isolation == "isolated"` block is skipped:
  **CNI ADD, `/etc/hosts` writing, and mesh egress wiring never run**
  (`client.go:1196-1217+`).
- Shared-namespace apps lose primary-PID tracking (`client.go:1185-1189`) and
  group-aware restart / stop-order (`GroupRestartAppID`, `RestartGroup`,
  `client.go:1849-1885`), because those read `appServices`/`appIsolation` too.

The observable symptom for the mesh: a meshed container comes back after reboot
with no CNI networking or mesh egress, so `device-N.cloud.wendy.dev` resolution
and peer dialing silently stop working until the app is re-created with
`wendy run` (which takes the full `CreateContainer` path). The root cause is
general — it affects **all** isolated containers, not just meshed ones.

## Approach

Persist the two data points that aren't already recoverable, then rebuild the
maps once at startup by scanning existing containers. This mirrors patterns the
codebase already uses for reboot resilience: entitlements round-trip through
container labels/annotations (`parseEntitlementsFromAnnotations`), and the mesh
resolv.conf fix keys off the persisted OCI spec rather than an in-memory cache
(`mesh_wiring.go:280-295`).

### 1. Persist two new container labels

Extend `wendyLabels` (`helpers.go:245`) to write, at container-create time:

| Label | Value | Scope |
|---|---|---|
| `sh.wendy/isolation` | `appCfg.Isolation` | per app (written on each service container) |
| `sh.wendy/depends-on` | comma-joined `ServiceConfig.DependsOn` | per service; omitted when the list is empty |

`wendyLabels` gains the two values as parameters (isolation string, dependsOn
`[]string`); the sole caller (`client.go:828`) passes `appCfg.Isolation` and the
current service's `DependsOn`. A blank isolation or empty dependsOn writes no
label (keeps labels clean and preserves "non-isolated" as the default).

Everything else needed to reconstruct the caches is already persisted:

- appID and service name are existing labels (`sh.wendy/app.id`,
  `sh.wendy/service`).
- Entitlements are already annotations (round-tripped via
  `parseEntitlementsFromAnnotations`) and are read from the container's own
  labels on the start path already (`client.go:1276-1281`).
- Namespace joins and the mesh resolv mount live in the persisted OCI spec.

Build-time `ServiceConfig` fields (`Context`, `Env`, `Resources`, `Frameworks`)
are **never read after create**, so they are deliberately not persisted. The
only post-create consumers of `appServices` are `len(...)` (group detection) and
`ServiceTopoOrder(...)` (needs names + `DependsOn`).

### 2. Rebuild the caches at startup

New method `Client.RebuildAppStateCaches(ctx)`:

- Lists all Wendy-managed containers (same label-scoped query
  `containersForApp` uses, but across all appIDs).
- For each container reads its labels and, under `c.mu`:
  - `appIsolation[appID] = <sh.wendy/isolation label>` (skip when blank).
  - `appServices[appID][service] = &ServiceConfig{DependsOn: <parsed>}` —
    enough for `len()` and `ServiceTopoOrder`.
- Idempotent (safe to call more than once) and best-effort: a per-container
  read failure logs a warning and continues; a total failure logs and returns
  without blocking boot recovery.

Called once at the top of `ReconcileBootContainers` (`monitor.go:150`), before
`MigrateStoppedByUserOnce` and `ListBootContainers`, so the caches are warm
before any `StartContainer` runs. `RebuildAppStateCaches` is added to the
containerd interface the monitor depends on.

### 3. primaryPIDs is not persisted

A pre-reboot PID is meaningless after reboot (the process is gone; the PID may
be recycled). `primaryPIDs` is re-derived exactly as today: `StartContainer`
calls `setPrimaryPID` when the primary starts (`client.go:1185-1189`), and the
existing `primaryTaskAlive` staleness check (`client.go:892-895`) discards any
stale entry. Rebuilding `appIsolation` is what lets that path execute at all
after a reboot — without it, the shared-namespace branch is skipped entirely.

## Behavior after the fix

- Meshed/isolated containers regain CNI networking, `/etc/hosts`, and mesh
  egress on reboot — no `wendy run` re-create needed. This closes the
  "Not yet fixed" item in the mesh data-plane design's reboot section.
- Shared-namespace groups regain primary-PID tracking and topo-ordered group
  restart after reboot.
- Non-isolated apps are unaffected (no isolation label → non-isolated, as
  today).

## Error handling

Consistent with the best-effort style of the surrounding mesh/boot code:
`RebuildAppStateCaches` never returns an error that aborts boot recovery.
Per-container label-read failures are logged and skipped. A container missing
the isolation label is treated as non-isolated (the safe default and today's
post-reboot behavior).

## Backward compatibility

Containers created *before* this change carry no `sh.wendy/isolation` /
`sh.wendy/depends-on` labels. On the first reboot after the agent upgrade,
`RebuildAppStateCaches` finds no isolation label for them and leaves them
non-isolated — i.e. **exactly today's broken-after-reboot behavior, no worse**.
They self-heal on the next `wendy run` (which re-creates the container with the
new labels). No migration or backfill is added: inferring isolation from the
persisted spec (e.g. presence of the mesh resolv mount) was considered and
rejected as YAGNI, since the gap self-corrects on the next deploy.

## Testing

- **Unit — labels:** `wendyLabels` emits `sh.wendy/isolation` and
  `sh.wendy/depends-on` when set, omits them when blank/empty; round-trip
  (build labels → parse back) yields the original isolation + dependsOn.
- **Unit — rebuild:** `RebuildAppStateCaches` against a fake container set
  covering isolated, shared-namespace-with-dependsOn, and non-isolated apps
  populates `appIsolation`/`appServices` correctly; blank/missing labels are
  safe; the call is idempotent.
- **Integration-style — cold-cache reconcile:** starting from empty caches,
  run the rebuild, then assert `getIsolation`/`appServices` return the created
  values so `StartContainer` takes the isolated branch (using the seams in
  `mesh_wiring_test.go` / `client_test.go`).
- **Backward compat:** a container with no isolation label rebuilds as
  non-isolated (no panic, no false-positive isolated wiring).

## Out of scope

- Persisting or restoring `primaryPIDs` across reboot (re-derived at runtime).
- Persisting build-time `ServiceConfig` fields.
- Backfilling labels onto pre-existing containers.
- The follow-up "prefer LAN over cloud" work — LAN-first already exists in
  `MeshDialer.DialDevice`; what it actually needs is a separate brainstorm.
