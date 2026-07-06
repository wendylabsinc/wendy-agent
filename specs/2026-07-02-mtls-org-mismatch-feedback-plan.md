# mTLS org-mismatch feedback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When an mTLS handshake to a device is rejected, tell the user plainly when the device belongs to an org they have no credentials for, instead of a generic "TLS handshake rejected" message.

**Architecture:** Capture the device's org from its server certificate inside the existing `VerifyConnection` callback (which already extracts it), thread it out onto the `AgentConnection`, and — on the LAN auto-TLS diagnostics rejection path — turn a genuine cross-org mismatch into a clear, actionable error.

**Tech Stack:** Go, `crypto/tls`, `crypto/x509`, `sync/atomic`, gRPC, standard `testing`.

## Global Constraints

- Work happens in the worktree `../wendyos-wt-mtls-org` on branch `jo/mtls-org-mismatch-feedback`. All paths below are relative to that worktree root.
- Org IDs are numeric `int32`; no human-readable org names exist locally and NO network call is made to resolve them.
- The user-facing command for gaining org credentials is `wendy cloud login` (not `wendy auth login`, which is an alias).
- Org capture in the verifier is **best-effort**: if identity extraction fails, capture nothing and behave exactly as today. Never let capture change verification outcomes.
- Go module lives under `go/`; run all Go commands from `go/` (e.g. `cd go && go test ./...`).

---

### Task 1: Capture the server org in the verifier

**Files:**
- Modify: `go/internal/shared/certs/mldsa.go` (add `OnServerIdentity` to `ServerVerifyOpts` at lines 55-61; fire it at the top of the `VerifyConnection` closure, after `leaf := cs.PeerCertificates[0]` at line 171)
- Test: `go/internal/shared/certs/server_verify_test.go` (reuse existing `selfSignedCert` helper)

**Interfaces:**
- Produces: `certs.ServerVerifyOpts.OnServerIdentity func(WendyIdentity)` — optional; when set, called once with the server leaf's Wendy identity BEFORE chain verification and the org-mismatch check, but only when `IdentityFromCert` returns `(id, true, nil)`.

- [ ] **Step 1: Write the failing tests**

Append to `go/internal/shared/certs/server_verify_test.go`:

```go
func TestBuildServerVerifyConnection_OnServerIdentityFiresOnSuccess(t *testing.T) {
	serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

	var got certs.WendyIdentity
	var calls int
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      string(chainPEM),
		ExpectedOrgID: 7,
		OnServerIdentity: func(id certs.WendyIdentity) {
			got = id
			calls++
		},
	})
	if err != nil {
		t.Fatalf("BuildServerVerifyConnection: %v", err)
	}

	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
	if err := verifyConn(cs); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("OnServerIdentity calls = %d, want 1", calls)
	}
	if got.OrgID != 7 {
		t.Errorf("captured OrgID = %d, want 7", got.OrgID)
	}
}

func TestBuildServerVerifyConnection_OnServerIdentityFiresOnMismatch(t *testing.T) {
	serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

	var got certs.WendyIdentity
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:         string(chainPEM),
		ExpectedOrgID:    5,
		OnServerIdentity: func(id certs.WendyIdentity) { got = id },
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
	if got.OrgID != 7 {
		t.Errorf("captured OrgID = %d, want 7 (must fire even though org check rejects)", got.OrgID)
	}
}

func TestBuildServerVerifyConnection_OnServerIdentityFiresOnChainFailure(t *testing.T) {
	// serverCert (org 9) verified against an UNRELATED CA chain → chain verify fails.
	serverCert, _ := selfSignedCert(t, "device", "urn:wendy:org:9:asset:1")
	_, unrelatedChain := selfSignedCert(t, "other-ca", "")

	var got certs.WendyIdentity
	var calls int
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:         string(unrelatedChain),
		ExpectedOrgID:    9,
		OnServerIdentity: func(id certs.WendyIdentity) { got = id; calls++ },
	})
	if err != nil {
		t.Fatalf("BuildServerVerifyConnection: %v", err)
	}

	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
	if err := verifyConn(cs); err == nil {
		t.Fatal("expected chain-verification error, got nil")
	}
	if calls != 1 || got.OrgID != 9 {
		t.Errorf("OnServerIdentity fired %d time(s) with OrgID %d, want 1 call with OrgID 9 (must fire before chain check)", calls, got.OrgID)
	}
}

func TestBuildServerVerifyConnection_OnServerIdentitySilentWhenNoIdentity(t *testing.T) {
	// CN carries no Wendy identity → sink must not be called.
	serverCert, chainPEM := selfSignedCert(t, "plain-cn", "")

	var calls int
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:         string(chainPEM),
		ExpectedOrgID:    0,
		OnServerIdentity: func(id certs.WendyIdentity) { calls++ },
	})
	if err != nil {
		t.Fatalf("BuildServerVerifyConnection: %v", err)
	}

	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
	if err := verifyConn(cs); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls != 0 {
		t.Errorf("OnServerIdentity calls = %d, want 0 (no Wendy identity in cert)", calls)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/shared/certs/ -run OnServerIdentity -v`
