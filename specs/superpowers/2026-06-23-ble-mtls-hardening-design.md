# BLE/LAN mTLS Hardening Design

**Date:** 2026-06-23  
**Status:** Approved

## Problem

The BLE and LAN gRPC client connections perform mTLS incompletely:

- The server verifies the client certificate (✓)
- The client does **not** verify the server certificate (✗) — `InsecureSkipVerify: true` with no compensating callback
- No org-identity cross-check between client and server certificates
- No protection against connecting to a device from a different org (same CA)
- A user logged into multiple orgs must manually select the right one
- No memory of which devices have been connected to, enabling silent downgrade

## Goals

1. Add ML-DSA-aware server cert verification to all client-side connections (BLE, LAN gRPC, cloud tunnel)
2. Verify the server cert's org ID matches the client's org ID on every connection
3. Auto-select the right org certificate when a user is logged into multiple orgs on the same cloud host
4. Pin device SPKI fingerprints after first successful mTLS connection; allow silent rotation when the chain is still valid

## Non-Goals

- Revocation checking beyond certificate lifetime bounds
- OrgID verification for cloud broker connections (already verified via XFCC header at application layer)
- Pinning for user certs (client side only)

---

## Design

### 1. Core types in `shared/certs`

#### `OrgMismatchError`

```go
type OrgMismatchError struct {
    Want int32 // client's org; 0 if client cert carries no org identity
    Got  int32 // org extracted from server cert
}
```

Returned by `BuildServerVerifyConnection` when `ExpectedOrgID != 0` and the server cert belongs to a different org. Callers use `errors.As` to detect it and drive multi-org retry.

#### `PinChecker` interface

```go
type PinChecker interface {
    CheckAndUpdate(leaf *x509.Certificate, displayName string) error
}
```

`*devicepin.Store` satisfies this. Passing `nil` skips pinning. Defined in `shared/certs` to avoid a circular import between `certs` and `devicepin` (which itself calls `certs.IdentityFromCert`).

#### `ServerVerifyOpts`

```go
type ServerVerifyOpts struct {
    ChainPEM      string       // required: CA chain for verifying server cert
    ExpectedOrgID int32        // 0 = accept any org (but still extract for pinning)
    PinStore      PinChecker   // nil = skip pinning
}
```

#### Extended `BuildServerVerifyConnection`

Signature changes from `BuildServerVerifyConnection(chainPEM string)` to:

```go
func BuildServerVerifyConnection(opts ServerVerifyOpts) (func(tls.ConnectionState) error, error)
```

The returned callback runs in order:

1. **Chain + ML-DSA verification** — existing logic (standard x509.Verify, ML-DSA fallback)
2. **Identity extraction** — `IdentityFromCert(leaf)` → `WendyIdentity{OrgID, EntityType, EntityID}`
3. **Org check** — if `opts.ExpectedOrgID != 0` and server orgID differs → return `&OrgMismatchError{Want, Got}`
4. **Pin check/update** — if `opts.PinStore != nil` → `opts.PinStore.CheckAndUpdate(leaf, displayName)`. I/O errors are logged at WARN, not returned (pin failure never blocks a valid connection).

`displayName` passed to `CheckAndUpdate` is synthesised from the cert's CN or SAN URI (e.g. `"urn:wendy:org:7:asset:42"`).

### 2. `WendyIdentity` and `IdentityFromCert` in `shared/certs/orgident.go`

```go
type WendyIdentity struct {
    OrgID      int32
    EntityType string // "user" or "asset"
    EntityID   string // string form of the numeric ID
}

// IdentityFromCert extracts org, entity type, and entity ID from the leaf cert.
// Returns (zero, false, nil) when no Wendy identity is present.
func IdentityFromCert(leaf *x509.Certificate) (WendyIdentity, bool, error)
```

Resolution order mirrors `OrgFromClientCert`: SAN URI first (`urn:wendy:org:<org>:<type>:<id>`), then CN fallback (`sh/wendy/<org>/<asset>`). The existing `OrgFromClientCert` is reimplemented as a thin wrapper: `id, ok, err := IdentityFromCert(leaf); return id.OrgID, ok, err`.

Pin store key is the full SAN URI string (e.g. `"urn:wendy:org:7:asset:42"`) or synthesised from CN for legacy certs.

