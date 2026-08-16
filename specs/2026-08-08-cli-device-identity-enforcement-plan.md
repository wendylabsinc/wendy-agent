# CLI Device Identity Enforcement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Wendy CLI refuse a LAN device whose certificate does not match the identity pinned for the name the user asked for, so a spoofed mDNS answer can no longer redirect a connection to another same-CA host or downgrade it to plaintext.

**Architecture:** One enforcement point (`ExpectedIdentity` inside `BuildServerVerifyConnection`'s `VerifyConnection` callback) fed by one state source (a hostname-keyed `config.DevicePin` that cloud can seed authoritatively). The dial ladder consults the pin before dialing, aborts on mismatch, and skips its plaintext rung entirely for a pinned host.

**Tech Stack:** Go 1.x, `crypto/tls` + `crypto/x509`, gRPC (`grpc.NewClient`), Cobra CLI, standard `testing` package with table-driven tests.

**Spec:** `specs/2026-08-08-cli-device-identity-enforcement-design.md`

## Global Constraints

- Branch: `jo/cli-device-identity-enforcement`, worktree `/Users/joannisorlandos/git/wendy/wendyos-device-identity`, based on `ed/lkg-connect-cache` (PR #1619).
- Run tests from the `go/` directory. Full suite: `make test` (`go test ./... -v -count=1 -timeout 120s`). Single package during TDD: `go test ./internal/shared/certs/ -run TestName -v`.
- Format with `gofmt -w -s .` (`make fmt`) before every commit; lint with `make lint`.
- Enforcement must live in `tls.Config.VerifyConnection`, never `VerifyPeerCertificate` — a resumed TLS 1.3 handshake skips the latter, and `tlscache` makes resumption the common path.
- Never widen the trust surface on an unpinned host: an unpinned host keeps grace mode and the plaintext fallback exactly as today.
- Key resolution may read mDNS/TXT-derived data; trust decisions may not. Choosing the wrong pin key must only ever produce a stricter outcome.
- Pin JSON fields are additive and `omitempty`; a config written by an older CLI must keep working (treated as `source: "lan"`, no asset constraint).
- Commit messages end with the repo's `Co-Authored-By` / `Claude-Session` trailers.
- Do not claim on-device verification. The PR body lists it as outstanding.

---

### Task 0: Land the prerequisite pin commit

`jo/pin-device-identity` is an existing unpushed commit that this plan builds on (asset id in the pin, the `OnVerifiedServerIdentity` sink, `ClearDevicePin`, the unprovisioned challenge). It goes out as its own PR against `main` first, then merges into this branch.

**Files:**
- No new files. Verifies and publishes an existing commit, then merges it here.

**Interfaces:**
- Produces: `certs.ServerVerifyOpts.OnVerifiedServerIdentity func(WendyIdentity)`; `(*grpcclient.AgentConnection).ObservedServerIdentity() (certs.WendyIdentity, bool)`; `config.DevicePin{OrgID int, CloudGRPC string, AssetID string}`; `config.PinAdoptAsset`; `(*config.Config).EvaluateDevicePin(hostname string, orgID int, cloudGRPC, assetID string) PinVerdict`; `(*config.Config).SetDevicePin(hostname string, orgID int, cloudGRPC, assetID string)`; `(*config.Config).ClearDevicePin(hostname string)`; `commands.observedDeviceIdentity{mTLS bool, orgID int, assetID string}`; `commands.enforceDeviceIdentity(hostname string, obs observedDeviceIdentity) error`; `commands.clearDevicePinForRepin(hostname string)`.

- [ ] **Step 1: Verify the existing commit builds and its tests pass**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos
git worktree add /tmp/wendyos-pin-verify jo/pin-device-identity
cd /tmp/wendyos-pin-verify/go
gofmt -l . | tee /tmp/gofmt-out.txt
go build ./... && go test ./internal/shared/config/ ./internal/shared/certs/ ./internal/cli/commands/ ./internal/cli/grpcclient/ -count=1
```

Expected: `gofmt -l` prints nothing, build succeeds, all four packages PASS. If anything fails, fix it on `jo/pin-device-identity` and amend before continuing — do not push a red branch.

- [ ] **Step 2: Push and open the PR**

```bash
cd /tmp/wendyos-pin-verify
git push -u origin jo/pin-device-identity
gh pr create --base main --title "Pin device asset id, and refuse a pinned device that goes unprovisioned" --body "$(cat <<'EOF'
## Summary
Extends the WDY-1149 device pin from (organisation, cloud host) to (organisation, cloud host, **asset id**), so a different physical device answering at a pinned hostname is detected — not just a different trust domain.

- `DevicePin.AssetID` from the verified server cert's `urn:wendy:org:<org>:asset:<id>` SAN.
- New `OnVerifiedServerIdentity` sink in `BuildServerVerifyConnection`, fired only after chain + org checks pass — unlike `OnServerIdentity`, it is safe to pin against.
- `PinAdoptAsset`: a pin written before asset ids existed backfills silently instead of being challenged.
- A hostname previously seen enrolled that now answers with no certificate at all is challenged rather than silently accepted.
- `wendy device set-default <host>` clears the pin first, so the "re-run set-default to re-pin" advice in every refusal actually works.

## Verification
- [x] Unit tests: pin evaluation (match / mismatch / legacy backfill / empty observed asset), verified-identity sink, unprovisioned challenge.
- [ ] On-device — not verified.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW
EOF
)"
cd / && git -C /Users/joannisorlandos/git/wendy/wendyos worktree remove /tmp/wendyos-pin-verify
```

- [ ] **Step 3: Merge it into this branch**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity
git merge --no-ff jo/pin-device-identity -m "Merge jo/pin-device-identity (asset-id device pin) into identity enforcement"
```

Conflicts are expected in `go/internal/cli/commands/helpers.go` (both #1619 and the pin commit edit `connectToAgent`) and possibly `device_pin.go`. Resolve by keeping **both**: #1619's LKG fast path *and* the pin commit's `enforceDevicePin` call placed before the update check.

- [ ] **Step 4: Verify the merge**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && go build ./... && go test ./internal/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit if the merge needed resolution**

```bash
git add -A && git commit --no-edit
```

---

### Task 1: `ExpectedIdentity` in the server verifier

**Files:**
- Modify: `go/internal/shared/certs/mldsa.go` (add `IdentityMismatchError` near `OrgMismatchError` at :38-47; add `ExpectedIdentity` to `ServerVerifyOpts` at :57-68; add the check after the org check at :224)
- Test: `go/internal/shared/certs/server_verify_test.go` (reuses the existing `selfSignedCert` helper at :21)

**Interfaces:**
- Consumes: `certs.WendyIdentity{OrgID int32, EntityType string, EntityID string}` and `certs.IdentityFromCert` from `orgident.go`.
- Produces: `certs.IdentityMismatchError{WantOrg int32, WantAsset string, GotOrg int32, GotAsset string}` and `certs.ServerVerifyOpts.ExpectedIdentity *certs.WendyIdentity`.

- [ ] **Step 1: Write the failing test**

Append to `go/internal/shared/certs/server_verify_test.go`:

```go
func TestBuildServerVerifyConnection_ExpectedIdentity(t *testing.T) {
	want := certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"}

	cases := []struct {
		name        string
		sanURI      string
		wantErr     bool
		wantGotAsst string
	}{
		{name: "exact match", sanURI: "urn:wendy:org:7:asset:42"},
		{name: "different asset, same org", sanURI: "urn:wendy:org:7:asset:43", wantErr: true, wantGotAsst: "43"},
		{name: "same asset, different org", sanURI: "urn:wendy:org:9:asset:42", wantErr: true, wantGotAsst: "42"},
		{name: "user URN is not an asset", sanURI: "urn:wendy:org:7:user:42", wantErr: true},
		{name: "no wendy identity at all", sanURI: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serverCert, chainPEM := selfSignedCert(t, "device", tc.sanURI)
			expected := want
			verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
				ChainPEM:         string(chainPEM),
				ExpectedIdentity: &expected,
			})
			if err != nil {
				t.Fatalf("BuildServerVerifyConnection: %v", err)
			}

			err = verifyConn(tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}})

			var mismatch *certs.IdentityMismatchError
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
				return
			}
			if !errors.As(err, &mismatch) {
				t.Fatalf("want IdentityMismatchError, got %v", err)
			}
			if mismatch.WantOrg != 7 || mismatch.WantAsset != "42" {
				t.Errorf("want side = org %d asset %q, want org 7 asset \"42\"", mismatch.WantOrg, mismatch.WantAsset)
			}
			if mismatch.GotAsset != tc.wantGotAsst {
				t.Errorf("GotAsset = %q, want %q", mismatch.GotAsset, tc.wantGotAsst)
			}
		})
	}
}

// TestBuildServerVerifyConnection_ExpectedIdentityNilIsPermissive locks in that
// the new field is opt-in: with it unset, a no-URN cert still passes (grace
// mode), which is what keeps unpinned legacy devices working.
func TestBuildServerVerifyConnection_ExpectedIdentityNil(t *testing.T) {
	serverCert, chainPEM := selfSignedCert(t, "device", "")
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      string(chainPEM),
		ExpectedOrgID: 7,
	})
	if err != nil {
		t.Fatalf("BuildServerVerifyConnection: %v", err)
	}
	if err := verifyConn(tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}); err != nil {
		t.Fatalf("grace mode should accept a no-URN cert, got %v", err)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd go && go test ./internal/shared/certs/ -run TestBuildServerVerifyConnection_ExpectedIdentity -v`
Expected: FAIL — `unknown field ExpectedIdentity in struct literal` and `undefined: certs.IdentityMismatchError`.

- [ ] **Step 3: Add the error type**

In `go/internal/shared/certs/mldsa.go`, after `OrgMismatchError.Error()` (:47):

```go
// IdentityMismatchError is returned by the VerifyConnection callback when the
// server certificate does not carry the exact asset identity the caller
// required via ServerVerifyOpts.ExpectedIdentity. Unlike OrgMismatchError this
// is never subject to grace mode: a certificate with no Wendy identity is a
// mismatch, because the caller asked for a specific device and got something
// that cannot prove it is that device.
type IdentityMismatchError struct {
	WantOrg   int32
	WantAsset string
	GotOrg    int32  // 0 when the certificate carried no Wendy identity
	GotAsset  string // "" when the certificate carried no Wendy asset identity
}

func (e *IdentityMismatchError) Error() string {
	if e.GotAsset == "" {
		return fmt.Sprintf("device presented no wendy asset identity, expected asset %s in org %d", e.WantAsset, e.WantOrg)
	}
	return fmt.Sprintf("device presented asset %s in org %d, expected asset %s in org %d",
		e.GotAsset, e.GotOrg, e.WantAsset, e.WantOrg)
}
```

- [ ] **Step 4: Add the opts field**

In `ServerVerifyOpts`, after `PinStore`:

```go
	// ExpectedIdentity, when non-nil, requires the server leaf to carry an
	// "asset" Wendy identity whose org and entity id match it exactly. This is
	// the CLI-side counterpart of agent/mtls.NewClientTLSConfigExpectingPeer:
	// chain validity alone only proves the peer holds a cert from a trusted CA,
	// not that it is the device the caller asked for, so any other same-CA cert
	// could otherwise answer at an mDNS-advertised address.
	//
	// Grace mode does not apply when this is set — a cert with no Wendy
	// identity is a mismatch, not a legacy device to be tolerated.
	ExpectedIdentity *WendyIdentity
```

- [ ] **Step 5: Add the check**

In `BuildServerVerifyConnection`, immediately after the existing org check (the `if hasIdentity && opts.ExpectedOrgID != 0 ...` block at :224) and before the SPKI step:

```go
		// Step 2b: exact device identity check. Deliberately after the org check
		// so a cross-org impostor still reports OrgMismatchError, whose remedy
		// (fetch that org's cert) differs from this one's (wrong device).
		if opts.ExpectedIdentity != nil {
			if !hasIdentity || identity.EntityType != "asset" {
				return &IdentityMismatchError{
					WantOrg:   opts.ExpectedIdentity.OrgID,
					WantAsset: opts.ExpectedIdentity.EntityID,
					GotOrg:    identity.OrgID,
				}
			}
			if identity.OrgID != opts.ExpectedIdentity.OrgID || identity.EntityID != opts.ExpectedIdentity.EntityID {
				return &IdentityMismatchError{
					WantOrg:   opts.ExpectedIdentity.OrgID,
					WantAsset: opts.ExpectedIdentity.EntityID,
					GotOrg:    identity.OrgID,
					GotAsset:  identity.EntityID,
				}
			}
		}
```

- [ ] **Step 6: Run the tests**

Run: `cd go && go test ./internal/shared/certs/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && gofmt -w -s . && cd ..
git add go/internal/shared/certs/
git commit -m "certs: add ExpectedIdentity to the server verifier

Chain validity alone proves only that a peer holds a cert from a trusted
CA, not that it is the device the caller asked for. ExpectedIdentity
requires an exact asset URN match, with no grace mode, mirroring the
agent's NewClientTLSConfigExpectingPeer.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
```

---

### Task 2: Surface the mismatch on `AgentConnection`

`grpc.NewClient` is lazy, so a `VerifyConnection` error surfaces mangled inside the first RPC. Capture the typed error the same way `observedServerOrg` is captured.

**Files:**
- Modify: `go/internal/cli/grpcclient/client.go` (`AgentConnection` struct at :74; `newAgentTLSConfig` at :152; `ConnectWithTLSAndPins` at :226)
- Test: `go/internal/cli/grpcclient/identity_mismatch_test.go` (create)

**Interfaces:**
- Consumes: `certs.IdentityMismatchError`, `certs.ServerVerifyOpts.ExpectedIdentity` (Task 1).
- Produces: `newAgentTLSConfig(address string, certInfo *config.CertificateInfo, pins certs.PinChecker, observedOrg *atomic.Int32, observedIdentity *atomic.Pointer[certs.WendyIdentity], expected *certs.WendyIdentity, mismatch *atomic.Pointer[certs.IdentityMismatchError]) (*tls.Config, error)`; `ConnectWithTLSExpecting(ctx context.Context, address string, certInfo *config.CertificateInfo, pins certs.PinChecker, expected *certs.WendyIdentity) (*AgentConnection, error)`; `(*AgentConnection).IdentityMismatch() (*certs.IdentityMismatchError, bool)`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/grpcclient/identity_mismatch_test.go`:

```go
package grpcclient

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// TestIdentityMismatchNilConnection covers the accessor's contract on a
// connection that never installed the sink (plaintext / NewFromConn).
func TestIdentityMismatchNilConnection(t *testing.T) {
	c := &AgentConnection{}
	if got, ok := c.IdentityMismatch(); ok || got != nil {
		t.Fatalf("want (nil, false), got (%v, %v)", got, ok)
	}
}

// TestIdentityMismatchRecorded covers the sink → accessor round trip that the
// dial ladder relies on to distinguish "wrong device" from "handshake failed".
func TestIdentityMismatchRecorded(t *testing.T) {
	c := newAgentConnection(nil)
	sink := identityMismatchSink(c.identityMismatch)
	sink(&certs.IdentityMismatchError{WantOrg: 7, WantAsset: "42", GotOrg: 7, GotAsset: "43"})

	got, ok := c.IdentityMismatch()
	if !ok {
		t.Fatal("want ok=true after sink fired")
	}
	if got.WantAsset != "42" || got.GotAsset != "43" {
		t.Fatalf("want 42→43, got %s→%s", got.WantAsset, got.GotAsset)
	}
}

// TestVerifyConnectionNotVerifyPeerCertificate locks in the resumption-safety
// invariant: a resumed TLS 1.3 handshake skips VerifyPeerCertificate entirely
// and calls only VerifyConnection, so the identity check must live there or it
// silently stops running after the first connect.
func TestVerifyConnectionNotVerifyPeerCertificate(t *testing.T) {
	cfg, err := newAgentTLSConfig("dev.local:50052", testCertInfo(t), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("newAgentTLSConfig: %v", err)
	}
	if cfg.VerifyConnection == nil {
		t.Error("VerifyConnection must be set")
	}
	if cfg.VerifyPeerCertificate != nil {
		t.Error("VerifyPeerCertificate must stay nil: a resumed handshake skips it")
	}
}
```

Add a `testCertInfo` helper in the same file that builds a `*config.CertificateInfo` with a self-signed leaf, key, and chain PEM. Copy the generation approach from `go/internal/shared/certs/server_verify_test.go:21` (`selfSignedCert`), additionally PEM-encoding the EC private key with `x509.MarshalECPrivateKey` so `tls.X509KeyPair` accepts it.

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd go && go test ./internal/cli/grpcclient/ -run 'TestIdentityMismatch|TestVerifyConnection' -v`
Expected: FAIL — `undefined: identityMismatchSink`, unknown field `identityMismatch`, and a signature mismatch on `newAgentTLSConfig`.

- [ ] **Step 3: Add the field, sink, and accessor**

In `AgentConnection` (after `observedServerIdentity`):

```go
	// identityMismatch holds the typed rejection raised inside VerifyConnection
	// when the peer failed ExpectedIdentity. gRPC's lazy dial mangles that error
	// into the first RPC's status, so the ladder reads it from here instead of
	// string-matching. nil for connections that never install the sink.
	identityMismatch *atomic.Pointer[certs.IdentityMismatchError]
```

```go
// identityMismatchSink returns the callback that records an ExpectedIdentity
// rejection. dst may be nil (enforcement not requested), in which case the
// returned sink is a no-op, so callers need no nil check.
func identityMismatchSink(dst *atomic.Pointer[certs.IdentityMismatchError]) func(*certs.IdentityMismatchError) {
	return func(e *certs.IdentityMismatchError) {
		if dst == nil || e == nil {
			return
		}
		dst.Store(e)
	}
}

// IdentityMismatch reports the ExpectedIdentity rejection this connection hit,
// if any. It is the ladder's signal that the *device* is wrong — as opposed to
// our certificate being wrong — and therefore that retrying with other certs or
// other ports is pointless.
func (c *AgentConnection) IdentityMismatch() (*certs.IdentityMismatchError, bool) {
	if c.identityMismatch == nil {
		return nil, false
	}
	e := c.identityMismatch.Load()
	return e, e != nil
}
```

In `newAgentConnection`, initialise `identityMismatch: new(atomic.Pointer[certs.IdentityMismatchError])`.

- [ ] **Step 4: Thread it through the TLS config**

Extend `newAgentTLSConfig`'s signature to the one in **Interfaces** above, pass `ExpectedIdentity: expected` into `certs.BuildServerVerifyConnection`, and wrap the returned verifier so a typed mismatch is recorded before being returned:

```go
	sink := identityMismatchSink(mismatch)
	inner := verifyConn
	verifyConn = func(cs tls.ConnectionState) error {
		err := inner(cs)
		var im *certs.IdentityMismatchError
		if errors.As(err, &im) {
			sink(im)
		}
		return err
	}
```

Place this wrap **before** the existing `WENDY_TLS_DEBUG` and `SetResumed` wraps so it observes the verifier's own result.

- [ ] **Step 5: Add the public entry point**

```go
// ConnectWithTLSExpecting is ConnectWithTLSAndPins with a required peer
// identity. A nil expected is exactly ConnectWithTLSAndPins.
func ConnectWithTLSExpecting(ctx context.Context, address string, certInfo *config.CertificateInfo, pins certs.PinChecker, expected *certs.WendyIdentity) (*AgentConnection, error) {
```

Move `ConnectWithTLSAndPins`'s body into it, add the two new atomics, and make `ConnectWithTLSAndPins` delegate with `expected = nil` so every existing caller is unchanged.

- [ ] **Step 6: Run the tests**

Run: `cd go && go test ./internal/cli/grpcclient/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && gofmt -w -s . && cd ..
git add go/internal/cli/grpcclient/
git commit -m "grpcclient: expose ExpectedIdentity rejections as a typed signal

gRPC's lazy dial mangles a VerifyConnection error into the first RPC's
status. Record the typed IdentityMismatchError inside the verifier so the
dial ladder can tell 'wrong device' from 'wrong cert' without string
matching.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
```

---

### Task 3: Pin source precedence

**Files:**
- Modify: `go/internal/shared/config/devicepin.go`
- Test: `go/internal/shared/config/devicepin_test.go`

**Interfaces:**
- Consumes: `config.DevicePin`, `config.PinVerdict`, `EvaluateDevicePin`, `SetDevicePin`, `PinAdoptAsset` (Task 0).
- Produces: `config.PinSourceLAN = "lan"`, `config.PinSourceCloud = "cloud"`; `DevicePin.Source string`; `(*Config).SetDevicePinFrom(hostname string, orgID int, cloudGRPC, assetID, source string)`; `(*Config).PinSource(hostname string) string`.

Cloud writes never consult the verdict — the cloud path calls `SetDevicePinFrom(..., PinSourceCloud)` unconditionally, which is what "cloud overwrites anything" means. `EvaluateDevicePin` therefore only ever judges LAN observations, and gains exactly one new rule: **a LAN observation must not backfill an asset id into a cloud-sourced pin.**

- [ ] **Step 1: Write the failing test**

Append to `go/internal/shared/config/devicepin_test.go`:

```go
func TestDevicePinSourcePrecedence(t *testing.T) {
	const host = "wendy-thor.local"

	t.Run("cloud write overwrites a lan pin", func(t *testing.T) {
		c := &Config{}
		c.SetDevicePinFrom(host, 7, "grpc.wendy.dev:443", "42", PinSourceLAN)
		c.SetDevicePinFrom(host, 7, "grpc.wendy.dev:443", "99", PinSourceCloud)

		pin, _ := c.DevicePinFor(host)
		if pin.AssetID != "99" || pin.Source != PinSourceCloud {
			t.Fatalf("want asset 99 from cloud, got asset %q from %q", pin.AssetID, pin.Source)
		}
	})

	t.Run("lan observation conflicting with a cloud pin is a mismatch", func(t *testing.T) {
		c := &Config{}
		c.SetDevicePinFrom(host, 7, "grpc.wendy.dev:443", "42", PinSourceCloud)
		if v := c.EvaluateDevicePin(host, 7, "grpc.wendy.dev:443", "43"); v != PinMismatch {
			t.Fatalf("want PinMismatch, got %v", v)
		}
	})

	t.Run("lan never backfills an asset into a cloud pin", func(t *testing.T) {
		c := &Config{}
		c.SetDevicePinFrom(host, 7, "grpc.wendy.dev:443", "", PinSourceCloud)
		if v := c.EvaluateDevicePin(host, 7, "grpc.wendy.dev:443", "42"); v != PinMatch {
			t.Fatalf("want PinMatch without adoption, got %v", v)
		}
	})

	t.Run("legacy fieldless pin reads as lan", func(t *testing.T) {
		c := &Config{DevicePins: map[string]DevicePin{
			host: {OrgID: 7, CloudGRPC: "grpc.wendy.dev:443"},
		}}
		if got := c.PinSource(host); got != PinSourceLAN {
			t.Fatalf("want %q, got %q", PinSourceLAN, got)
		}
		if v := c.EvaluateDevicePin(host, 7, "grpc.wendy.dev:443", "42"); v != PinAdoptAsset {
			t.Fatalf("want PinAdoptAsset, got %v", v)
		}
	})
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd go && go test ./internal/shared/config/ -run TestDevicePinSourcePrecedence -v`
Expected: FAIL — `undefined: PinSourceLAN`, `c.SetDevicePinFrom undefined`, `c.PinSource undefined`.

- [ ] **Step 3: Implement**

In `go/internal/shared/config/devicepin.go`:

```go
// Pin sources. A pin's source records how much the CLI knows about it, which
// decides who may overwrite it: cloud spoke to the org's cloud over an
// authenticated session, lan only observed a certificate on the local network.
const (
	PinSourceLAN   = "lan"
	PinSourceCloud = "cloud"
)
```

Add `Source string \`json:"source,omitempty"\`` to `DevicePin`, documented as: empty means a pin written before sources were recorded, read as `PinSourceLAN`.

```go
// PinSource returns the recorded source for a hostname's pin, defaulting to
// PinSourceLAN for pins written before sources existed and for unpinned hosts.
func (c *Config) PinSource(hostname string) string {
	pin, ok := c.DevicePinFor(hostname)
	if !ok || pin.Source == "" {
		return PinSourceLAN
	}
	return pin.Source
}

// SetDevicePinFrom records a pin and where it came from. A cloud-sourced write
// is authoritative and overwrites whatever was there.
func (c *Config) SetDevicePinFrom(hostname string, orgID int, cloudGRPC, assetID, source string) {
	if c.DevicePins == nil {
		c.DevicePins = make(map[string]DevicePin)
	}
	c.DevicePins[normalizePinHost(hostname)] = DevicePin{
		OrgID: orgID, CloudGRPC: cloudGRPC, AssetID: assetID, Source: source,
	}
}
```

Make the existing `SetDevicePin` delegate: `c.SetDevicePinFrom(hostname, orgID, cloudGRPC, assetID, PinSourceLAN)`.

In `EvaluateDevicePin`, change the `case pin.AssetID == "":` arm to withhold adoption from cloud pins:

```go
	case pin.AssetID == "":
		if pin.Source == PinSourceCloud {
			// Cloud said this device has no asset identity; a LAN sighting is
			// not evidence to the contrary.
			return PinMatch
		}
		return PinAdoptAsset
```

- [ ] **Step 4: Run the tests**

Run: `cd go && go test ./internal/shared/config/ -count=1`
Expected: PASS (including the pre-existing `TestEvaluateDevicePin` cases from Task 0).

- [ ] **Step 5: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && gofmt -w -s . && cd ..
git add go/internal/shared/config/
git commit -m "config: record where a device pin came from

Cloud spoke to the org over an authenticated session; LAN only saw a
certificate on the local network. Cloud writes overwrite anything; a LAN
sighting never backfills an asset id into a cloud pin.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
```

---

### Task 4: SPKI hard fail with a rotation window

**Files:**
- Modify: `go/internal/shared/devicepin/store.go` (`PinnedDevice` at :21, `CheckAndUpdate` at :60)
- Modify: `go/internal/shared/certs/mldsa.go` (stop discarding the pin error at :235)
- Test: `go/internal/shared/devicepin/store_test.go`

**Interfaces:**
- Produces: `devicepin.PinMismatchError{Key, DisplayName, Want, Got string}`; `PinnedDevice.NotAfter string` (RFC3339).

- [ ] **Step 1: Write the failing test**

Append to `go/internal/shared/devicepin/store_test.go`:

```go
func TestCheckAndUpdateKeyChange(t *testing.T) {
	t.Run("within validity is a hard fail", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		first := assetCert(t, 7, "42", time.Now().Add(24*time.Hour))
		if err := s.CheckAndUpdate(first, "thor"); err != nil {
			t.Fatalf("first use: %v", err)
		}

		second := assetCert(t, 7, "42", time.Now().Add(24*time.Hour))
		err = s.CheckAndUpdate(second, "thor")

		var mismatch *PinMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("want PinMismatchError, got %v", err)
		}
	})

	t.Run("after the pinned cert expires it re-pins silently", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		expired := assetCert(t, 7, "42", time.Now().Add(-time.Hour))
		if err := s.CheckAndUpdate(expired, "thor"); err != nil {
			t.Fatalf("first use: %v", err)
		}

		renewed := assetCert(t, 7, "42", time.Now().Add(24*time.Hour))
		if err := s.CheckAndUpdate(renewed, "thor"); err != nil {
			t.Fatalf("rotation after expiry must be accepted, got %v", err)
		}
	})
}
```

Add an `assetCert(t *testing.T, org int32, assetID string, notAfter time.Time) *x509.Certificate` helper following `selfSignedCert` in `go/internal/shared/certs/server_verify_test.go:21`, setting `NotAfter: notAfter` and the URI SAN `certs.AssetURN`-style string `urn:wendy:org:<org>:asset:<assetID>`. Generate a fresh key per call so two calls differ in SPKI.

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd go && go test ./internal/shared/devicepin/ -run TestCheckAndUpdateKeyChange -v`
Expected: FAIL — `undefined: PinMismatchError`, and the "within validity" case returns nil.

