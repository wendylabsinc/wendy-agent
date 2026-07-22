# Bridged Network Mode (secure default, via deprecation) — Design

Date: 2026-07-05
Status: Approved (design review with Joannis)
Repo: WendyOS `go/` (wendy-agent). Independent of the mesh data plane; base the
branch on `jo/mesh-foundation` for now (retarget to `main` once mesh merges).

## Problem

A `network` entitlement with `mode` omitted (a bare `{ "type": "network" }`)
maps to **host networking** (`applyNetwork` in `oci/entitlements.go:432-438`
treats `mode == ""` as `host`, stripping the container's network namespace).
Host networking exposes every port the app binds on the device's real
interfaces (LAN/internet) and is the *implicit default*. Operators get public
exposure without asking for it. The docs even claim (wrongly) that omitting
`mode` is "isolated" (`apps/wendy.json.md:197`).

The desired end state is **isolated-by-default**: an app with no explicit mode
should get its own network namespace with outbound internet but **no**
publicly-published ports. But there is currently no such mode — `none` gives an
isolated namespace with *no* connectivity, and only `host` provides internet.
Flipping the default naively would cut every app off the network.

## Decisions (from design review)

1. **Build a real bridged mode with NAT egress** (`mode: "bridge"`): own network
   namespace, private IP, outbound internet via NAT, working DNS, and no host
   port publishing.
2. **Roll out via a deprecation path**, NOT a hard flip: this PR introduces the
   `bridge` mode and *warns* that the implicit-host default is deprecated; the
   default stays host for now and flips to `bridge` in a future major version.

## Key enabler

The CNI bridge config the agent already uses for isolated multi-service apps
sets `"isGateway": true` and `"ipMasq": true` (`cni.go:235-249`). `ipMasq`
makes the CNI bridge plugin install the outbound MASQUERADE rule automatically,
so a container attached to this bridge already gets **internet egress** — the
NAT is not new work. This feature reuses that bridge path for single-service
apps.

## Scope

In scope:
- New `network` mode `bridge`.
- Wire single-service `bridge`-mode apps through the existing CNI bridge (ADD on
  start, DEL on stop), plus in-namespace DNS.
- Accept `bridge` in entitlement validation.
- Deprecation warning for implicit-host apps.
- Docs: document `bridge`, fix the wrong "omitted = isolated" claim, document
  the upcoming default change.

Out of scope (explicit follow-ups):
- **Changing the default** (omitted → bridge). Deferred to a future major
  version; this PR only warns.
- **Publishing a bridge app's port to the host/LAN** (a `ports` host-mapping for
  bridge mode). Bridge ports are private in this PR; cross-device access is via
  the mesh, human/LAN access via a future publish option or a tunnel.
- Prefer-LAN and public-port-exposure-warning work (separate specs/PRs).

## Design

### 1. `applyNetwork`: add a `bridge` branch (`oci/entitlements.go`)

`bridge` keeps the container's own network namespace (do **not** strip it as
host does). The default OCI spec already includes a network namespace, so the
branch mirrors the existing `none` branch (ensure a `network` namespace is
present) — but unlike `none`, the container is then attached to a CNI bridge by
the containerd layer (step 2). No host mounts (`/sys` bind, host resolv.conf)
and no `CAP_NET_ADMIN`.

### 2. Containerd: wire single-service bridge apps through CNI (`containerd/`)

Today CNI ADD runs only for multi-service isolated apps
(`client.go:1289,1310`, gated on app-level `isolation == "isolated" &&
serviceName != ""`). Add a parallel trigger: an app whose **network entitlement
mode is `bridge`** gets, on `StartContainer`:
- `CNIAdd(appID, appName, netnsPath)` using the existing bridge config
  (`buildBridgeCNIConfig` → `isGateway`+`ipMasq`), giving it a private IP and
  NAT egress. Reuse the exact netns-anchoring (`bindNetnsForCNI`) and CNI
  ADD/rollback logic already present for the isolated path — factor the shared
  block so bridge and multi-service isolated share one implementation rather
  than duplicating it.
- A resolv.conf so DNS resolves inside the namespace (reuse the mesh
  resolv.conf helper / host resolv.conf bind pattern).
- On stop/remove: `CNIDel` teardown (reuse the existing DEL path).

The network mode is read from the container's entitlement labels (as mesh
egress already does via `parseEntitlementsFromAnnotations`), so the reboot/
reconcile path works too.

Note the interaction with the mesh-reboot cache work already on this branch:
CNI wiring keys off entitlement labels (persisted), not the in-memory cache, so
it survives restarts.

### 3. Validation: accept `bridge` (`appconfig/appconfig.go:321`)

Add `"bridge"` to the allowed `network` modes.

### 4. Deprecation warning (`containerd` create path)

When an app has a `network` entitlement with an **omitted/empty mode** (the
implicit-host case), log one `WARN` at container create:

> `app "X": network entitlement without an explicit "mode" currently uses host networking (ports are publicly reachable). This default will change to isolated "bridge" networking in a future release. Set "mode": "host" to keep host networking, or "mode": "bridge" for isolated networking with outbound internet.`

The behavior does NOT change in this PR — omitted still means host. Only the
warning and the new opt-in mode are added.

### 5. Docs (`apps/wendy.json.md`, network section)

- Fix the mode table: omitting `mode` (and `host`) = host networking, ports
  publicly reachable — not "isolated".
- Add `mode: "bridge"`: isolated namespace, private IP, outbound internet, ports
  NOT published to the host/LAN (reachable cross-device via a mesh entitlement).
- Add `mode: "mesh"` (currently undocumented) for completeness.
- Note the upcoming default change (host → bridge) and the deprecation warning.

## Error handling

CNI wiring is best-effort in the established style: a failed CNI ADD logs and
rolls back (existing behavior); DNS setup failure logs and the container keeps
the image resolv.conf. The deprecation warning never affects lifecycle.

## Testing

- **Unit:** validation accepts `bridge` and still rejects unknown modes;
  `applyNetwork("bridge")` keeps a network namespace and adds no host mounts /
  `CAP_NET_ADMIN`; deprecation warning fires for omitted mode and NOT for
  explicit `host`/`bridge`/`mesh`; the shared CNI-wiring predicate selects
  bridge and multi-service-isolated apps and not host/none.
- **Hardware-dependent (cannot be unit-verified — flag in the PR):** actual
  bridge attachment, private IP, outbound internet (NAT), DNS resolution, and
  teardown on a real device. The PR is a **draft** pending on-device
  validation (2× device or 1 device with an internet reachability check from
  inside a bridge-mode container).
- `go build ./...`, `go vet ./internal/...`, `go test` for affected packages.

## Rollout

Non-breaking: default unchanged, `bridge` is opt-in, deprecation warning
informs. The default flip (omitted → bridge) is a separate future-major PR that
this one sets up.
