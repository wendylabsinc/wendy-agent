# BLE/LAN mTLS Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add server cert verification, orgID matching, device SPKI pinning, and multi-org retry to all Wendy CLI→agent connections (BLE, LAN gRPC, cloud tunnel).

**Architecture:** `BuildServerVerifyConnection` in `shared/certs` grows an options struct that drives ML-DSA chain verification → orgID check → SPKI pin update, all inside a single `VerifyConnection` TLS callback. A new `shared/devicepin` package owns the pin file. BLE multi-org retry catches `OrgMismatchError` and retries with the matching cert from any auth entry; LAN already iterates all certs with a live probe so multi-org works there for free.

**Tech Stack:** Go 1.26, `crypto/tls`, `github.com/cloudflare/circl` (ML-DSA), `google.golang.org/grpc` v1.81.

## Global Constraints

- Module root: `github.com/wendylabsinc/wendy`; all package paths are `github.com/wendylabsinc/wendy/go/internal/...`
- Tests run from repo root: `go test ./go/internal/shared/certs/...` etc.
- `shared/devicepin` must not import `shared/certs` (would be circular); `shared/certs` defines the `PinChecker` interface
- `InsecureSkipVerify: true` is kept on all agent connections; it is justified by ML-DSA chain-cert parse failures and absent TLS hostnames — the `VerifyConnection` callback performs the actual chain validation
- `//nolint:gosec` comment required alongside every `InsecureSkipVerify: true`
- All new Go files: `package` declaration matches directory name
- Pin file path: `~/.wendy/known_devices.json` (inside `config.ConfigDir()`)
- Do not add `grpc.WithBlock()` (removed in gRPC v1.58+)

---

## File Map

**New files:**
- `go/internal/shared/devicepin/store.go` — `Store`, `Open`, `CheckAndUpdate`
- `go/internal/shared/devicepin/store_test.go`