- [ ] **Step 3: Implement**

Add `NotAfter string \`json:"notAfter,omitempty"\`` to `PinnedDevice`, and:

```go
// PinMismatchError reports that a device identity presented a different public
// key than the one pinned for it, while the pinned certificate was still valid.
// A rotation that happens after the pinned cert expires is not an error — it is
// renewal — so this only fires inside the pinned cert's validity window, where
// a new key is unexplained.
type PinMismatchError struct {
	Key         string
	DisplayName string
	Want        string
	Got         string
}

func (e *PinMismatchError) Error() string {
	return fmt.Sprintf("device %q (%s) presented a different certificate key than pinned (pinned %s, now %s)",
		e.DisplayName, e.Key, e.Want, e.Got)
}
```

Replace the warn-and-overwrite block in `CheckAndUpdate` (:69-73):

```go
	if existing, pinned := s.devices[key]; pinned && existing.SPKIFingerprint != fingerprint {
		// A key change while the pinned cert is still valid is unexplained: a
		// renewal replaces an expiring cert, it does not race a live one.
		if existing.NotAfter != "" {
			if exp, parseErr := time.Parse(time.RFC3339, existing.NotAfter); parseErr == nil && time.Now().Before(exp) {
				return &PinMismatchError{Key: key, DisplayName: displayName, Want: existing.SPKIFingerprint, Got: fingerprint}
			}
		}
	}
```