Expected: FAIL to compile — `unknown field 'OnServerIdentity' in struct literal of type certs.ServerVerifyOpts`.

- [ ] **Step 3: Add the field to `ServerVerifyOpts`**

In `go/internal/shared/certs/mldsa.go`, replace the struct (lines 55-61):

```go
// ServerVerifyOpts configures the server certificate verification callback
// returned by BuildServerVerifyConnection.
type ServerVerifyOpts struct {
	ChainPEM      string     // required: PEM-encoded CA chain for ML-DSA-aware chain verification
	ExpectedOrgID int32      // 0 = accept any org (still extracted for pinning key)
	PinStore      PinChecker // nil = skip pinning
	// OnServerIdentity, when non-nil, is called with the server leaf's Wendy
	// identity BEFORE chain verification and the org-mismatch check — so the
	// observed org is captured on every outcome (success, chain-verify failure,
	// org mismatch, and before any client-cert rejection). Best-effort: it is
	// not called when the cert carries no Wendy identity or identity parsing
	// fails, and it never affects the verification result.
	OnServerIdentity func(WendyIdentity)
}
```

- [ ] **Step 4: Fire the sink at the top of the closure**

In `go/internal/shared/certs/mldsa.go`, inside the returned closure, immediately after `leaf := cs.PeerCertificates[0]` (line 171) and before the `// Step 1: ML-DSA-aware chain verification.` comment, insert:

```go
		// Best-effort: surface the server's observed Wendy identity before any
		// verification step so callers can report a cross-org mismatch even when
		// the chain fails to verify or the peer later rejects our client cert.
		if opts.OnServerIdentity != nil {
			if id, ok, idErr := IdentityFromCert(leaf); ok && idErr == nil {
				opts.OnServerIdentity(id)
			}
		}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/shared/certs/ -run OnServerIdentity -v`
Expected: PASS (all four tests).

- [ ] **Step 6: Run the full certs package to check for regressions**

Run: `cd go && go test ./internal/shared/certs/`
Expected: `ok` — existing `TestBuildServerVerifyConnection_*` tests still pass (behavior unchanged when `OnServerIdentity` is nil).

- [ ] **Step 7: Commit**

```bash
cd ../wendyos-wt-mtls-org
git add go/internal/shared/certs/mldsa.go go/internal/shared/certs/server_verify_test.go
git commit -m "feat(certs): add OnServerIdentity sink to server cert verifier

Fires with the server leaf's Wendy identity before chain/org checks so
callers can capture the device's org on every handshake outcome."
```

---

### Task 2: Expose the observed server org on AgentConnection