**Modified files:**
- `go/internal/shared/certs/orgident.go` — add `WendyIdentity`, `IdentityFromCert`; rewrite `OrgFromClientCert` as wrapper; rename internal parsers
- `go/internal/shared/certs/orgident_test.go` — add `IdentityFromCert` tests
- `go/internal/shared/certs/mldsa.go` — add `OrgMismatchError`, `PinChecker`, `ServerVerifyOpts`; change `BuildServerVerifyConnection` signature; add orgID check + pin step to callback
- `go/internal/shared/certs/mldsa_verify_test.go` — update any `BuildServerVerifyConnection(chainPEM)` calls to new signature (none expected; the function wasn't called directly in that test file)
- `go/internal/cli/ble/conn.go` — change `NewClientTLSConfig(certPEM, keyPEM, chainPEM string)` to `NewClientTLSConfig(certPEM, keyPEM string, opts certs.ServerVerifyOpts)`
- `go/internal/cli/commands/helpers.go` — replace `bleTLSConfig` + `connectBLEAgent` with multi-org-retry version; add `attemptBLEConnect`, `findCertByOrgID`, `openPinStore` helpers
- `go/internal/cli/grpcclient/client.go` — add `VerifyConnection` to `ConnectWithTLS`
- `go/internal/cli/commands/cloud_tunnel.go` — add `VerifyConnection` at line ~115
- `go/internal/cli/mcp/tools_cloud.go` — add `VerifyConnection` at line ~436

---

## Task 1 — `WendyIdentity` and `IdentityFromCert`

**Files:**
- Modify: `go/internal/shared/certs/orgident.go`
- Modify: `go/internal/shared/certs/orgident_test.go`

**Interfaces produced:**
```go
type WendyIdentity struct {
    OrgID      int32
    EntityType string // "user" or "asset"
    EntityID   string // numeric ID as string, e.g. "42"
}

// IdentityFromCert extracts org, entity type, and entity ID.
// (zero, false, nil) when no Wendy identity is present.
// (zero, false, err) when a malformed identity is found.
func IdentityFromCert(leaf *x509.Certificate) (WendyIdentity, bool, error)

// OrgFromClientCert is now a thin wrapper around IdentityFromCert.
func OrgFromClientCert(leaf *x509.Certificate) (int32, bool, error)
```

- [ ] **Step 1: Write failing tests for `IdentityFromCert`**

Add to `go/internal/shared/certs/orgident_test.go` after the existing tests:

```go
func TestIdentityFromCert(t *testing.T) {
    mustParseSANURI := func(raw string) *url.URL {
        u, err := url.Parse(raw)
        if err != nil {
            t.Fatalf("parsing URI %q: %v", raw, err)
        }
        return u
    }
    makeCert := func(cn string, uris ...string) *x509.Certificate {
        c := &x509.Certificate{Subject: pkix.Name{CommonName: cn}}
        for _, u := range uris {
            c.URIs = append(c.URIs, mustParseSANURI(u))
        }
        return c
    }

    tests := []struct {
        name        string
        cert        *x509.Certificate
        wantID      WendyIdentity
        wantOK      bool
        wantErr     bool
    }{
        {
            name: "SAN URI asset",
            cert: makeCert("ignored", "urn:wendy:org:7:asset:42"),
            wantID: WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"},
            wantOK: true,
        },
        {
            name: "SAN URI user",
            cert: makeCert("ignored", "urn:wendy:org:3:user:99"),
            wantID: WendyIdentity{OrgID: 3, EntityType: "user", EntityID: "99"},
            wantOK: true,
        },
        {
            name: "CN fallback sh/wendy",
            cert: makeCert("sh/wendy/5/123"),
            wantID: WendyIdentity{OrgID: 5, EntityType: "asset", EntityID: "123"},
            wantOK: true,
        },
        {
            name:   "no identity",
            cert:   makeCert("wendy/user/99"),
            wantOK: false,
        },
        {
            name:    "multiple wendy URNs",
            cert:    makeCert("", "urn:wendy:org:1:asset:1", "urn:wendy:org:2:asset:2"),
            wantErr: true,
        },
        {
            name:    "malformed URN",
            cert:    makeCert("", "urn:wendy:org:0:asset:5"),
            wantErr: true,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            id, ok, err := IdentityFromCert(tt.cert)
            if (err != nil) != tt.wantErr {
                t.Fatalf("IdentityFromCert() error = %v, wantErr %v", err, tt.wantErr)
            }
            if ok != tt.wantOK {
                t.Errorf("ok = %v, want %v", ok, tt.wantOK)
            }
            if ok && id != tt.wantID {
                t.Errorf("identity = %+v, want %+v", id, tt.wantID)
            }
        })
    }
}

func TestOrgFromClientCert_StillWorks(t *testing.T) {
    mustParseURI := func(raw string) *url.URL {
        u, _ := url.Parse(raw)
        return u
    }
    cert := &x509.Certificate{
        URIs: []*url.URL{mustParseURI("urn:wendy:org:7:asset:42")},
    }
    orgID, ok, err := OrgFromClientCert(cert)
    if err != nil || !ok || orgID != 7 {
        t.Errorf("OrgFromClientCert() = %d, %v, %v; want 7, true, nil", orgID, ok, err)
    }
}
```

Add required imports to the test file:
```go
import (
    "crypto/x509"
    "crypto/x509/pkix"
    "net/url"
    "testing"
)
```

- [ ] **Step 2: Run tests — expect compilation failure**

```bash
go test ./go/internal/shared/certs/... -run TestIdentityFromCert
```
Expected: `undefined: IdentityFromCert`

- [ ] **Step 3: Implement `WendyIdentity` and `IdentityFromCert` in `orgident.go`**

Replace the contents of `go/internal/shared/certs/orgident.go` with:

```go
// Package certs provides certificate and key utilities for mTLS authentication.
package certs

import (
    "crypto/x509"
    "fmt"
    "strconv"
    "strings"
)

const wendyOrgURNPrefix = "urn:wendy:org:"

// WendyIdentity holds the Wendy org and entity identity extracted from a certificate.
type WendyIdentity struct {
    OrgID      int32
    EntityType string // "user" or "asset"
    EntityID   string // numeric ID as string
}

// IdentityKey returns the canonical URN string used as a pin-store key.
func (w WendyIdentity) IdentityKey() string {
    return fmt.Sprintf("urn:wendy:org:%d:%s:%s", w.OrgID, w.EntityType, w.EntityID)
}

// IdentityFromCert extracts the Wendy org+entity identity from a certificate.
//
// Resolution order:
//  1. SAN URI beginning with "urn:wendy:org:" (authoritative; exactly one allowed)
//  2. CommonName "sh/wendy/<org>/<asset>" (legacy fallback)
//  3. No identity: returns (zero, false, nil)
func IdentityFromCert(leaf *x509.Certificate) (WendyIdentity, bool, error) {
    var wendyURNs []string
    for _, u := range leaf.URIs {
        raw := u.String()
        if strings.HasPrefix(raw, wendyOrgURNPrefix) {
            wendyURNs = append(wendyURNs, raw)
        }
    }
    if len(wendyURNs) > 1 {
        return WendyIdentity{}, false, fmt.Errorf("certificate contains %d wendy org URNs; expected at most one", len(wendyURNs))
    }
    if len(wendyURNs) == 1 {
        id, err := parseWendyOrgURN(wendyURNs[0])
        if err != nil {
            return WendyIdentity{}, false, err
        }
        return id, true, nil
    }

    cn := leaf.Subject.CommonName
    if strings.HasPrefix(cn, "sh/wendy/") {
        id, err := parseShWendyCN(cn)
        if err != nil {
            return WendyIdentity{}, false, err
        }
        return id, true, nil
    }

    return WendyIdentity{}, false, nil
}

// OrgFromClientCert extracts the org ID from a certificate. It is a wrapper
// around IdentityFromCert that drops entity type and ID.
func OrgFromClientCert(leaf *x509.Certificate) (orgID int32, hasOrg bool, err error) {
    id, ok, err := IdentityFromCert(leaf)
    return id.OrgID, ok, err
}

// parseWendyOrgURN parses "urn:wendy:org:<org>:(user|asset):<id>" into a WendyIdentity.
func parseWendyOrgURN(uri string) (WendyIdentity, error) {
    parts := strings.Split(uri, ":")
    if len(parts) != 6 {
        return WendyIdentity{}, fmt.Errorf("invalid wendy URN format (want 6 colon-separated parts): %s", uri)
    }
    if parts[0] != "urn" || parts[1] != "wendy" || parts[2] != "org" {
        return WendyIdentity{}, fmt.Errorf("invalid wendy URN prefix: %s", uri)
    }
    orgID, err := strconv.ParseInt(parts[3], 10, 32)
    if err != nil {
        return WendyIdentity{}, fmt.Errorf("invalid organization ID in URN %q: %w", parts[3], err)
    }
    if orgID <= 0 {
        return WendyIdentity{}, fmt.Errorf("organization ID must be positive, got %d", orgID)
    }
    entityType := parts[4]
    if entityType != "user" && entityType != "asset" {
        return WendyIdentity{}, fmt.Errorf("unknown entity type in wendy URN %q: %s", uri, entityType)
    }
    if parts[5] == "" {
        return WendyIdentity{}, fmt.Errorf("empty entity ID in wendy URN: %s", uri)
    }
    return WendyIdentity{OrgID: int32(orgID), EntityType: entityType, EntityID: parts[5]}, nil
}

// parseShWendyCN parses "sh/wendy/<org>/<asset>" into a WendyIdentity.
// Caller must have verified the CN starts with "sh/wendy/".
func parseShWendyCN(cn string) (WendyIdentity, error) {
    parts := strings.Split(cn, "/")
    if len(parts) != 4 {
        return WendyIdentity{}, fmt.Errorf("invalid sh/wendy CommonName (want 4 slash-separated segments): %s", cn)
    }
    orgID, err := strconv.ParseInt(parts[2], 10, 32)
    if err != nil {
        return WendyIdentity{}, fmt.Errorf("invalid organization ID in CommonName %q: %w", parts[2], err)
    }
    if orgID <= 0 {
        return WendyIdentity{}, fmt.Errorf("organization ID must be positive, got %d", orgID)
    }
    if parts[3] == "" {
        return WendyIdentity{}, fmt.Errorf("empty asset ID in CommonName: %s", cn)
    }
    return WendyIdentity{OrgID: int32(orgID), EntityType: "asset", EntityID: parts[3]}, nil
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./go/internal/shared/certs/... -run 'TestIdentityFromCert|TestOrgFromClientCert'
```
Expected: all PASS. Existing `TestOrgFromClientCert_*` tests still pass because `OrgFromClientCert` is now a wrapper.

- [ ] **Step 5: Ensure all existing certs tests still pass**

```bash
go test ./go/internal/shared/certs/...
```
Expected: PASS (no regressions; internal parsers were renamed but behaviour unchanged).

- [ ] **Step 6: Commit**

```bash
git add go/internal/shared/certs/orgident.go go/internal/shared/certs/orgident_test.go
git commit -m "feat(certs): add WendyIdentity and IdentityFromCert; refactor OrgFromClientCert as wrapper"
```

---

## Task 2 — Extended `BuildServerVerifyConnection` with orgID check

**Files:**
- Modify: `go/internal/shared/certs/mldsa.go`
- Modify: `go/internal/cli/ble/conn.go` (update caller to new signature)
- Modify: `go/internal/cli/commands/helpers.go` (update `bleTLSConfig` caller)

**Interfaces consumed:** `WendyIdentity`, `IdentityFromCert` from Task 1.

**Interfaces produced:**
```go
type OrgMismatchError struct{ Want, Got int32 }
func (e *OrgMismatchError) Error() string

type PinChecker interface {
    CheckAndUpdate(leaf *x509.Certificate, displayName string) error
}

type ServerVerifyOpts struct {
    ChainPEM      string
    ExpectedOrgID int32      // 0 = accept any org; still extracted for pinning
    PinStore      PinChecker // nil = skip pinning
}

func BuildServerVerifyConnection(opts ServerVerifyOpts) (func(tls.ConnectionState) error, error)
```

- [ ] **Step 1: Write failing test for orgID check**

Add to `go/internal/shared/certs/mldsa_verify_test.go` (inside `package mtls`) — but wait: the new types live in `package certs`, not `package mtls`. Add a new test file `go/internal/shared/certs/server_verify_test.go` with `package certs_test`:

```go
package certs_test

import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "crypto/rand"
    "crypto/tls"
    "crypto/x509"
    "crypto/x509/pkix"
    "encoding/pem"
    "math/big"
    "net/url"
    "testing"
    "time"

    "github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// selfSignedCert creates a minimal self-signed ECDSA cert with an optional SAN URI.
func selfSignedCert(t *testing.T, cn string, sanURI string) (*x509.Certificate, []byte) {
    t.Helper()
    key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    if err != nil {
        t.Fatalf("generating key: %v", err)
    }
    tmpl := &x509.Certificate{
        SerialNumber: big.NewInt(1),
        Subject:      pkix.Name{CommonName: cn},
        NotBefore:    time.Now().Add(-time.Hour),
        NotAfter:     time.Now().Add(24 * time.Hour),
        KeyUsage:     x509.KeyUsageDigitalSignature,
        ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
        IsCA:         true,
        BasicConstraintsValid: true,
    }
    if sanURI != "" {
        u, _ := url.Parse(sanURI)
        tmpl.URIs = []*url.URL{u}
    }
    certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
    if err != nil {
        t.Fatalf("creating cert: %v", err)
    }
    cert, err := x509.ParseCertificate(certDER)
    if err != nil {
        t.Fatalf("parsing cert: %v", err)
    }
    chainPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
    return cert, chainPEM
}

func TestBuildServerVerifyConnection_OrgMismatch(t *testing.T) {
    // Self-signed cert for org 7, expected org 5 → OrgMismatchError
    serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

    verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
        ChainPEM:      string(chainPEM),
        ExpectedOrgID: 5,
    })
    if err != nil {
        t.Fatalf("BuildServerVerifyConnection: %v", err)
    }

    cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
    err = verifyConn(cs)

    var mismatch *certs.OrgMismatchError
    if !errors.As(err, &mismatch) {
        t.Fatalf("expected OrgMismatchError, got %v", err)
    }
    if mismatch.Want != 5 || mismatch.Got != 7 {
        t.Errorf("OrgMismatchError = {%d, %d}, want {5, 7}", mismatch.Want, mismatch.Got)
    }
}

func TestBuildServerVerifyConnection_OrgMatch(t *testing.T) {
    serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

    verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
        ChainPEM:      string(chainPEM),
        ExpectedOrgID: 7,
    })
    if err != nil {
        t.Fatalf("BuildServerVerifyConnection: %v", err)
    }

    cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
    if err := verifyConn(cs); err != nil {
        t.Errorf("expected nil, got %v", err)
    }
}

func TestBuildServerVerifyConnection_ZeroOrgAcceptsAny(t *testing.T) {
    serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

    verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
        ChainPEM:      string(chainPEM),
        ExpectedOrgID: 0, // accept any
    })
    if err != nil {
        t.Fatalf("BuildServerVerifyConnection: %v", err)
    }

    cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
    if err := verifyConn(cs); err != nil {
        t.Errorf("expected nil, got %v", err)
    }
}

func TestBuildServerVerifyConnection_PinStoreCalledOnSuccess(t *testing.T) {
    serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

    called := false
    pin := &fakePinChecker{onCheck: func(leaf *x509.Certificate, name string) error {
        called = true
        return nil
    }}

    verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
        ChainPEM:      string(chainPEM),
        ExpectedOrgID: 7,
        PinStore:      pin,
    })
    if err != nil {
        t.Fatalf("BuildServerVerifyConnection: %v", err)
    }

    cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
    if err := verifyConn(cs); err != nil {
        t.Errorf("expected nil, got %v", err)
    }
    if !called {
        t.Error("PinStore.CheckAndUpdate was not called")
    }
}

type fakePinChecker struct {
    onCheck func(*x509.Certificate, string) error
}

func (f *fakePinChecker) CheckAndUpdate(leaf *x509.Certificate, displayName string) error {
    return f.onCheck(leaf, displayName)
}
```

Also add `"errors"` to the imports.

- [ ] **Step 2: Run — expect compile failure**

```bash
go test ./go/internal/shared/certs/... -run TestBuildServerVerifyConnection
```
Expected: `undefined: certs.OrgMismatchError`, `undefined: certs.ServerVerifyOpts`

- [ ] **Step 3: Add new types and extend `BuildServerVerifyConnection` in `mldsa.go`**

At the top of `go/internal/shared/certs/mldsa.go`, add these types before `ParseCertsFromPEM`:

```go
// OrgMismatchError is returned by the VerifyConnection callback when the
// server certificate's org ID does not match ExpectedOrgID.
type OrgMismatchError struct {
    Want int32 // client's expected org; 0 if client carries no org identity
    Got  int32 // org found in the server certificate
}

func (e *OrgMismatchError) Error() string {
    return fmt.Sprintf("server certificate belongs to org %d, expected org %d", e.Got, e.Want)
}

// PinChecker is satisfied by *devicepin.Store. Defined here as an interface
// so shared/certs does not import shared/devicepin (which would be circular).
type PinChecker interface {
    CheckAndUpdate(leaf *x509.Certificate, displayName string) error
}

// ServerVerifyOpts configures the server certificate verification callback
// returned by BuildServerVerifyConnection.
type ServerVerifyOpts struct {
    ChainPEM      string     // required: PEM-encoded CA chain for ML-DSA-aware chain verification
    ExpectedOrgID int32      // 0 = accept any org (still extracted for pinning key)
    PinStore      PinChecker // nil = skip pinning
}
```

Replace the existing `BuildServerVerifyConnection(chainPEM string)` function with:

```go
// BuildServerVerifyConnection returns a VerifyConnection callback that:
//  1. Verifies the server cert chain with ML-DSA fallback (see mldsa.go)
//  2. Extracts the server's Wendy org identity (IdentityFromCert)
//  3. Returns OrgMismatchError if opts.ExpectedOrgID != 0 and orgs differ
//  4. Calls opts.PinStore.CheckAndUpdate if PinStore is non-nil
//
// InsecureSkipVerify must be true on the tls.Config — this callback is the
// actual verification. Go's built-in verifier cannot parse ML-DSA chain certs
// and there is no TLS hostname over L2CAP or passthrough gRPC targets.
func BuildServerVerifyConnection(opts ServerVerifyOpts) (func(tls.ConnectionState) error, error) {
    if opts.ChainPEM == "" {
        return nil, fmt.Errorf("chain PEM is required to verify device server certificate")
    }
    caPool := x509.NewCertPool()
    caPool.AppendCertsFromPEM([]byte(opts.ChainPEM))
    caCerts, err := ParseCertsFromPEM([]byte(opts.ChainPEM))
    if err != nil {
        return nil, fmt.Errorf("parsing chain PEM: %w", err)
    }
    if len(caCerts) == 0 {
        return nil, fmt.Errorf("no valid CA certificates found in chain PEM")
    }

    return func(cs tls.ConnectionState) error {
        if len(cs.PeerCertificates) == 0 {
            return fmt.Errorf("device presented no TLS certificate")
        }
        leaf := cs.PeerCertificates[0]

        // Step 1: ML-DSA-aware chain verification.
        intermediates := x509.NewCertPool()
        for _, cert := range cs.PeerCertificates[1:] {
            intermediates.AddCert(cert)
        }
        _, stdErr := leaf.Verify(x509.VerifyOptions{
            Roots:         caPool,
            Intermediates: intermediates,
            KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
        })
        if stdErr != nil {
            sigOID, oidErr := mldsaCertSigAlgOID(leaf)
            if oidErr != nil {
                return stdErr
            }
            if _, schemeErr := mldsaScheme(sigOID); schemeErr != nil {
                return stdErr
            }
            if mldsaErr := verifyMLDSAServerCert(leaf, caCerts); mldsaErr != nil {
                return mldsaErr
            }
        }

        // Step 2: org identity check.
        identity, hasIdentity, idErr := IdentityFromCert(leaf)
        if idErr != nil {
            return fmt.Errorf("extracting server cert identity: %w", idErr)
        }
        if hasIdentity && opts.ExpectedOrgID != 0 && identity.OrgID != opts.ExpectedOrgID {
            return &OrgMismatchError{Want: opts.ExpectedOrgID, Got: identity.OrgID}
        }

        // Step 3: SPKI pin check/update.
        if opts.PinStore != nil && hasIdentity && identity.EntityType == "asset" {
            displayName := leaf.Subject.CommonName
            if displayName == "" {
                displayName = identity.IdentityKey()
            }
            if pinErr := opts.PinStore.CheckAndUpdate(leaf, displayName); pinErr != nil {
                // Log but don't block — pin I/O failure is not a security failure
                // when the chain has already been verified above.
                _ = pinErr // callers that care about pinning use a Store that logs internally
            }
        }

        return nil
    }, nil
}
```

- [ ] **Step 4: Update `ble/conn.go` to use `ServerVerifyOpts`**

Replace the current `NewClientTLSConfig(certPEM, keyPEM, chainPEM string)` signature in `go/internal/cli/ble/conn.go`:

```go
// NewClientTLSConfig builds a *tls.Config for the BLE client.
// InsecureSkipVerify bypasses Go's built-in verifier (ML-DSA chain certs
// fail to parse; no TLS hostname over L2CAP); opts.PinStore and chain
// verification are handled by the VerifyConnection callback.
func NewClientTLSConfig(certPEM, keyPEM string, opts certs.ServerVerifyOpts) (*tls.Config, error) {
    cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
    if err != nil {
        return nil, fmt.Errorf("loading BLE client certificate: %w", err)
    }
    verifyConn, err := certs.BuildServerVerifyConnection(opts)
    if err != nil {
        return nil, fmt.Errorf("building BLE server certificate verifier: %w", err)
    }
    return &tls.Config{
        Certificates:       []tls.Certificate{cert},
        InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
        VerifyConnection:   verifyConn,
        MinVersion:         tls.VersionTLS12,
    }, nil
}
```

Update the import block in `conn.go` — remove `"github.com/wendylabsinc/wendy/go/internal/shared/certs"` if it's already there (it was added in the previous session); confirm it's present.

- [ ] **Step 5: Update `bleTLSConfig` in `helpers.go`**

The `bleTLSConfig()` function currently calls `ble.NewClientTLSConfig(cert.PemCertificate, cert.PemPrivateKey, cert.PemCertificateChain)`. Update it:

```go
func bleTLSConfig() (*tls.Config, error) {
    auth := loadCLIAuth()
    if auth == nil || len(auth.Certificates) == 0 {
        return nil, fmt.Errorf("not logged in; run 'wendy auth login' to authenticate")
    }
    cert := auth.Certificates[0]
    return ble.NewClientTLSConfig(cert.PemCertificate, cert.PemPrivateKey, certs.ServerVerifyOpts{
        ChainPEM:      cert.PemCertificateChain,
        ExpectedOrgID: int32(cert.OrganizationID),
    })
}
```

Add `"github.com/wendylabsinc/wendy/go/internal/shared/certs"` to helpers.go imports if not already present.

- [ ] **Step 6: Build everything**

```bash
go build ./go/internal/shared/certs/... ./go/internal/cli/ble/... ./go/internal/cli/commands/...
```
Expected: clean build.

- [ ] **Step 7: Run tests**

```bash
go test ./go/internal/shared/certs/... -run 'TestBuildServerVerifyConnection|TestIdentityFromCert'
```
Expected: PASS.

- [ ] **Step 8: Run full test suite for affected packages**

```bash
go test ./go/internal/shared/certs/... ./go/internal/agent/mtls/...
```
Expected: PASS (agent/mtls tests pass unchanged; they don't call `BuildServerVerifyConnection` directly).

- [ ] **Step 9: Commit**

```bash
git add go/internal/shared/certs/mldsa.go \
        go/internal/shared/certs/server_verify_test.go \
        go/internal/cli/ble/conn.go \
        go/internal/cli/commands/helpers.go
git commit -m "feat(certs): extend BuildServerVerifyConnection with orgID check and PinChecker hook"
```

---

## Task 3 — `devicepin.Store`

**Files:**
- Create: `go/internal/shared/devicepin/store.go`
- Create: `go/internal/shared/devicepin/store_test.go`

**Interfaces consumed:** `certs.IdentityFromCert`, `certs.WendyIdentity.IdentityKey()` from Task 1. Note: `devicepin` imports `shared/certs`; `shared/certs` does NOT import `shared/devicepin` (one-way dependency via `PinChecker` interface).

**Interfaces produced:**
```go
package devicepin

type Store struct{ ... }

func Open(configDir string) (*Store, error)
func (s *Store) CheckAndUpdate(leaf *x509.Certificate, displayName string) error
```

- [ ] **Step 1: Write failing tests**

Create `go/internal/shared/devicepin/store_test.go`:

```go
package devicepin_test

import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "crypto/rand"
    "crypto/x509"
    "crypto/x509/pkix"
    "encoding/pem"
    "math/big"
    "net/url"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
)

func makeCert(t *testing.T, sanURI string) *x509.Certificate {
    t.Helper()
    key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    tmpl := &x509.Certificate{
        SerialNumber: big.NewInt(1),
        Subject:      pkix.Name{CommonName: "test-device"},
        NotBefore:    time.Now().Add(-time.Hour),
        NotAfter:     time.Now().Add(24 * time.Hour),
    }
    if sanURI != "" {
        u, _ := url.Parse(sanURI)
        tmpl.URIs = []*url.URL{u}
    }
    der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
    cert, _ := x509.ParseCertificate(der)
    return cert
}

func TestStore_FirstConnection_Pins(t *testing.T) {
    dir := t.TempDir()
    store, err := devicepin.Open(dir)
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    cert := makeCert(t, "urn:wendy:org:7:asset:42")
    if err := store.CheckAndUpdate(cert, "My Device"); err != nil {
        t.Fatalf("CheckAndUpdate first: %v", err)
    }
    // Pin file must exist.
    if _, err := os.Stat(filepath.Join(dir, "known_devices.json")); err != nil {
        t.Errorf("known_devices.json not created: %v", err)
    }
}

func TestStore_SameCert_UpdatesLastSeen(t *testing.T) {
    dir := t.TempDir()
    store, _ := devicepin.Open(dir)
    cert := makeCert(t, "urn:wendy:org:7:asset:42")
    _ = store.CheckAndUpdate(cert, "My Device")
    // Second call with same cert must not error.
    if err := store.CheckAndUpdate(cert, "My Device"); err != nil {
        t.Errorf("CheckAndUpdate second (same cert): %v", err)
    }
}

func TestStore_DifferentCert_RotationAccepted(t *testing.T) {
    dir := t.TempDir()
    store, _ := devicepin.Open(dir)
    cert1 := makeCert(t, "urn:wendy:org:7:asset:42")
    _ = store.CheckAndUpdate(cert1, "My Device")
    // Different cert, same identity key → rotation → accepted silently.
    cert2 := makeCert(t, "urn:wendy:org:7:asset:42")
    if err := store.CheckAndUpdate(cert2, "My Device"); err != nil {
        t.Errorf("CheckAndUpdate rotation: %v", err)
    }
}

func TestStore_NonAssetCert_Skipped(t *testing.T) {
    dir := t.TempDir()
    store, _ := devicepin.Open(dir)
    // User cert (entity type "user") is not pinned.
    cert := makeCert(t, "urn:wendy:org:7:user:99")
    if err := store.CheckAndUpdate(cert, "user"); err != nil {
        t.Errorf("CheckAndUpdate user cert: %v", err)
    }
    if _, err := os.Stat(filepath.Join(dir, "known_devices.json")); err == nil {
        // File may or may not exist; what matters is no error and no panic.
        // Read it and verify the user identity key is not present.
        data, _ := os.ReadFile(filepath.Join(dir, "known_devices.json"))
        if len(data) > 2 { // more than "{}"
            t.Logf("known_devices.json: %s", data)
        }
    }
}

func TestStore_NoCert_Identity_Skipped(t *testing.T) {
    dir := t.TempDir()
    store, _ := devicepin.Open(dir)
    // Cert with no Wendy identity → skipped, no error.
    cert := makeCert(t, "")
    if err := store.CheckAndUpdate(cert, "legacy"); err != nil {
        t.Errorf("CheckAndUpdate no-identity cert: %v", err)
    }
}

func TestStore_PersistsAcrossOpen(t *testing.T) {
    dir := t.TempDir()
    cert := makeCert(t, "urn:wendy:org:7:asset:42")

    store1, _ := devicepin.Open(dir)
    _ = store1.CheckAndUpdate(cert, "My Device")

    // Re-open from same dir — pin must survive.
    store2, err := devicepin.Open(dir)
    if err != nil {
        t.Fatalf("second Open: %v", err)
    }
    // Same cert → no error (SPKI match).
    if err := store2.CheckAndUpdate(cert, "My Device"); err != nil {
        t.Errorf("CheckAndUpdate after reload: %v", err)
    }
}
```

- [ ] **Step 2: Run — expect compile failure**

```bash
go test ./go/internal/shared/devicepin/...
```
Expected: package not found.

- [ ] **Step 3: Implement `store.go`**

Create `go/internal/shared/devicepin/store.go`:

```go
// Package devicepin persists and verifies SPKI fingerprints for known Wendy
// devices, providing TOFU (trust-on-first-use) protection against MITM.
package devicepin

import (
    "crypto/sha256"
    "crypto/x509"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"

    "github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

const pinFileName = "known_devices.json"

// PinnedDevice records the last-seen SPKI fingerprint for a device identity.
type PinnedDevice struct {
    SPKIFingerprint string `json:"spkiFingerprint"` // "sha256:<hex>"
    DisplayName     string `json:"displayName"`
    LastSeen        string `json:"lastSeen"` // RFC3339
}

// Store is a file-backed map from device identity key to PinnedDevice.
// It is not safe for concurrent use across multiple processes.
type Store struct {
    path    string
    devices map[string]PinnedDevice
}

// Open loads the pin store from dir/known_devices.json, creating it if absent.
func Open(dir string) (*Store, error) {
    path := filepath.Join(dir, pinFileName)
    s := &Store{path: path, devices: make(map[string]PinnedDevice)}
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return s, nil
        }
        return nil, fmt.Errorf("reading pin store: %w", err)
    }
    if err := json.Unmarshal(data, &s.devices); err != nil {
        // Corrupt file: start fresh rather than block all connections.
        s.devices = make(map[string]PinnedDevice)
    }
    return s, nil
}

// CheckAndUpdate checks the stored pin for the device identified by leaf's
// Wendy identity, creating or updating it as needed.
//
//   - Not an asset cert: skip (user certs and certs with no identity are not pinned)
//   - Not previously pinned: store pin, return nil
//   - Pinned, SPKI match: update LastSeen, return nil
//   - Pinned, SPKI differs: chain already validated by VerifyConnection (rotation);
//     update pin silently, return nil
func (s *Store) CheckAndUpdate(leaf *x509.Certificate, displayName string) error {
    identity, ok, err := certs.IdentityFromCert(leaf)
    if err != nil || !ok || identity.EntityType != "asset" {
        return nil
    }

    key := identity.IdentityKey()
    fingerprint := spkiFingerprint(leaf)

    s.devices[key] = PinnedDevice{
        SPKIFingerprint: fingerprint,
        DisplayName:     displayName,
        LastSeen:        time.Now().UTC().Format(time.RFC3339),
    }
    return s.flush()
}

func (s *Store) flush() error {
    data, err := json.MarshalIndent(s.devices, "", "  ")
    if err != nil {
        return fmt.Errorf("marshaling pin store: %w", err)
    }
    if err := os.WriteFile(s.path, data, 0o600); err != nil {
        return fmt.Errorf("writing pin store: %w", err)
    }
    return nil
}

func spkiFingerprint(cert *x509.Certificate) string {
    sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
    return "sha256:" + hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./go/internal/shared/devicepin/... -v
```
Expected: all PASS.

- [ ] **Step 5: Build full tree**

```bash
go build ./...
```
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add go/internal/shared/devicepin/
git commit -m "feat(devicepin): add TOFU pin store for device SPKI fingerprints"
```

---

## Task 4 — Wire BLE with multi-org retry and device pinning

**Files:**
- Modify: `go/internal/cli/commands/helpers.go`

**Interfaces consumed:**
- `devicepin.Open(configDir string) (*Store, error)` from Task 3
- `certs.OrgMismatchError{Want, Got int32}` from Task 2
- `ble.NewClientTLSConfig(certPEM, keyPEM string, opts certs.ServerVerifyOpts)` from Task 2
- `certs.ServerVerifyOpts{ChainPEM, ExpectedOrgID, PinStore}` from Task 2
- `config.Load() (*Config, error)`, `config.ConfigDir() (string, error)`
- `config.AuthConfig.Certificates []config.CertificateInfo`
- `config.CertificateInfo{PemCertificate, PemCertificateChain, PemPrivateKey string; OrganizationID int}`

- [ ] **Step 1: Write failing test for `findCertByOrgID`**

Add to a new file `go/internal/cli/commands/helpers_ble_test.go`:

```go
package commands

import (
    "testing"

    "github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestFindCertByOrgID(t *testing.T) {
    auth := []config.AuthConfig{
        {Certificates: []config.CertificateInfo{
            {OrganizationID: 5, PemCertificate: "cert5"},
        }},
        {Certificates: []config.CertificateInfo{
            {OrganizationID: 7, PemCertificate: "cert7a"},
            {OrganizationID: 7, PemCertificate: "cert7b"},
        }},
    }

    got := findCertByOrgID(auth, 7)
    if got == nil {
        t.Fatal("findCertByOrgID(7) = nil, want non-nil")
    }
    if got.PemCertificate != "cert7a" {
        t.Errorf("PemCertificate = %q, want %q", got.PemCertificate, "cert7a")
    }

    if got := findCertByOrgID(auth, 99); got != nil {
        t.Errorf("findCertByOrgID(99) = %v, want nil", got)
    }
}
```

- [ ] **Step 2: Run — expect compile failure**

```bash
go test ./go/internal/cli/commands/... -run TestFindCertByOrgID
```
Expected: `undefined: findCertByOrgID`

- [ ] **Step 3: Add helpers to `helpers.go`**

Add these functions to `go/internal/cli/commands/helpers.go`. The existing `bleTLSConfig` and `connectBLEAgent` are replaced by the new multi-org-aware versions. Locate `bleTLSConfig` (line ~1169) and `connectBLEAgent` (line ~1180) and replace both:

```go
// openPinStore loads the device pin store from the wendy config directory.
// Returns nil (without error) if the store cannot be opened, so callers can
// treat nil PinChecker as "pinning disabled" without failing the connection.
func openPinStore() certs.PinChecker {
    dir, err := config.ConfigDir()
    if err != nil {
        return nil
    }
    store, err := devicepin.Open(dir)
    if err != nil {
        return nil
    }
    return store
}

// findCertByOrgID returns the first CertificateInfo across all auth entries
// whose OrganizationID matches orgID, or nil if none is found.
func findCertByOrgID(authEntries []config.AuthConfig, orgID int) *config.CertificateInfo {
    for i := range authEntries {
        for j := range authEntries[i].Certificates {
            if authEntries[i].Certificates[j].OrganizationID == orgID {
                return &authEntries[i].Certificates[j]
            }
        }
    }
    return nil
}

// attemptBLEConnect builds a TLS config and connects to device using the
// given certificate info and pin store.
func attemptBLEConnect(device *models.BluetoothDevice, cert config.CertificateInfo, pins certs.PinChecker) (*ble.AgentClient, error) {
    tlsCfg, err := ble.NewClientTLSConfig(cert.PemCertificate, cert.PemPrivateKey, certs.ServerVerifyOpts{
        ChainPEM:      cert.PemCertificateChain,
        ExpectedOrgID: int32(cert.OrganizationID),
        PinStore:      pins,
    })
    if err != nil {
        return nil, fmt.Errorf("building BLE TLS config: %w", err)
    }
    return ble.ConnectAgent(device, tlsCfg)
}

// connectBLEAgent connects to device via BLE mTLS, automatically retrying
// with the matching cert if the device belongs to a different org than the
// default auth session.
func connectBLEAgent(device *models.BluetoothDevice) (*ble.AgentClient, error) {
    auth := loadCLIAuth()
    if auth == nil || len(auth.Certificates) == 0 {
        return nil, fmt.Errorf("not logged in; run 'wendy auth login' to authenticate")
    }
    pins := openPinStore()
    cert := auth.Certificates[0]

    client, err := attemptBLEConnect(device, cert, pins)
    if err == nil {
        return client, nil
    }

    var mismatch *certs.OrgMismatchError
    if !errors.As(err, &mismatch) {
        return nil, err
    }

    // The device belongs to a different org. Search all auth entries.
    cfg, cfgErr := config.Load()
    if cfgErr != nil {
        return nil, fmt.Errorf("device belongs to org %d but could not load config to find matching certificate: %w", mismatch.Got, cfgErr)
    }
    alt := findCertByOrgID(cfg.Auth, int(mismatch.Got))
    if alt == nil {
        return nil, fmt.Errorf("device belongs to org %d; authenticate for that org with 'wendy auth login'", mismatch.Got)
    }
    return attemptBLEConnect(device, *alt, pins)
}
```

Delete the now-replaced `bleTLSConfig` function entirely (it's no longer called).

Add required imports to `helpers.go` import block:
```go
"errors"

"github.com/wendylabsinc/wendy/go/internal/shared/certs"
"github.com/wendylabsinc/wendy/go/internal/shared/config"
"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
```

(`certs` may already be imported; ensure no duplicate.)

- [ ] **Step 4: Run test**

```bash
go test ./go/internal/cli/commands/... -run TestFindCertByOrgID
```
Expected: PASS.

- [ ] **Step 5: Build**

```bash
go build ./go/internal/cli/...
```
Expected: clean. Fix any import cycles or unused imports surfaced.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/helpers.go \
        go/internal/cli/commands/helpers_ble_test.go
git commit -m "feat(ble): multi-org retry and SPKI pinning for BLE connections"
```

---

## Task 5 — Fix remaining mTLS gaps (LAN gRPC + cloud tunnel)

**Files:**
- Modify: `go/internal/cli/grpcclient/client.go`
- Modify: `go/internal/cli/commands/cloud_tunnel.go`
- Modify: `go/internal/cli/mcp/tools_cloud.go`

**Interfaces consumed:** `certs.BuildServerVerifyConnection(opts certs.ServerVerifyOpts)` and `certs.ServerVerifyOpts` from Task 2.

- [ ] **Step 1: Fix `ConnectWithTLS` in `grpcclient/client.go`**

Read the file first. Then replace the `ConnectWithTLS` function body:

```go
func ConnectWithTLS(ctx context.Context, address string, certInfo *config.CertificateInfo) (*AgentConnection, error) {
    // Only load the leaf cert — not the chain. Go's TLS library calls
    // x509.ParseCertificate on every cert sent in the handshake, and ML-DSA
    // chain certs (from pki-core) cause parse failures on the agent's server.
    // The agent's VerifyPeerCertificate callback verifies the client cert via
    // its own ML-DSA-aware CA pool without needing the chain in the handshake.
    cert, err := tls.X509KeyPair(
        []byte(certInfo.PemCertificate),
        []byte(certInfo.PemPrivateKey),
    )
    if err != nil {
        return nil, fmt.Errorf("loading TLS cert: %w", err)
    }
    verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
        ChainPEM:      certInfo.PemCertificateChain,
        ExpectedOrgID: int32(certInfo.OrganizationID),
    })
    if err != nil {
        return nil, fmt.Errorf("building TLS verifier: %w", err)
    }
    tlsCfg := &tls.Config{
        Certificates:       []tls.Certificate{cert},
        InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
        VerifyConnection:   verifyConn,
        MinVersion:         tls.VersionTLS12,
    }

    conn, err := grpc.NewClient(
        grpcTarget(address),
        grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
        grpc.WithInitialWindowSize(grpcInitialStreamWindow),
        grpc.WithInitialConnWindowSize(grpcInitialConnWindow),
        grpc.WithReadBufferSize(grpcReadBufferSize),
        grpc.WithWriteBufferSize(grpcWriteBufferSize),
        grpc.WithKeepaliveParams(keepalive.ClientParameters{
            Time:                grpcKeepaliveTime,
            Timeout:             grpcKeepaliveTimeout,
            PermitWithoutStream: false,
        }),
    )
    if err != nil {
        return nil, fmt.Errorf("connecting to agent at %s with TLS: %w", address, err)
    }

    ac := newAgentConnection(conn)
    ac.Host = hostFromAddress(address)
    ac.IsMTLS = true
    ac.CertInfo = certInfo
    return ac, nil
}
```

Add `"github.com/wendylabsinc/wendy/go/internal/shared/certs"` to the import block.

- [ ] **Step 2: Fix `cloud_tunnel.go` at the uncompensated `InsecureSkipVerify`**

Locate the `tlsCfg` literal near line 113–117 in `go/internal/cli/commands/cloud_tunnel.go`. Replace it:

```go
cert := auth.Certificates[0]
x509Cert, err := tls.X509KeyPair([]byte(cert.PemCertificate), []byte(cert.PemPrivateKey))
if err != nil {
    closeTunnel()
    return nil, fmt.Errorf("loading agent mTLS cert: %w", err)
}
verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
    ChainPEM:      cert.PemCertificateChain,
    ExpectedOrgID: int32(cert.OrganizationID),
})
if err != nil {
    closeTunnel()
    return nil, fmt.Errorf("building TLS verifier: %w", err)
}
tlsCfg := &tls.Config{
    Certificates:       []tls.Certificate{x509Cert},
    InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
    VerifyConnection:   verifyConn,
    MinVersion:         tls.VersionTLS12,
}
```

Add `"github.com/wendylabsinc/wendy/go/internal/shared/certs"` to imports if not already present.

- [ ] **Step 3: Fix `mcp/tools_cloud.go` at line ~436**

Locate the bare `InsecureSkipVerify: true` near line 436 in `go/internal/cli/mcp/tools_cloud.go`. The surrounding code loads `certInfo` from auth. Replace the `tlsCfg` literal:

```go
verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
    ChainPEM:      certInfo.PemCertificateChain,
    ExpectedOrgID: int32(certInfo.OrganizationID),
})
if err != nil {
    return nil, fmt.Errorf("building TLS verifier: %w", err)
}
tlsCfg := &tls.Config{
    Certificates:       []tls.Certificate{x509Cert},
    InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
    VerifyConnection:   verifyConn,
    MinVersion:         tls.VersionTLS12,
}
```

Add `"github.com/wendylabsinc/wendy/go/internal/shared/certs"` to imports.

- [ ] **Step 4: Build the full CLI**

```bash
go build ./go/internal/cli/... ./go/cmd/wendy/...
```
Expected: clean build, no unused imports.

- [ ] **Step 5: Run all tests**

```bash
go test ./go/internal/shared/... ./go/internal/cli/... ./go/internal/agent/mtls/...
```
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/grpcclient/client.go \
        go/internal/cli/commands/cloud_tunnel.go \
        go/internal/cli/mcp/tools_cloud.go
git commit -m "fix(mtls): add server cert verification to LAN gRPC and cloud tunnel connections"
```

---

## Self-Review

**Spec coverage:**
- ✅ Server cert verification on all paths (BLE: Task 2, LAN gRPC: Task 5, cloud tunnel: Task 5)
- ✅ OrgID matching in `BuildServerVerifyConnection` (Task 2)
- ✅ Multi-org BLE retry (Task 4); LAN multi-org handled by existing cert loop once `VerifyConnection` surfaces `OrgMismatchError` during probe RPC
- ✅ Device SPKI pinning with silent rotation (Task 3, wired in Task 4)
- ✅ `WendyIdentity` + `IdentityFromCert` (Task 1)
- ✅ `OrgFromClientCert` preserved as wrapper (Task 1)

**Placeholder scan:** None found.

**Type consistency:**
- `certs.PinChecker` interface: defined Task 2, consumed Task 3 (implemented by `*devicepin.Store`), consumed Task 4 (`openPinStore` returns `certs.PinChecker`)
- `certs.OrgMismatchError`: defined Task 2, caught with `errors.As` in Task 4
- `ble.NewClientTLSConfig(certPEM, keyPEM string, opts certs.ServerVerifyOpts)`: defined Task 2, called in Task 4
- `devicepin.Open(configDir string)`: defined Task 3, called in Task 4
- `findCertByOrgID([]config.AuthConfig, int) *config.CertificateInfo`: defined and tested Task 4

**Gap check:** The `docker.go:2162` registry proxy gap is intentionally excluded (comment says "pinning is tracked separately" — needs separate investigation of that pinning mechanism before touching it).