Record `NotAfter: leaf.NotAfter.UTC().Format(time.RFC3339)` in the stored `PinnedDevice`. A pin written before `NotAfter` existed has an empty value and falls through to re-pin, which is the safe direction for an upgrade.

- [ ] **Step 4: Stop discarding the error in the verifier**

In `go/internal/shared/certs/mldsa.go`, replace the step-3 discard (:234-238) with:

```go
			if pinErr := opts.PinStore.CheckAndUpdate(leaf, displayName); pinErr != nil {
				return pinErr
			}
```

- [ ] **Step 5: Run the tests**

Run: `cd go && go test ./internal/shared/devicepin/ ./internal/shared/certs/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && gofmt -w -s . && cd ..
git add go/internal/shared/devicepin/ go/internal/shared/certs/
git commit -m "devicepin: fail closed on an unexplained key change

The store warned and then overwrote the pin, and the verifier discarded
the error, so SPKI pinning enforced nothing. Fail the handshake instead —
but only inside the pinned cert's validity window, so ordinary renewal
still re-pins silently.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
```

---

### Task 5: `dialTarget` and ladder enforcement

**Files:**
- Modify: `go/internal/cli/commands/helpers.go` (`dialAgentLadderWithCerts` at :1729, `dialAgentLadder` at :1829, `dialAgentLKG` at :1367, `provisionedAgentAdvertisedMTLS` at :1893)
- Create: `go/internal/cli/commands/dial_target.go`
- Test: `go/internal/cli/commands/dial_target_test.go`