**Files:**
- Modify: `go/internal/cli/grpcclient/client.go` (add `sync/atomic` import; add field to `AgentConnection` at lines 47-68; wire `OnServerIdentity` and assign the field in `ConnectWithTLSAndPins` at lines 97-147; add `ObservedServerOrg` method)
- Test: `go/internal/cli/grpcclient/observed_org_test.go` (create)

**Interfaces:**
- Consumes: `certs.ServerVerifyOpts.OnServerIdentity` (Task 1).
- Produces: `func (c *AgentConnection) ObservedServerOrg() (int32, bool)` — returns the org observed in the device's server cert during the handshake, and `false` if none was observed (plaintext connection, no Wendy identity, or handshake never reached the server cert). Safe to call after the first RPC returns.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/grpcclient/observed_org_test.go`:

```go
package grpcclient

import (
	"sync/atomic"
	"testing"
)

func TestObservedServerOrg_UnsetReturnsFalse(t *testing.T) {
	c := &AgentConnection{}
	if org, ok := c.ObservedServerOrg(); ok || org != 0 {
		t.Errorf("ObservedServerOrg() = (%d, %v), want (0, false)", org, ok)
	}
}

func TestObservedServerOrg_ReturnsStoredValue(t *testing.T) {
	c := &AgentConnection{observedServerOrg: new(atomic.Int32)}
	c.observedServerOrg.Store(7)
	if org, ok := c.ObservedServerOrg(); !ok || org != 7 {
		t.Errorf("ObservedServerOrg() = (%d, %v), want (7, true)", org, ok)
	}
}

func TestObservedServerOrg_ZeroStoredIsUnset(t *testing.T) {
	c := &AgentConnection{observedServerOrg: new(atomic.Int32)} // never stored → 0
	if org, ok := c.ObservedServerOrg(); ok || org != 0 {
		t.Errorf("ObservedServerOrg() = (%d, %v), want (0, false)", org, ok)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd go && go test ./internal/cli/grpcclient/ -run ObservedServerOrg -v`
Expected: FAIL to compile — `unknown field 'observedServerOrg'` and `c.ObservedServerOrg undefined`.

- [ ] **Step 3: Add the field**

In `go/internal/cli/grpcclient/client.go`, add `"sync/atomic"` to the import block, then add this field to the `AgentConnection` struct (after `TimeSyncService` at line 67, before the closing brace at line 68):

```go
	// observedServerOrg holds the org ID read from the device's server
	// certificate during the TLS handshake (set by the OnServerIdentity sink
	// wired in ConnectWithTLSAndPins). Written on the handshake goroutine, read
	// by callers after the first RPC returns; atomic makes that read race-free.
	// nil for connections that never install the sink (plaintext / NewFromConn).
	observedServerOrg *atomic.Int32
```

- [ ] **Step 4: Add the accessor method**

In `go/internal/cli/grpcclient/client.go`, add after the `Close` method (after line 203):

```go
// ObservedServerOrg returns the org ID observed in the device's server
// certificate during the TLS handshake, or (0, false) if none was observed
// (plaintext connection, cert without a Wendy identity, or a handshake that
// never reached the server certificate). Safe to call after the first RPC.
func (c *AgentConnection) ObservedServerOrg() (int32, bool) {
	if c.observedServerOrg == nil {
		return 0, false
	}
	v := c.observedServerOrg.Load()
	return v, v != 0
}
```

- [ ] **Step 5: Wire the sink in `ConnectWithTLSAndPins`**

In `go/internal/cli/grpcclient/client.go`, in `ConnectWithTLSAndPins`, replace the verifier construction (lines 110-114):

```go
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      certInfo.PemCertificateChain,
		ExpectedOrgID: int32(certInfo.OrganizationID),
		PinStore:      pins,
	})
```

with:

```go
	observedOrg := new(atomic.Int32)
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      certInfo.PemCertificateChain,
		ExpectedOrgID: int32(certInfo.OrganizationID),
		PinStore:      pins,
		OnServerIdentity: func(id certs.WendyIdentity) {
			if id.OrgID != 0 {
				observedOrg.Store(id.OrgID)
			}
		},
	})
```

Then, at the end of the function, replace the return-setup block (lines 142-146):

```go
	ac := newAgentConnection(conn)
	ac.Host = hostFromAddress(address)
	ac.IsMTLS = true
	ac.CertInfo = certInfo
	return ac, nil
```

with:

```go
	ac := newAgentConnection(conn)
	ac.Host = hostFromAddress(address)
	ac.IsMTLS = true
	ac.CertInfo = certInfo
	ac.observedServerOrg = observedOrg
	return ac, nil
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `cd go && go test ./internal/cli/grpcclient/ -run ObservedServerOrg -v`
Expected: PASS (all three tests).

- [ ] **Step 7: Build the package to catch import/compile issues**

Run: `cd go && go build ./internal/cli/grpcclient/`
Expected: no output (success).

- [ ] **Step 8: Commit**

```bash
cd ../wendyos-wt-mtls-org
git add go/internal/cli/grpcclient/client.go go/internal/cli/grpcclient/observed_org_test.go
git commit -m "feat(grpcclient): expose ObservedServerOrg from mTLS handshake

Wire the certs OnServerIdentity sink into an atomic on AgentConnection so
callers can read the device's cert org after a failed probe."
```

---

### Task 3: Surface the cross-org mismatch on the LAN connect path

**Files:**
- Modify: `go/internal/cli/commands/helpers.go` (add the `orgMismatchDeviceError` type + constructor + helpers near the other connection error types around lines 66-91; capture the observed org and branch in `connectWithAutoTLSDiagnostics` at lines 1011-1090)
- Test: `go/internal/cli/commands/org_mismatch_error_test.go` (create)

**Interfaces:**
- Consumes: `(*grpcclient.AgentConnection).ObservedServerOrg()` (Task 2); existing `config.CertificateInfo.OrganizationID int` (`go/internal/shared/config/config.go:64-71`); existing `loadAllCLICerts() []config.CertificateInfo` (`helpers.go:1364`).
- Produces: `newOrgMismatchDeviceError(deviceOrg int32, userCerts []config.CertificateInfo) error` and its message; `orgInCerts(org int32, certs []config.CertificateInfo) bool`.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/cli/commands/org_mismatch_error_test.go`:

```go
package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestOrgInCerts(t *testing.T) {
	certs := []config.CertificateInfo{{OrganizationID: 3}, {OrganizationID: 8}}
	if !orgInCerts(3, certs) {
		t.Error("orgInCerts(3) = false, want true")
	}
	if orgInCerts(9, certs) {
		t.Error("orgInCerts(9) = true, want false")
	}
	if orgInCerts(3, nil) {
		t.Error("orgInCerts(3, nil) = true, want false")
	}
}

func TestOrgMismatchDeviceError_Message(t *testing.T) {
	userCerts := []config.CertificateInfo{{OrganizationID: 3}, {OrganizationID: 8}}
	err := newOrgMismatchDeviceError(42, userCerts)
	msg := err.Error()

	for _, want := range []string{"org 42", "org 3", "org 8", "wendy cloud login"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	// Must NOT recommend switching the default org — that does not help on this path.
	if strings.Contains(msg, "list-orgs") || strings.Contains(strings.ToLower(msg), "default org") {
		t.Errorf("message must not suggest switching default org: %q", msg)
	}
}

func TestOrgMismatchDeviceError_SingleUserOrg(t *testing.T) {
	err := newOrgMismatchDeviceError(42, []config.CertificateInfo{{OrganizationID: 3}})
	msg := err.Error()
	if !strings.Contains(msg, "org 42") || !strings.Contains(msg, "org 3") {
		t.Errorf("message %q missing device org 42 or user org 3", msg)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/cli/commands/ -run 'OrgInCerts|OrgMismatchDeviceError' -v`
