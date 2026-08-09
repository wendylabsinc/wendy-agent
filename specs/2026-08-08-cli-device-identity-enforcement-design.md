# CLI Device Identity Enforcement (follow-up to PR #1616 review)

**Date:** 2026-08-08
**Status:** Approved (design)
**Branch:** `jo/cli-device-identity-enforcement` (stacked on
`ed/lkg-connect-cache`, PR #1619, itself stacked on `ed/instant-mdns-discovery`,
PR #1616)
**Depends on:** `jo/pin-device-identity`, to be opened as its own PR against
`main` first and merged into this branch (see *Sequencing*)

## Problem

mDNS TXT records are unauthenticated: anyone on the LAN can answer a browse
with any content. PR #1616 consolidates the parsing of those records into
`lanDeviceFromService` (`shared/discovery/mdns.go`), which derives a device's
`ID`, `DisplayName`, `AssetID`, `OrgID`, `MeshName` and `IsMTLS` from them.
Nothing downstream on the CLI path binds any of that to the certificate the
device later presents.

Concretely, on `main` today:

1. **No identity binding.** `newAgentTLSConfig` (`cli/grpcclient/client.go:152`)
   sets `InsecureSkipVerify: true` and passes `ExpectedOrgID:
   int32(certInfo.OrganizationID)` — *the CLI's own* certificate org, never the
   discovered one. `BuildServerVerifyConnection` (`shared/certs/mldsa.go:160`)
   then checks chain, org, and SPKI pin only; `AssetID`, `ID`, and hostname are
   never compared against `identity.EntityID`. Any host holding any certificate
   chaining to the Wendy CA is accepted at whatever address mDNS supplied.
2. **Grace mode widens it.** `mldsa.go:224` rejects only when the leaf carries a
   Wendy identity *and* the org differs, so a chain-valid certificate with no
   Wendy URN at all passes unconditionally.
3. **Plaintext downgrade.** `dialAgentLadderWithCerts` falls back to
   `grpcclient.Connect(ctx, plaintextAddr)` (`cli/commands/helpers.go:1355` on
   `main`, `:1641` on #1616, `:1822` on #1619) unless every failure looked like
   a cert rejection —
   and `isCertRejectionError` explicitly excludes `"first record does not look
   like a TLS handshake"`, which is exactly what a plaintext impostor produces.
   The one guard, `provisionedAgentAdvertisedMTLS`, reads the spoofable `tls`
   TXT bit.
4. **Pinning does not backstop any of it.** `devicepin.Store.CheckAndUpdate`
   (`shared/devicepin/store.go:60`) is keyed by the *certificate's own*
   `urn:wendy:org:N:asset:M`, not by hostname, so a different asset answering at
   a spoofed address is a fresh key rather than a mismatch — and a genuine
   mismatch only warns before overwriting the pin and returning nil. The
   hostname-keyed `enforceDevicePin` (`cli/commands/device_pin.go:45`) applies
   only to the saved default device, and pins the *CLI's* org, so it cannot
   distinguish two devices within one organisation.

The agent already solves this for its own mesh dials:
`agent/mtls.NewClientTLSConfigExpectingPeer` (`agent/mtls/server.go:178`) names
this exact threat in its doc comment and requires `ident.OrgID == wantOrgID &&
ident.EntityID == wantAssetID`. The CLI has no equivalent.

**Impact.** Within one organisation, an attacker on the LAN can redirect a
device name to a host it controls and be accepted over mTLS with any same-CA
certificate; or serve plaintext gRPC and get an unauthenticated session; or
poison `AssetID`/`MeshName` used for fleet peer URLs
(`cli/commands/fleet_manifest.go:213`).

This is pre-existing on `main` — #1616 consolidates the parsing rather than
introducing the trust gap — but that consolidation is what makes a single
chokepoint fix possible.

## Approved decisions

| Question | Decision |
| --- | --- |
| Trust anchor | Local TOFU pin: first verified mTLS connection to a name records `(orgID, assetID)`; later connections must match |
| Mismatch policy | Hard fail with an explicit re-pin command — no interactive "trust this?" prompt |
| Cloud's role | Cloud is authority: a cloud-verified identity seeds and overwrites the pin silently; a LAN observation never overwrites a cloud pin |
| Legacy posture | Strict once pinned, permissive on first contact — unpinned hosts keep today's grace mode and plaintext fallback |
| SPKI store | Hard fail on key change, with an expiry-based rotation window |

## Design

### 1. Enforcement inside the handshake

`certs.ServerVerifyOpts` gains `ExpectedIdentity *WendyIdentity`. When non-nil,
step 2 of `BuildServerVerifyConnection` requires the leaf to carry an **asset**
identity whose org and entity ID match exactly; grace mode does not apply and a
no-URN certificate is rejected. When nil, behaviour is exactly as today. This is
deliberately the same shape as `agent/mtls.NewClientTLSConfigExpectingPeer`, so
both sides of the fleet enforce identity identically.

It must live in `VerifyConnection`, not `VerifyPeerCertificate`: a resumed TLS
1.3 handshake skips `VerifyPeerCertificate` entirely and calls only
`VerifyConnection`. The CLI's session cache (`tlscache`, PR #1612) makes
resumption the common path, so a check in the wrong callback would silently stop
running after the first connect — the gotcha `agent/mtls/server.go:212` already
documents.

Mismatch produces a typed `certs.IdentityMismatchError{WantOrg, WantAsset,
GotOrg, GotAsset}`. Because `grpc.NewClient` is lazy, the error surfaces mangled
inside the `GetAgentVersion` probe, so it is captured the way `observedServerOrg`
already is — an atomic set inside the verifier, read back via
`conn.IdentityMismatch()`. **On mismatch the dial ladder aborts immediately**
rather than continuing to the next certificate or the next port: trying our other
client certs is pointless when it is the *device* that is wrong, and aborting
turns a security failure into a fast, legible one.

### 2. Pin record

`config.DevicePin` becomes:

```go
type DevicePin struct {
    OrgID        int    `json:"orgId"`
    CloudGRPC    string `json:"cloudGRPC"`
    AssetID      string `json:"assetId,omitempty"`
    Source       string `json:"source,omitempty"`       // "cloud" | "lan"
    SPKI         string `json:"spki,omitempty"`
    SPKINotAfter string `json:"spkiNotAfter,omitempty"` // RFC3339
}
```

`OrgID`, `CloudGRPC`, `AssetID` and the `PinAdoptAsset` legacy-upgrade verdict
come from the `jo/pin-device-identity` commit. This spec adds `Source`, `SPKI`,
`SPKINotAfter`.

"Cloud-verified" means an identity obtained over an authenticated session with
the org's cloud — the asset roster returned by `fetchCloudAssetsFiltered`, or a
cloud tunnel connection whose TLS terminates at cloud. It specifically does not
mean "an identity that mentions a cloud host": `DevicePin.CloudGRPC` is a
recorded attribute, not evidence.

Precedence:

- A cloud-verified identity writes `Source: "cloud"`, overwriting anything, and
  refreshes silently — re-provisioning through cloud needs no manual unpin.
- A LAN observation never overwrites a `Source: "cloud"` pin. Conflict is a hard
  fail.
- TOFU writes `Source: "lan"` only when no pin exists; a later cloud value
  silently corrects it.
- A pin with neither `AssetID` nor `Source` (written by an older CLI) is treated
  as `Source: "lan"` with no asset constraint: org and cloud are enforced as
  today and the asset is backfilled on the next successful connect. Existing
  users are not locked out of their default device on first run after upgrade.

### 3. Pin key: the name the user asked for

Today the ladder receives a bare `plaintextAddr` string, so the requested
identity is gone by dial time. Introduce:

```go
type dialTarget struct {
    PinKey   string              // user-facing name; "" disables pin enforcement
    Addr     string              // host:port actually dialed
    Expected *certs.WendyIdentity // non-nil when a pin or cloud value constrains it
}
```

threaded from `resolveDeviceAddress`, the picker, and the cloud connect path
into `dialAgentLadderWithCerts`. #1619's `dialAgentLKG` funnels through
`dialAgentLadderWithCertsFn`, so the single enforcement point covers the LKG
fast path too, and #1619 already threads `originalAddr` for
`cacheConnectSuccess` — that is the pin key.

Keying on the requested name rather than the dialed IP matters: an IP-keyed pin
false-positives on ordinary DHCP churn, which trains users to unpin reflexively.

Cloud asset names and LAN hostnames do not always agree (`calm-zinnia` vs
`wendyos-calm-zinnia.local`), so lookup tries hostname, then display name, then
mesh name. The safety property that makes this acceptable: **key resolution may
consult spoofable data, because choosing the wrong key can only ever produce a
mismatch — a stricter outcome — never a bypass.** Trust decisions stay on the
certificate; only key *selection* touches TXT-derived data.

### 4. Plaintext downgrade

The rule becomes state-based rather than TXT-based: **if a pin exists for the
key, the plaintext fallback is not attempted at all.** No dependence on the `tls`
TXT record, the cache's `MTLS` flag, or `isCertRejectionError`'s string matching
— all three are attacker-influenced or fragile.

The guard reads the pin state the dial already resolved (`dialTarget.pinned`,
backed by `PinnedKey`), never a fresh lookup. One connect makes one decision
about what the pin says: a second, independent read can observe a different
answer — every `wendy` invocation shares one config file, so a cloud seeding or
an `unpin` from another process lands mid-ladder — and disagreeing in the
unpinned direction would hand a host that was pinned when the connect started an
unauthenticated connection. Deriving the guard, `Expected`, and the refusal key
from one resolution makes that disagreement unrepresentable.

`provisionedAgentAdvertisedMTLS` (`helpers.go:1893` on #1619) loses its security role and
survives only as a phrasing hint for the error message; its doc comment is
updated to say so, because today it reads like a guard.

Unpinned hosts keep the fallback, which is what preserves provisioning for
genuinely fresh devices. #1619's `dialAgentLKG` already declines a plaintext
downgrade when the cache entry advertised mTLS; this change turns that heuristic
into a rule anchored on the pin instead of on cache state.

The `jo/pin-device-identity` commit's `challengeUnprovisionedDevice` covers the
same case *after* connecting and prompts interactively. It is re-pointed at the
hard-fail policy and retained as the backstop for the paths the ladder rule does
not cover: connections whose `PinKey` resolves only after the transport is up
(cloud tunnel), and `NewFromConn`-style connections that never run the ladder at
all.

### 5. SPKI pinning

`devicepin.Store.CheckAndUpdate` returns a real error on key change, and
`BuildServerVerifyConnection` step 3 propagates it instead of discarding it
(`mldsa.go:234-238`).

What propagates is the *rejection*, not every error the store can raise. Only a
`PinMismatchError` — the peer's key changed inside the pinned certificate's
validity window — aborts the handshake; a failure to WRITE the store does not.
The store is local bookkeeping, and dropping an otherwise fully verified
connection because `~/.wendy` is read-only or the disk is full is an outage with
no security question behind it. Because `shared/certs` cannot import
`shared/devicepin` (the reason `PinChecker` is an interface), the distinction is
carried by a marker interface `certs.BlockingPinError` that the rejection
implements and a write failure does not; a compile-time assertion in `devicepin`
keeps the two halves from drifting apart silently.

To keep that from breaking ordinary certificate renewal, the store records the
pinned leaf's `NotAfter`. A key change is accepted silently — and re-pinned —
when the previously pinned certificate has already expired, since that is
rotation by definition; otherwise it hard-fails. A cloud-authoritative
connection also refreshes the SPKI pin silently. Without one of these, a
fleet-wide renewal becomes a manual unpin per device per user, which is the kind
of friction that gets the check disabled later.

### 6. Surface

- **`wendy device unpin <hostname>`** clears both the `config.DevicePins` entry
  and the `devicepin` SPKI entry. It is the single escape hatch every refusal
  points at.
- `wendy device set-default <host>` keeps the `clearDevicePinForRepin` behaviour
  from `jo/pin-device-identity`: naming a device is an assertion that you mean
  it, so the pin is dropped and re-established.
- Refusal messages name the pinned identity, the observed identity, which field
  changed, and the exact command to run. They are identical in interactive,
  JSON, and non-interactive modes — no prompt, per the approved policy.
- Cloud seeding hooks the two places the CLI already holds verified asset data:
  the cloud tunnel connect path, and `fetchCloudAssetsFiltered` (used by
  `wendy cloud discover`).

### 7. Scope of enforcement

Enforcement applies to every gRPC connection with a non-empty `PinKey`:
`--device`, the saved default, picker selections, and cloud connects. This is
the main behavioural widening over `jo/pin-device-identity`, which gates on
`if isDefault`.

## Sequencing

1. `jo/pin-device-identity` is pushed as its own PR against `main` and reviewed
   independently. It is a coherent, self-contained improvement (asset id in the
   pin, verified-identity sink, unprovisioned challenge) and carries its own
   tests.
2. This branch is cut from `ed/lkg-connect-cache` (#1619) and merges
   `jo/pin-device-identity` into it. Both touch `device_pin.go` and `helpers.go`,
   so the merge is done here rather than left to the reviewer.
3. On merge of #1616, #1619, and the pin PR, this branch is retargeted to
   `main`.

## Testing

All of it runs without hardware:

- **Verifier** (`shared/certs`): table-driven over match, wrong asset, wrong
  org, no URN, user-URN instead of asset-URN, and a resumed handshake — the last
  asserting the check still runs when `VerifyPeerCertificate` is skipped.
- **Pin precedence** (`shared/config`): cloud-over-lan, lan-never-over-cloud,
  legacy fieldless pin upgrade, asset backfill.
- **SPKI** (`shared/devicepin`): mismatch within validity hard-fails; mismatch
  after `NotAfter` re-pins silently; cloud refresh re-pins silently.
- **Ladder** (`cli/commands`): identity mismatch aborts the ladder without
  trying further certs or ports; a pinned host never reaches the plaintext rung;
  an unpinned host still does; LKG fast path enforces identically.
- **Key resolution**: hostname / display name / mesh name lookup order, and that
  an unresolvable key fails closed for a pinned device.

On-device verification is **not** claimed by this PR and will be listed as
outstanding in the PR body.

## Explicitly out of scope

- The agent-side mesh path, which already pins correctly via
  `NewClientTLSConfigExpectingPeer`.
- The registry authorization gap tracked separately as WDY-2355.
- Any change to how devices *advertise* TXT records — the fix is entirely on the
  consuming side, since a hostile advertiser is the threat model.