**Interfaces:**
- Consumes: `grpcclient.ConnectWithTLSExpecting`, `(*AgentConnection).IdentityMismatch` (Task 2); `config.PinSource`, `DevicePin.AssetID` (Task 3).
- Produces: `commands.dialTarget{PinKey, Addr string, Expected *certs.WendyIdentity}`; `commands.newDialTarget(pinKey, addr string) dialTarget`; `commands.expectedIdentityFor(pinKey string) *certs.WendyIdentity`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/dial_target_test.go`:

```go
package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestExpectedIdentityFor(t *testing.T) {
	cases := []struct {
		name string
		pin  *config.DevicePin // nil = unpinned
		want *certs.WendyIdentity
	}{
		{name: "unpinned host is unconstrained", pin: nil, want: nil},
		{
			name: "pinned with asset constrains exactly",
			pin:  &config.DevicePin{OrgID: 7, AssetID: "42"},
			want: &certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"},
		},
		{
			name: "pinned without an asset stays unconstrained",
			pin:  &config.DevicePin{OrgID: 7},
			want: nil,
		},
	}
	// Drive expectedIdentityFor through a config injected via the
	// loadConfigForPinFn seam so the test never touches the real config file.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { /* body per Step 3 */ })
	}
}

// TestPinnedHostSkipsPlaintextRung is the security-critical case: a host we
// have seen over mTLS must never be reached unauthenticated, no matter what
// the TXT records or the cache claim.
func TestPinnedHostSkipsPlaintextRung(t *testing.T) {
	// Arrange a dialTarget whose PinKey resolves to a pin, stub the mTLS rungs
	// to fail with a non-cert transport error (the shape that today falls
	// through to plaintext), and assert grpcclient.Connect is never called.
}
```

Fill both bodies using the existing seams in `helpers.go` (`dialAgentLadderWithCertsFn`, `loadAllCLICertsFn`); add a `loadConfigForPinFn = config.Load` seam and a `plaintextConnectFn = grpcclient.Connect` seam in `dial_target.go` so the plaintext rung is observable from a test.

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestExpectedIdentityFor|TestPinnedHostSkipsPlaintextRung' -v`
Expected: FAIL — `undefined: expectedIdentityFor`, `undefined: dialTarget`.

