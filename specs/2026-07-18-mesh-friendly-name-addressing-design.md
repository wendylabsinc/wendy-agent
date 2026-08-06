# Mesh Friendly-Name Addressing — Design

Date: 2026-07-18
Status: Approved (design review with Joannis)
Builds on: `jo/mesh-foundation` (mesh data plane: `mesh/vip.go`, `mesh/dns.go`,
`mesh/proxy.go`, `services/mesh_dialer.go`) and the mesh data plane design
(`specs/2026-07-02-mesh-data-plane-design.md`).

## Goal

Add a human-readable mesh hostname **alongside** the existing numeric form:

```
http://brave-dolphin.acme.cloud.wendy.dev:8080     # new: <devicename>.<org-slug>
http://device-215.cloud.wendy.dev:8080             # unchanged: pure-arithmetic
```

`brave-dolphin` is the target device's cloud asset name; `acme` is the org
slug. **For now the org segment must be the dialing device's own org** — mesh
never resolves a foreign org. Both forms produce the same asset ID and reuse
the identical VIP → proxy → dialer pipeline downstream; the friendly name is a
thin resolver layer only.

## Why this is a resolver, not more arithmetic

`device-<N>` is a pure function: the name *is* the VIP (`device-215` →
`10.99.0.215`), with no state anywhere. A friendly name has no arithmetic path
to a VIP, so it needs one new step — **`<devicename>` → asset ID** — after
which everything is identical to the numeric path. The VIP mapping itself stays
a pure function; the only new state is a name directory, not an allocator.

## Grammar

The mesh DNS server is authoritative for exactly two name shapes; every other
query is forwarded upstream unchanged (regular internet DNS inside the
container keeps working):

| Form | Example | Resolution |
|---|---|---|
| `device-<N>.cloud.wendy.dev` | `device-215` | arithmetic (unchanged) |
| `<devicename>.<org-slug>.cloud.wendy.dev` | `brave-dolphin.acme` | directory lookup → asset ID → arithmetic |

`<devicename>` and `<org-slug>` are single DNS labels (`[a-z0-9-]`, no dots).
A friendly name with the wrong org slug, an unknown device, or an ambiguous
device (see Edge cases) returns **NXDOMAIN** — `device-<N>` remains the
unambiguous escape hatch in every failure case.

## Resolution (hybrid, in order)

Resolution stops at the first step that yields an asset ID.

1. **mDNS first (LAN peers).** Agents already advertise `name` and (at
   provisioning) `assetid` in their Avahi TXT record; this design adds a numeric
   `orgid` entry. A browse resolves `name → assetid` for any peer on the LAN,
   filtered to the dialer's **own** `orgid` so a same-named device from another
   org on the shared LAN is never matched — sub-second, no cloud round-trip. The
   numeric `orgid` is advertised (not the slug) because the device knows its
   org id locally at provisioning time; the human `org-slug` is only ever used
   for the DNS enforcement check below, against a value learned from cloud.
2. **Cloud roster fallback (off-LAN peers).** The agent syncs its org's
   `{name → asset-id}` directory (and its own org slug) from the cloud via the
   new `GetMeshRoster` RPC, caches it, and refreshes periodically and on
   cache-miss. This is what lets a **named** peer that is off-LAN resolve and
   then take the existing cloud-broker relay path.