### 3. `shared/devicepin` package

**File:** `~/.wendy/known_devices.json`

```json
{
  "urn:wendy:org:7:asset:42": {
    "spkiFingerprint": "sha256:abcdef...",
    "displayName": "My Jetson",
    "lastSeen": "2026-06-23T10:00:00Z"
  }
}
```

**API:**

```go
package devicepin

// Open loads (or creates) the pin store from the wendy config directory.
func Open(configDir string) (*Store, error)

// CheckAndUpdate checks the pin for the device identified by leaf's Wendy
// identity, updating or creating it as needed.
//
//   - Not pinned:          store pin, return nil
//   - Pinned, SPKI match:  update LastSeen, return nil
//   - Pinned, SPKI differ: cert chain already validated by VerifyConnection,
//                          so this is a legitimate rotation — update pin, return nil
//   - Not an asset cert:   return nil (user certs are not pinned)
func (s *Store) CheckAndUpdate(leaf *x509.Certificate, displayName string) error
```

SPKI fingerprint is `"sha256:" + hex.EncodeToString(sha256(leaf.RawSubjectPublicKeyInfo))`.

The store is loaded once per CLI invocation and flushed after each `CheckAndUpdate`. Concurrent CLI processes are not expected; no file locking is added (acceptable for a single-user local file).

### 4. Multi-org retry

#### BLE

`connectBLEAgent` in `cli/commands/helpers.go`:

1. Resolve default auth, open pin store
2. Attempt BLE connect with `cert.OrganizationID` as `ExpectedOrgID`
3. On `*certs.OrgMismatchError`: search **all** `cfg.Auth[*].Certificates[*]` for `OrganizationID == mismatch.Got`
4. If found: retry with that cert (and its chain and pin store)
5. If not found: return error — `"device belongs to org %d; run 'wendy auth login' for that org first"`

`bleTLSConfig` helper is replaced by an inline `attemptBLEConnect(device, certInfo, pins)` to make the retry pattern explicit.

#### LAN gRPC (`ConnectWithTLS`)

`ConnectWithTLS` gains a `PinChecker` parameter (callers pass `nil` if they don't want pinning). After `grpc.NewClient`, it calls `clientConn.Connect()` to eagerly trigger the TLS handshake so the `OrgMismatchError` surfaces at connection time rather than on the first RPC. The existing cert-iteration loops in callers handle the retry naturally.

#### Cloud tunnel connections

The target asset's org ID is known from the cloud API response before dialing. `buildTLSCfg` selects the matching cert upfront; no retry is needed. `PinStore` is `nil` for cloud tunnel paths (the tunnel itself is authenticated at the cloud layer).

### 5. Gaps fixed

| File | Change |
|---|---|
| `cli/ble/conn.go` | `NewClientTLSConfig` migrated to `ServerVerifyOpts` (already has chain; add orgID + pins) |
| `cli/grpcclient/client.go` | Add `VerifyConnection` via `BuildServerVerifyConnection`; add `Connect()` for eager handshake; add `PinChecker` param |
| `cli/commands/cloud_tunnel.go:115` | Add `VerifyConnection` with cert chain + orgID; no pinning |
| `cli/mcp/tools_cloud.go:436` | Same as above |

### 6. Files changed

**New:**
- `go/internal/shared/devicepin/store.go`
- `go/internal/shared/devicepin/store_test.go`

**Modified:**
- `go/internal/shared/certs/mldsa.go` — `ServerVerifyOpts`, `OrgMismatchError`, `PinChecker`, extended `BuildServerVerifyConnection`
- `go/internal/shared/certs/orgident.go` — `WendyIdentity`, `IdentityFromCert`; `OrgFromClientCert` becomes a wrapper
- `go/internal/shared/certs/orgident_test.go` — tests for `IdentityFromCert`
- `go/internal/cli/ble/conn.go` — migrate to `ServerVerifyOpts`
- `go/internal/cli/commands/helpers.go` — multi-org retry, open pin store
- `go/internal/cli/grpcclient/client.go` — `VerifyConnection`, `Connect()`, `PinChecker` param
- `go/internal/cli/commands/cloud_tunnel.go` — `VerifyConnection`
- `go/internal/cli/mcp/tools_cloud.go` — `VerifyConnection`