- [ ] **Step 3: Create `dial_target.go`**

```go
package commands

// dialTarget carries what the ladder needs to know about *who* it is dialing,
// not just where. Before this, the ladder took a bare address, so the identity
// the user asked for was gone by the time a certificate arrived — which is
// exactly what let a spoofed mDNS answer redirect a connection to another
// same-CA host.
type dialTarget struct {
	// PinKey is the name the user asked for (--device value, saved default, or
	// the picker's device name) — never the resolved IP, which changes on
	// ordinary DHCP churn and would train users to unpin reflexively. An empty
	// PinKey disables pin enforcement for this dial.
	PinKey string
	// Addr is the host:port actually dialed.
	Addr string
	// Expected constrains the peer certificate. Non-nil only when a pin (or a
	// cloud-seeded value) names a specific asset.
	Expected *certs.WendyIdentity
}

// loadConfigForPinFn is a seam over config.Load for tests.
var loadConfigForPinFn = config.Load

// newDialTarget resolves the pin for pinKey and returns a target constrained by
// it. Key resolution deliberately may read discovery-derived names: choosing
// the wrong key can only ever produce a mismatch — a stricter outcome — never
// a bypass, because the trust decision itself stays on the certificate.
func newDialTarget(pinKey, addr string) dialTarget {
	return dialTarget{PinKey: pinKey, Addr: addr, Expected: expectedIdentityFor(pinKey)}
}

// expectedIdentityFor returns the asset identity pinned for pinKey, or nil when
// the host is unpinned or its pin predates asset ids. Nil means "first contact
// is permissive" — the posture that keeps legacy and unprovisioned devices
// working; the pin is written on the first successful connect.
func expectedIdentityFor(pinKey string) *certs.WendyIdentity {
	if pinKey == "" {
		return nil
	}
	cfg, err := loadConfigForPinFn()
	if err != nil {
		return nil
	}
	pin, ok := cfg.DevicePinFor(pinKey)
	if !ok || pin.AssetID == "" {
		return nil
	}
	return &certs.WendyIdentity{OrgID: int32(pin.OrgID), EntityType: "asset", EntityID: pin.AssetID}
}

// isPinned reports whether pinKey has any recorded pin. A pinned host has been
// reached over mTLS before, so the ladder must not offer it the plaintext rung.
func isPinned(pinKey string) bool {
	if pinKey == "" {
		return false
	}
	cfg, err := loadConfigForPinFn()
	if err != nil {
		return false
	}
	_, ok := cfg.DevicePinFor(pinKey)
	return ok
}
```