Rationale for hybrid over either alone: mDNS alone cannot resolve a named peer
that is off-LAN (breaking the mesh's LAN-first-*with-fallback* promise); a
cloud-only lookup pays a round-trip and a cloud dependency even for the common
same-LAN case. Hybrid keeps the LAN case cloud-free and the off-LAN case
functional.

## Org slug

Because a device only ever resolves within **its own** org, the DNS server
enforces:

```
<org-slug> == this device's own org slug   →   proceed
otherwise                                  →   NXDOMAIN
```

No cross-org resolution and therefore **no need for globally-unique slugs**.
The slug is derived by normalizing the cloud `Organization.name`
(lowercase; spaces/underscores → `-`; strip characters outside `[a-z0-9-]`;
collapse repeated `-`), learned from the `GetMeshRoster` response once and
cached with the roster. No cloud schema change for a dedicated slug column in
v1; the normalization function is the single source of truth on both ends of
any future comparison.

## Cloud change (separate Swift repo, `~/git/wendy/cloud`)

One new RPC, callable by an **asset certificate**, scoped to the caller's own
org:

```
GetMeshRoster(GetMeshRosterRequest) returns (GetMeshRosterResponse)

GetMeshRosterResponse {
  string org_slug = 1;                 // normalized server-side (same rules)
  repeated MeshRosterEntry entries = 2 // { string name; int32 asset_id }
}
```

- Authorized only for asset certs; the org is taken from the caller's cert
  identity (`urn:wendy:org:<id>:asset:<id>`), never from a request field, so a
  device cannot enumerate another org.
- Returns only compute devices in the caller's org: `{name, asset_id}` pairs
  plus the org slug. Deliberately narrower than `ListAssets` (no blob URLs,
  hardware specs, tags, IPs) — least privilege for what mesh needs.
- Gated by the same `orgs.mesh_enabled` flag as the rest of the mesh
  (`PermissionDenied` when off), so disabling mesh also stops roster exposure.

The agent reaches this over the existing cloud gRPC connection it already uses
for the tunnel broker, presenting the same asset-cert identity.

## Components (WendyOS repo, `go/`)

| Piece | Where | Responsibility |
|---|---|---|
| Name grammar + resolver | `internal/agent/mesh/dns.go` | parse both forms; friendly form → resolver; org-slug enforcement; NXDOMAIN rules |
| Slug normalization | `internal/agent/mesh/slug.go` | `Organization.name` → DNS-label slug (pure, shared-rule) |
| `Resolver` interface | `internal/agent/mesh/dns.go` | `Resolve(name) (assetID, ok)` + `OrgSlug()`, injected via `SetResolver` (keeps `mesh` free of gRPC/mDNS deps, mirroring the `PeerDialer` seam) |
| Hybrid resolver | `internal/agent/services/mesh_resolver.go` | implements `mesh.Resolver`: mDNS browse (own-orgid filter) → cloud-roster cache; duplicate detection |
| Roster cache/sync | `internal/agent/services/mesh_roster.go` | periodic `GetMeshRoster`, TTL cache, own-org-slug store, refresh-on-miss |
| mDNS advertise | `internal/agent/configpartition/apply.go` | add numeric `orgid` TXT alongside the existing `assetid` at provisioning |
| mDNS browse | `internal/shared/discovery` + `models.LANDevice` | parse `name` + `orgid` TXT into `LANDevice`; resolve peer `name → assetid` on LAN |
| Proto | `Proto/wendy/` + cloud `service-protos` | `GetMeshRoster` |

The resolver is injected into the DNS server behind a small interface
(`func(name string) (assetID int32, ok bool)`) so `dns.go` stays free of mDNS
and gRPC dependencies and is unit-testable with a fake resolver, matching the
existing seam style of the mesh package.

## Error handling

Consistent with the existing best-effort mesh plumbing. Every friendly-name
failure — wrong org slug, unknown/ambiguous name, cold cache with peer
off-LAN, `mesh_enabled` off — surfaces to the container as an ordinary
NXDOMAIN or connection refused; the agent log carries the real reason. Roster
sync failures never kill the agent and never affect the numeric `device-<N>`
path or non-mesh containers. A stale roster resolves to a stale asset ID whose
VIP/dialer path simply fails to connect, identical to any offline peer today.

## Edge cases

- **Name normalization:** the device `Asset.name` is normalized to a DNS label
  and matched case-insensitively; the advertised/roster name is the normalized
  form.
- **Duplicate friendly names** (two devices in the org normalize to the same
  label): the friendly name resolves to **NXDOMAIN** with a logged warning;
  the operator disambiguates by renaming, and `device-<N>` works meanwhile.
- **Cold cache / cloud unreachable:** LAN peers still resolve via mDNS;
  off-LAN named lookups fail closed exactly like an unreachable peer today.
- **Own org slug not yet learned** (first boot, pre-first-sync): friendly names
  return NXDOMAIN until the first `GetMeshRoster` succeeds; `device-<N>` works
  immediately.

## Testing

- **Unit (agent):** grammar parse for both forms + junk; org-slug enforcement
  (own slug proceeds, foreign slug → NXDOMAIN); slug normalization table;
  duplicate-name → NXDOMAIN; resolver ordering (mDNS hit short-circuits cloud;
  cloud fallback on mDNS miss); cold-cache behavior; numeric path unaffected.
- **Unit (cloud):** `GetMeshRoster` authz matrix (asset cert vs user cert;
  own-org only; `mesh_enabled` off → `PermissionDenied`); response contains
  only `{name, asset_id}` + slug; slug normalization parity with the agent.
- **E2E (two devices):** HelloMesh targets `<name>.<org>.cloud.wendy.dev:8080`
  once on a shared LAN (mDNS path) and once with LAN blocked (cloud-roster +
  broker path). HelloMesh's `MESH_TARGET` default and README switch to the
  friendly form; the numeric form stays documented as the fallback.

## Backward compatibility

`device-<N>.cloud.wendy.dev` is untouched — same grammar, same arithmetic,
same tests. Friendly names are purely additive. A device running older agent
software (no TXT `org-slug`/`asset-id`, no `GetMeshRoster`) is still reachable
by its numeric `device-<N>` name and, once it advertises the new TXT entries,
by friendly name on the LAN.

## Out of scope (v1)

- Cross-org friendly names (the org segment must be the device's own org).
- Service-name-level addressing (host:port only, as in the base design).
- A dedicated `organizations.slug` column in cloud (derived by normalization
  for now).
- Non-default serviceCIDRs (inherited limitation from the base design).