Expected: FAIL to compile — `undefined: orgInCerts`, `undefined: newOrgMismatchDeviceError`.

- [ ] **Step 3: Add the error type, constructor, and helpers**

In `go/internal/cli/commands/helpers.go`, after the `tlsHandshakeRejectedError` block (after line 91), insert:

```go
// orgMismatchDeviceError reports that the device's server certificate belongs
// to an org the user holds no credentials for — a genuine cross-org mismatch
// distinct from a same-org handshake failure (clock skew / stale cert).
type orgMismatchDeviceError struct {
	deviceOrg int32
	userOrgs  []int32 // distinct orgs the CLI has credentials for
}

func (e orgMismatchDeviceError) Error() string {
	parts := make([]string, len(e.userOrgs))
	for i, o := range e.userOrgs {
		parts[i] = fmt.Sprintf("org %d", o)
	}
	have := "none"
	if len(parts) > 0 {
		have = strings.Join(parts, ", ")
	}
	return fmt.Sprintf(
		"This device belongs to org %d; your credentials cover %s.\n"+
			"Your account isn't a member of org %d — run 'wendy cloud login' with an account that can access org %d.",
		e.deviceOrg, have, e.deviceOrg, e.deviceOrg)
}

// orgInCerts reports whether any of the given certs carries the org ID.
func orgInCerts(org int32, certs []config.CertificateInfo) bool {
	for i := range certs {
		if int32(certs[i].OrganizationID) == org {
			return true
		}
	}
	return false
}

// newOrgMismatchDeviceError builds an orgMismatchDeviceError, deduplicating the
// user's org IDs (in first-seen order) for the message.
func newOrgMismatchDeviceError(deviceOrg int32, userCerts []config.CertificateInfo) error {
	var userOrgs []int32
	seen := map[int32]bool{}
	for i := range userCerts {
		o := int32(userCerts[i].OrganizationID)
		if o == 0 || seen[o] {
			continue
		}
		seen[o] = true
		userOrgs = append(userOrgs, o)
	}
	return orgMismatchDeviceError{deviceOrg: deviceOrg, userOrgs: userOrgs}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run 'OrgInCerts|OrgMismatchDeviceError' -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Capture the observed org and branch in `connectWithAutoTLSDiagnostics`**

In `go/internal/cli/commands/helpers.go`, in `connectWithAutoTLSDiagnostics`:

(a) Add a capture variable. After `var plaintextAddrCertReject bool` (line 1041) and its sibling `var mtlsPortCertFails, mtlsPortNonCertFails int` (line 1042), add:

```go
				var observedDeviceOrg int32 // org read from the device's server cert on a failed mTLS probe (0 = none)
```

(b) Record the observed org right after a failed probe, before `conn.Close()`. Replace the block (lines 1065-1069):

```go
					recordMTLSErr(mtlsAddr, probeErr)
					if tlsDebug {
						fmt.Fprintf(os.Stderr, "[tls-debug] GetAgentVersion(%s) error: %v\n", mtlsAddr, probeErr)
					}
					conn.Close()
```

with:

```go
					recordMTLSErr(mtlsAddr, probeErr)
					if tlsDebug {
						fmt.Fprintf(os.Stderr, "[tls-debug] GetAgentVersion(%s) error: %v\n", mtlsAddr, probeErr)
					}
					if org, ok := conn.ObservedServerOrg(); ok {
						observedDeviceOrg = org
					}
					conn.Close()
```

(c) Prefer the mismatch error in the rejection branch. Replace the block (lines 1083-1085):

```go
			if plaintextAddrCertReject || (mtlsPortCertFails > 0 && mtlsPortNonCertFails == 0) {
				return nil, lastMTLSErr, newTLSHandshakeRejectedError(lastMTLSErr)
			}