- [ ] **Step 4: Change the ladder signature and enforce**

In `helpers.go`, change `dialAgentLadderWithCerts(ctx context.Context, plaintextAddr string, allCerts []config.CertificateInfo)` to take `target dialTarget` instead of `plaintextAddr string`, with `plaintextAddr := target.Addr` as the first line so the body is otherwise untouched. Then:

1. Replace the per-cert dial `grpcclient.ConnectWithTLSAndPins(ctx, mtlsAddr, &allCerts[i], pins)` with `grpcclient.ConnectWithTLSExpecting(ctx, mtlsAddr, &allCerts[i], pins, target.Expected)`.
2. Immediately after `recordMTLSErr(mtlsAddr, probeErr)`, abort on a wrong device:

```go
					if im, mismatched := conn.IdentityMismatch(); mismatched {
						conn.Close()
						// The device is wrong, not our certificate — every
						// remaining cert and port would fail the same way, and
						// the plaintext rung below must not be reached at all.
						return nil, lastMTLSErr, identityRefusal(target.PinKey, im)
					}
```

3. Guard the plaintext rung (the `conn, err := grpcclient.Connect(ctx, plaintextAddr)` at :1822):

```go
	if isPinned(target.PinKey) {
		// A host we have already reached over mTLS must never be reached
		// unauthenticated. Unlike provisionedAgentAdvertisedMTLS this reads
		// local state, not a TXT record the attacker also controls.
		return nil, lastMTLSErr, pinnedHostWentUnauthenticatedError(target.PinKey)
	}
	conn, err := plaintextConnectFn(ctx, plaintextAddr)
```

4. Update `dialAgentLadder` to take a `dialTarget`, and `dialAgentLKG` to build one via `newDialTarget(originalPinKey, hostPort(e.IP, e.Port))` — `dialAgentLKG` gains the pin key as a parameter from its caller, which already has `originalAddr`.
5. Update `provisionedAgentAdvertisedMTLS`'s doc comment: it is now a phrasing hint for error messages only and must not be used as a guard.

Find every remaining call site and update it:

```bash
cd go && grep -rn "dialAgentLadderWithCerts\|dialAgentLadder(\|dialAgentLKG(" internal/cli/commands/
```

- [ ] **Step 5: Add the two error constructors**

In `dial_target.go`:

```go
// identityRefusal renders a wrong-device rejection. Same text in interactive,
// JSON, and non-interactive modes — there is deliberately no "trust this?"
// prompt, because a MITM warning that can be dismissed gets dismissed.
func identityRefusal(pinKey string, im *certs.IdentityMismatchError) error {
	got := "no wendy identity"
	if im.GotAsset != "" {
		got = fmt.Sprintf("asset %s in organization %d", im.GotAsset, im.GotOrg)
	}
	return fmt.Errorf(
		"device %q is pinned to asset %s in organization %d, but the host answering presented %s; refusing to connect — if this device was legitimately replaced or re-enrolled, run 'wendy device unpin %s'",
		pinKey, im.WantAsset, im.WantOrg, got, pinKey)
}

func pinnedHostWentUnauthenticatedError(pinKey string) error {
	return fmt.Errorf(
		"device %q is pinned to an enrolled identity but no authenticated endpoint answered; refusing to fall back to an unauthenticated connection — if it was reflashed or factory reset, run 'wendy device unpin %s'",
		pinKey, pinKey)
}
```

- [ ] **Step 6: Run the tests**

Run: `cd go && go test ./internal/cli/commands/ -count=1`
Expected: PASS. Pre-existing connect-flow tests from #1619 may need their `dialAgentLadderWithCertsFn` stubs updated to the new signature — update them, do not weaken their assertions.

- [ ] **Step 7: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && gofmt -w -s . && cd ..
git add go/internal/cli/commands/
git commit -m "cli: bind the dialed device to the name the user asked for

The ladder took a bare address, so the requested identity was gone by the
time a certificate arrived. dialTarget carries the pin key and expected
asset down to the handshake: a mismatch aborts the ladder outright, and a
pinned host never reaches the plaintext rung.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
```

---

### Task 6: Enforce on every connection, not just the default device

**Files:**
- Modify: `go/internal/cli/commands/helpers.go` (`resolveDeviceAddress` at :765, the `if isDefault` pin gate in `connectToAgent`)
- Modify: `go/internal/cli/commands/device_pin.go` (`enforceDeviceIdentity` — drop the interactive prompt)
- Test: `go/internal/cli/commands/device_pin_test.go`

**Interfaces:**
- Consumes: `newDialTarget`, `identityRefusal` (Task 5); `enforceDeviceIdentity`, `observedDeviceIdentity` (Task 0).
- Produces: `resolveDeviceAddress() (addr string, pinKey string, isDefault bool, err error)`.

- [ ] **Step 1: Write the failing test**

Append to `go/internal/cli/commands/device_pin_test.go`:

```go
// TestEnforceDeviceIdentityHardFails covers the approved policy change: a
// mismatch is refused identically in every mode, with no prompt.
func TestEnforceDeviceIdentityHardFails(t *testing.T) {
	// Arrange a config with a pin for "thor.local" at org 7 asset 42, then
	// call enforceDeviceIdentity with an observation of org 7 asset 43 while
	// isInteractiveTerminal() reports true. Assert a non-nil error and that
	// tui.ConfirmNoDefaultDanger was never invoked.
}

// TestEnforceDeviceIdentityAppliesToNonDefault guards the scope widening.
func TestEnforceDeviceIdentityAppliesToNonDefault(t *testing.T) {
	// Arrange --device pointing at a pinned host with a conflicting identity;
	// assert connectToAgent returns the refusal rather than a connection.
}
```

Write both bodies against the existing seams in `device_pin_test.go` from Task 0 (which already stubs config load/save and terminal detection); mirror their arrangement rather than inventing new ones.

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestEnforceDeviceIdentity -v`
Expected: FAIL — the current code prompts and accepts.

- [ ] **Step 3: Replace the prompt with a refusal**

In `enforceDeviceIdentity`'s `default: // config.PinMismatch` arm, delete the `tui.ConfirmNoDefaultDanger` branch and the re-pin that follows it, and return the refusal unconditionally after `printIdentityChangeWarning`:

```go
		return fmt.Errorf("device %q identity changed (organization/cloud/asset); refusing to connect — if this is expected, run 'wendy device unpin %s'", hostname, hostname)
```

Apply the same change to `challengeUnprovisionedDevice`: keep the explanatory output, drop the prompt, always return the error.

- [ ] **Step 4: Widen the scope**

Change `resolveDeviceAddress` to also return the pin key (the raw `hostname` before `hostPort` is applied, since that is the name the user typed), and in `connectToAgent` replace `if isDefault { ... enforceDevicePin ... }` with an unconditional call using that pin key. Pass the same key into the dial path via `newDialTarget`.

For picker-selected devices, use the selected device's `DisplayName` as the pin key in `connectFromSelectedDevice`.

- [ ] **Step 5: Run the tests**

Run: `cd go && go test ./internal/cli/commands/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && gofmt -w -s . && cd ..
git add go/internal/cli/commands/
git commit -m "cli: enforce the device pin on every connection, and stop prompting

Pinning covered only the saved default device, so --device and picker
selections were unchecked. A mismatch now refuses identically in every
mode: a MITM warning that can be dismissed gets dismissed.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
```

---

### Task 7: `wendy device unpin`

**Files:**
- Modify: `go/internal/cli/commands/device.go` (register alongside `newDeviceSetDefaultCmd` at :476)
- Create: `go/internal/cli/commands/device_unpin.go`
- Test: `go/internal/cli/commands/device_unpin_test.go`

**Interfaces:**
- Consumes: `(*config.Config).ClearDevicePin` (Task 0); `devicepin.Open` and the store's key format `certs.WendyIdentity.IdentityKey()`.
- Produces: `newDeviceUnpinCmd() *cobra.Command`; `(*devicepin.Store).Remove(key string) error`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/device_unpin_test.go`:

```go
func TestDeviceUnpinClearsBothStores(t *testing.T) {
	// Arrange: a config pin for "thor.local" (org 7, asset 42) and a devicepin
	// SPKI entry keyed "urn:wendy:org:7:asset:42".
	// Act: run the unpin command for "thor.local".
	// Assert: config.DevicePinFor returns !ok, and the SPKI entry is gone.
}