```

with:

```go
			if plaintextAddrCertReject || (mtlsPortCertFails > 0 && mtlsPortNonCertFails == 0) {
				// A genuine cross-org mismatch (device's org is one we hold no cert
				// for) gets a clear, actionable message. A same-org failure (observed
				// org is one we have, e.g. clock skew / stale cert) or no observed org
				// falls through to the generic handshake-rejected error, which
				// connectToAgent already post-processes with clock-skew and
				// refresh-certs remedies.
				if observedDeviceOrg != 0 && !orgInCerts(observedDeviceOrg, allCerts) {
					return nil, lastMTLSErr, newOrgMismatchDeviceError(observedDeviceOrg, allCerts)
				}
				return nil, lastMTLSErr, newTLSHandshakeRejectedError(lastMTLSErr)
			}
```

- [ ] **Step 6: Build and run the commands package tests**

Run: `cd go && go build ./internal/cli/commands/ && go test ./internal/cli/commands/ -run 'OrgInCerts|OrgMismatchDeviceError|AutoTLS|ConnectWithAutoTLS'`
Expected: `ok` — new tests pass and no existing connect-diagnostics test regressed. (If no `AutoTLS`/`ConnectWithAutoTLS` tests exist, the run simply exercises the new ones plus the build.)

- [ ] **Step 7: Commit**

```bash
cd ../wendyos-wt-mtls-org
git add go/internal/cli/commands/helpers.go go/internal/cli/commands/org_mismatch_error_test.go
git commit -m "feat(cli): report cross-org device on mTLS rejection

When the LAN connect path exhausts all certs and the device's server cert
belongs to an org we hold no credential for, tell the user to run
'wendy cloud login' for that org instead of the generic rejection message."
```

---

### Task 4: Full build, vet, and regression sweep

**Files:** none (verification only).

- [ ] **Step 1: Vet the touched packages**

Run: `cd go && go vet ./internal/shared/certs/ ./internal/cli/grpcclient/ ./internal/cli/commands/`
Expected: no output (success).

- [ ] **Step 2: Run the full test suites for the touched packages**

Run: `cd go && go test ./internal/shared/certs/ ./internal/cli/grpcclient/ ./internal/cli/commands/`
Expected: three `ok` lines.

- [ ] **Step 3: Build the whole Go module**

Run: `cd go && go build ./...`
Expected: no output (success).

- [ ] **Step 4: Confirm no stray commits/uncommitted changes**

Run: `cd ../wendyos-wt-mtls-org && git status --short`
Expected: empty (all work committed across Tasks 1-3).

---

## Notes for the implementer

- **Why capture before the checks (Task 1):** the existing `IdentityFromCert` call at `mldsa.go:197` runs only *after* chain verification succeeds, so the two rejection paths we care about (chain-verify failure; the device rejecting our client cert) never reach it. Firing the sink at the top guarantees capture on all outcomes. The duplicate `IdentityFromCert` call (top for the sink, plus the existing one at line 197 for the grace/pin logic) is intentional and cheap — do not remove the line-197 call.
- **Why no `errors.As(&certs.OrgMismatchError)` in Task 3:** capturing the observed org via the sink already covers the client-rejects-device (`OrgMismatchError`) case — the sink fires before that error is returned — so a separate `errors.As` check would be redundant. The single `observedDeviceOrg` gate handles chain-fail, client-rejects-device, and device-rejects-us uniformly.
- **Scope:** cloud-tunnel (`cloud_tunnel.go`), MCP (`tools_cloud.go`), and BLE (`ble/conn.go`) callers all get the capture for free via Task 1, but only the LAN path (`connectWithAutoTLSDiagnostics`) gets the enriched message in this plan. BLE already surfaces `OrgMismatchError` via `errors.As`. Extending the message to tunnel/MCP is a deliberate follow-up, not part of this plan.