func TestDeviceUnpinUnknownHostIsNotAnError(t *testing.T) {
	// Unpinning an unpinned host succeeds quietly: the command's job is to
	// leave the host unpinned, which it already is.
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestDeviceUnpin -v`
Expected: FAIL — `undefined: newDeviceUnpinCmd`.

- [ ] **Step 3: Add `Remove` to the SPKI store**

In `go/internal/shared/devicepin/store.go`:

```go
// Remove drops the pin for an identity key, so the next connection is a first
// use. Removing an absent key is not an error.
func (s *Store) Remove(key string) error {
	if _, ok := s.devices[key]; !ok {
		return nil
	}
	delete(s.devices, key)
	return s.flush()
}
```

- [ ] **Step 4: Implement the command**

Create `go/internal/cli/commands/device_unpin.go` with a Cobra command `unpin <hostname>` (`Args: cobra.ExactArgs(1)`) that loads the config, reads the pin so it can derive the SPKI key `certs.WendyIdentity{OrgID: int32(pin.OrgID), EntityType: "asset", EntityID: pin.AssetID}.IdentityKey()`, calls `cfg.ClearDevicePin(hostname)` and `store.Remove(key)`, saves, and prints:

```
Unpinned "thor.local". The next connection to it will record a fresh identity.
```

Register it in `device.go` next to `newDeviceSetDefaultCmd()`.

- [ ] **Step 5: Run the tests**

Run: `cd go && go test ./internal/cli/commands/ ./internal/shared/devicepin/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && gofmt -w -s . && cd ..
git add go/internal/cli/commands/ go/internal/shared/devicepin/
git commit -m "cli: add 'wendy device unpin'

Every identity refusal points at this command, so it has to exist and has
to clear both pin stores — the hostname-keyed config pin and the
identity-keyed SPKI pin.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
```

---

### Task 8: Cloud seeding

**Files:**
- Modify: `go/internal/cli/commands/cloud_discover.go` (`fetchCloudAssetsFiltered` call sites)
- Create: `go/internal/cli/commands/cloud_pin_seed.go`
- Test: `go/internal/cli/commands/cloud_pin_seed_test.go`

**Interfaces:**
- Consumes: `cloudpb.Asset` (`GetId()`, `GetName()`), `(*config.Config).SetDevicePinFrom`, `config.PinSourceCloud` (Task 3).
- Produces: `seedPinsFromCloudAssets(assets []*cloudpb.Asset, orgID int, cloudGRPC string) error`.

- [ ] **Step 1: Write the failing test**

```go
func TestSeedPinsFromCloudAssets(t *testing.T) {
	t.Run("writes a cloud-sourced pin per asset", func(t *testing.T) {
		// assets: {Id: 42, Name: "calm-zinnia"} → pin "calm-zinnia" is
		// org 7 / asset "42" / PinSourceCloud.
	})
	t.Run("overwrites a conflicting lan pin", func(t *testing.T) {
		// Pre-seed "calm-zinnia" as lan/asset 99; after seeding it is
		// cloud/asset 42, with no error and no prompt.
	})
	t.Run("skips assets with no name", func(t *testing.T) {
		// An unnamed asset has no key to pin under; it must be skipped, not
		// pinned under "".
	})
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestSeedPinsFromCloudAssets -v`
Expected: FAIL — `undefined: seedPinsFromCloudAssets`.

- [ ] **Step 3: Implement**

```go
// seedPinsFromCloudAssets records the org's asset roster as cloud-sourced pins.
// The cloud spoke over an authenticated session, so this is authority, not a
// sighting: it overwrites whatever a LAN observation recorded, and closes the
// trust-on-first-use window for every device the cloud knows about before the
// CLI ever meets it on the network.
func seedPinsFromCloudAssets(assets []*cloudpb.Asset, orgID int, cloudGRPC string) error {
	cfg, err := loadConfigForPinFn()
	if err != nil {
		return err
	}
	changed := false
	for _, a := range assets {
		name := a.GetName()
		if name == "" {
			continue
		}
		cfg.SetDevicePinFrom(name, orgID, cloudGRPC, strconv.Itoa(int(a.GetId())), config.PinSourceCloud)
		changed = true
	}
	if !changed {
		return nil
	}
	return config.Save(cfg)
}
```

Call it from each place `fetchCloudAssetsFiltered` returns successfully, best-effort: a seeding failure must never block the command the user actually ran, so log at debug and continue.

- [ ] **Step 4: Run the tests**

Run: `cd go && go test ./internal/cli/commands/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go && gofmt -w -s . && cd ..
git add go/internal/cli/commands/
git commit -m "cli: seed device pins from the cloud asset roster

Cloud is authority: it spoke to the org over an authenticated session, so
its asset ids overwrite anything a LAN sighting recorded and close the
trust-on-first-use window before the CLI meets the device on the network.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
```

---

### Task 9: Documentation and PR

**Files:**
- Modify: `go/internal/cli/assets/docs/pki/README.md` (pinning section — extended by Task 0's commit; document sources, the rotation window, and `unpin`)
- Modify: `go/internal/cli/assets/docs/wendyos/networking/mdns.md:119` (the paragraph claiming `tls=true` decides how a device is contacted — it no longer decides trust)

- [ ] **Step 1: Update the mDNS doc**

Replace the trust implication at `mdns.md:119` with a statement that TXT records are unauthenticated and are used for routing and display only; the device's identity is what its certificate proves, checked against the pin recorded for the name being connected to.

- [ ] **Step 2: Update the PKI doc**

Document: pin sources (`lan` vs `cloud`) and their precedence; that a mismatch refuses without a prompt; the SPKI rotation window; and `wendy device unpin <host>` as the single escape hatch.

- [ ] **Step 3: Full verification**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity/go
gofmt -l . && make lint && make test
```

Expected: no gofmt output, lint clean, all tests PASS. Fix anything that fails before opening the PR.

- [ ] **Step 4: Commit and push**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-device-identity
git add go/internal/cli/assets/docs/
git commit -m "docs: mDNS TXT records are routing, not trust

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01C9ZxDP3ea5YmqPe2tc6fsW"
git push -u origin jo/cli-device-identity-enforcement
```

- [ ] **Step 5: Open the PR**

Base it on `ed/lkg-connect-cache`. The body must state plainly: the trust gap is pre-existing on `main` (#1616 consolidated the TXT parsing rather than introducing it); this branch contains the `jo/pin-device-identity` commit under separate review; and **on-device verification has not been performed**. Link the review comment that started this (`https://github.com/wendylabsinc/WendyOS/pull/1616#discussion_r3742488639`) and the spec.

---

### Task 10: Multi-key pin lookup

**Added after Task 8 revealed a plan defect.** Design §3 requires that pin "lookup tries hostname, then display name, then mesh name", and §Testing requires a test for that lookup order — but no task implemented it. `expectedIdentityFor` and `isPinned` each do a single `DevicePinFor(pinKey)`. Consequence: Task 8's cloud seeding writes pins keyed by the cloud asset name (`calm-zinnia`) while every dial path looks up the mDNS hostname (`wendyos-calm-zinnia`), so those pins are inert. `cloudpb.Asset` carries only `{Id, Name}` — no hostname — so cloud cannot supply the dial key directly; the lookup side has to reconcile.

**Files:**
- Modify: `go/internal/cli/commands/dial_target.go` (`expectedIdentityFor`, `isPinned`)
- Test: `go/internal/cli/commands/dial_target_test.go`

**Interfaces:**
- Consumes: `discoverycache.Entry{Hostname, DisplayName, MeshName string}`; `cachedDeviceEntry(cache *discoverycache.Cache, host string) (discoverycache.Entry, bool)` in `helpers.go`; `(*config.Config).DevicePinFor`; `config.PinSourceCloud`.
- Produces: `pinCandidateKeys(pinKey string) []string`; `lookupPin(cfg *config.Config, pinKey string) (config.DevicePin, string, bool)` returning the winning pin, the key it was found under, and whether one was found.

**Rules:**
1. Candidates are `pinKey` first, then — from the discovery-cache entry whose hostname matches `pinKey` — its `MeshName` and `DisplayName`. Deduplicated, empties dropped, `pinKey` never repeated.
2. **A cloud-sourced pin wins over a LAN-sourced one** regardless of position, since cloud is authority. Among pins of equal source, the earliest candidate wins.
3. Cache access is best-effort: any failure degrades to the single-key behaviour. A cache miss must never make a host that *was* pinned read as unpinned.
4. The safety property that licenses reading discovery data here: consulting more keys can only ever *find* a pin, never discard one, so a wrong candidate produces a stricter outcome — never a bypass. Trust decisions stay on the certificate.

- [ ] **Step 1: Write the failing tests**

Cover, with real assertions: single-key match still works with no cache; a pin written under the mesh name is found when dialing the hostname; a cloud pin under one candidate beats a LAN pin under an earlier candidate; a cache read failure degrades to single-key rather than reporting unpinned; and `isPinned` returns true when any candidate is pinned. Reuse `setTempConfig` and the cache helpers already in this package's tests.

- [ ] **Step 2: Run them to confirm they fail**

Run: `cd go && go test ./internal/cli/commands/ -run 'PinCandidate|LookupPin|ExpectedIdentityFor|IsPinned' -v`

- [ ] **Step 3: Implement `pinCandidateKeys` and `lookupPin`, and route both accessors through them**

- [ ] **Step 4: Run the suite**

Run: `cd go && go test ./internal/cli/commands/ -count=1`

- [ ] **Step 5: Commit**

---

## Self-Review

**Spec coverage:** §1 enforcement → Tasks 1–2; §2 pin record → Tasks 0, 3; §3 pin key → Tasks 5 and 10; §4 plaintext → Task 5; §5 SPKI → Task 4; §6 surface → Tasks 6–8; §7 scope → Task 6; Sequencing → Task 0; Testing → distributed across all tasks; docs → Task 9. No section is unimplemented.

**Correction (2026-08-09):** the original coverage claim above was wrong — it mapped design §3 to Task 5 alone, but §3's multi-key lookup requirement had no task. Task 10 was added to close it after Task 8 surfaced the gap.

**Known softness:** Tasks 5–8 describe several test bodies as arrangements against existing seams rather than quoting them in full, because the seams they must use (`device_pin_test.go`'s config/terminal stubs) arrive with Task 0's commit and cannot be quoted accurately until that merge lands. The implementer must write real assertions there, not skip them. Task 5 Step 4 also requires a grep-driven sweep of call sites rather than an exhaustive list, since #1619's callers move as it evolves.
