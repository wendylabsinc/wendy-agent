# Prefer LAN for Mesh Dials — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make LAN-direct mesh dials actually succeed and use a dialable address, so the mesh prefers LAN over the cloud relay when a peer is local.

**Architecture:** (A) In `meshDialLAN`, dial the resolved IP:port through a gRPC context dialer against a fixed `passthrough:///` target, so a zoned IPv6 address is handed to `net.Dialer` verbatim and never hits gRPC's target URL parser. (B) In shared discovery scoring, prefer a routable (IPv4/global) address over a link-local IPv6 so the dialer receives a dialable one.

**Tech Stack:** Go, gRPC (`grpc.WithContextDialer`, `passthrough` resolver), `net`/`net/netip`, Go `testing`.

## Global Constraints

- Design doc: `specs/2026-07-05-mesh-prefer-lan-design.md`.
- Run all go commands from `/Users/joannisorlandos/git/wendy/wendyos-mesh/go`.
- Mesh peer-identity pinning is unchanged: `meshDialLAN` still builds its TLS config via `mtls.NewClientTLSConfigExpectingPeer(...)` with the same `ident.orgID` / `deviceID` arguments. The `passthrough:///` target is safe only because that config verifies the peer's cert identity (`InsecureSkipVerify: true` + custom `VerifyPeerCertificate`), not the hostname — do not alter the TLS config.
- `meshDialBroker` is NOT changed (it targets a real broker hostname).
- Fix B lives in shared discovery and must never *drop* an address: a device advertising only a link-local IPv6 keeps it.
- Commit messages end with the trailers:
  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq
  ```

---

### Task 1: Dial the resolved address via a context dialer (fix A)

**Files:**
- Modify: `internal/agent/services/mesh_dialer.go` (add `meshDialTarget` const + `meshDialContextDialer`; change the `grpc.NewClient` call in `meshDialLAN`, currently line 253)
- Test: `internal/agent/services/mesh_dialer_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `const meshDialTarget = "passthrough:///mesh-peer"`
  - `func meshDialContextDialer(hostport string) func(context.Context, string) (net.Conn, error)`

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/services/mesh_dialer_test.go` (package `services`; `context`, `net`, `strings`, `testing` are already imported by the file — verify and add any missing):

```go
func TestMeshDialTargetCarriesNoAddress(t *testing.T) {
	// The fixed gRPC target must embed no peer address: the address (which may
	// contain an IPv6 zone "%") is supplied via the context dialer instead, so
	// it never reaches gRPC's URL parser.
	if !strings.HasPrefix(meshDialTarget, "passthrough:///") {
		t.Fatalf("meshDialTarget must use the passthrough resolver, got %q", meshDialTarget)
	}
	if strings.ContainsAny(meshDialTarget, "%[") {
		t.Fatalf("meshDialTarget must not embed an address/zone, got %q", meshDialTarget)
	}
}

func TestMeshDialContextDialerDialsVerbatim(t *testing.T) {
	// The dialer must connect to the exact hostport it was given, via the
	// standard library (the only thing that understands IPv6 zone ids), and
	// must ignore the gRPC target argument.
	for _, host := range []string{"127.0.0.1", "[::1]"} {
		ln, err := net.Listen("tcp", host+":0")
		if err != nil {
			t.Skipf("cannot listen on %s (unavailable in this env): %v", host, err)
		}
		dialer := meshDialContextDialer(ln.Addr().String())
		conn, err := dialer(context.Background(), "grpc-target-should-be-ignored")
		if err != nil {
			ln.Close()
			t.Fatalf("dialer(%s) error: %v", ln.Addr(), err)
		}
		conn.Close()
		ln.Close()
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/services/ -run 'TestMeshDialTargetCarriesNoAddress|TestMeshDialContextDialerDialsVerbatim' -v`
Expected: compile failure — `meshDialTarget` and `meshDialContextDialer` undefined.

- [ ] **Step 3: Add the target constant and dialer helper**

In `internal/agent/services/mesh_dialer.go`, add near the other package-level consts (next to `lanBudget`/`lanCacheTTL` around line 29):

```go
// meshDialTarget is the fixed gRPC target used for every LAN peer dial. The
// peer's real address is supplied out-of-band by meshDialContextDialer, so a
// resolved IP:port — including an IPv6 zone id such as "%wlan0" — is handed to
// net.Dialer verbatim and never flows through gRPC's target URL parser, which
// rejects the "%" zone separator as an invalid percent-escape (the failure that
// silently forced every zoned-IPv6 LAN dial onto the cloud relay).
const meshDialTarget = "passthrough:///mesh-peer"

// meshDialContextDialer returns a gRPC dialer that connects to hostport
// verbatim via the standard library, which is the only component that
// understands IPv6 zone ids. hostport is the address MeshDialer resolved from
// mDNS (a net.JoinHostPort result). The gRPC-supplied target argument is
// ignored — meshDialTarget is a placeholder authority only.
func meshDialContextDialer(hostport string) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", hostport)
	}
}
```

- [ ] **Step 4: Use the dialer in `meshDialLAN`**

In `internal/agent/services/mesh_dialer.go`, replace the existing dial line (currently line 253):

```go
	cc, err := grpc.NewClient(hostport, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
```

with:

```go
	cc, err := grpc.NewClient(meshDialTarget,
		grpc.WithContextDialer(meshDialContextDialer(hostport)),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
```

Leave everything else in `meshDialLAN` unchanged (the `tlsCfg` from `NewClientTLSConfigExpectingPeer`, the stream open, `dialBoundContext`, etc.).

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/services/ -run 'TestMeshDialTargetCarriesNoAddress|TestMeshDialContextDialerDialsVerbatim' -v && go build ./...`
Expected: PASS; module builds.

- [ ] **Step 6: Run the existing dialer tests (no regression)**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/agent/services/ -run 'TestDialDevice|TestMeshDial|TestStreamNetConn|TestUpdateIdentity' -v`
Expected: PASS (the `d.dialLAN` seam these use is unaffected).

- [ ] **Step 7: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-mesh
git add go/internal/agent/services/mesh_dialer.go go/internal/agent/services/mesh_dialer_test.go
git commit -m "fix(mesh): dial LAN peers via context dialer so zoned IPv6 works

$(printf 'Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq')"
```

---

### Task 2: Prefer a routable address in discovery scoring (fix B)

**Files:**
- Modify: `internal/shared/discovery/usb_connection.go` (add `net/netip` import; add `isRoutableLANAddress`; add one term to `lanDeviceDiscoveryScore`, currently lines 147-174)
- Test: `internal/shared/discovery/usb_connection_test.go` (new file)

**Interfaces:**
- Consumes: existing `preferDiscoveredLANDevice` / `appendPreferredLANDevice` (`usb_connection.go:122-145`), `models.LANDevice`.
- Produces:
  - `func isRoutableLANAddress(addr string) bool`

- [ ] **Step 1: Write the failing tests**

Create `internal/shared/discovery/usb_connection_test.go` (package `discovery`):

```go
package discovery

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestIsRoutableLANAddress(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"192.168.1.10", true},                        // IPv4 private
		{"10.0.0.5", true},                            // IPv4 private
		{"2001:db8::1", true},                         // global IPv6
		{"fd00::1", true},                             // ULA IPv6 (not link-local)
		{"::1", true},                                 // IPv6 loopback is not link-local-unicast
		{"fe80::1", false},                            // IPv6 link-local
		{"fe80::1dc5:4d23:df52:fc45%wlan0", false},    // zoned IPv6 link-local
		{"", false},                                   // empty
		{"not-an-ip", false},                          // garbage
	}
	for _, tc := range cases {
		if got := isRoutableLANAddress(tc.addr); got != tc.want {
			t.Errorf("isRoutableLANAddress(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestAppendPreferredLANDevicePrefersRoutable(t *testing.T) {
	v4 := models.LANDevice{ID: "d", DisplayName: "cam", Hostname: "cam.local", Port: 50052, IPAddress: "192.168.1.5"}
	v6ll := models.LANDevice{ID: "d", DisplayName: "cam", Hostname: "cam.local", Port: 50052, IPAddress: "fe80::1%wlan0"}
	const key = "cam-cam.local-50052"

	// Link-local discovered first, then routable IPv4 → IPv4 must win.
	var devs []models.LANDevice
	idx := map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v6ll)
	devs = appendPreferredLANDevice(devs, idx, key, v4)
	if len(devs) != 1 || devs[0].IPAddress != "192.168.1.5" {
		t.Fatalf("routable IPv4 should win, got %+v", devs)
	}

	// Routable IPv4 first, then link-local → IPv4 must remain.
	devs = nil
	idx = map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v4)
	devs = appendPreferredLANDevice(devs, idx, key, v6ll)
	if len(devs) != 1 || devs[0].IPAddress != "192.168.1.5" {
		t.Fatalf("routable IPv4 should remain, got %+v", devs)
	}

	// Only link-local available → it must be kept (no address dropped).
	devs = nil
	idx = map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v6ll)
	if len(devs) != 1 || devs[0].IPAddress != "fe80::1%wlan0" {
		t.Fatalf("link-local should be kept when only option, got %+v", devs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/shared/discovery/ -run 'TestIsRoutableLANAddress|TestAppendPreferredLANDevicePrefersRoutable' -v`
Expected: compile failure — `isRoutableLANAddress` undefined.

- [ ] **Step 3: Add the `net/netip` import**

In `internal/shared/discovery/usb_connection.go`, add `"net/netip"` to the import block (keep the existing `"net"`):

```go
import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)
```

- [ ] **Step 4: Add `isRoutableLANAddress` and extend the scorer**

In `internal/shared/discovery/usb_connection.go`, add the helper (next to `lanDeviceDiscoveryScore`):

```go
// isRoutableLANAddress reports whether addr is a directly dialable address —
// IPv4 (any) or a non-link-local IPv6 — as opposed to an IPv6 link-local
// unicast address (fe80::/10), which needs a zone id and is a poor default dial
// target. A "%zone" suffix is stripped before parsing; an empty or unparseable
// address is treated as non-routable.
func isRoutableLANAddress(addr string) bool {
	if i := strings.IndexByte(addr, '%'); i >= 0 {
		addr = addr[:i]
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	return !a.IsLinkLocalUnicast()
}
```

Then in `lanDeviceDiscoveryScore`, add a routable-address term immediately after the existing `if dev.IPAddress != "" { score++ }` block:

```go
	if dev.IPAddress != "" {
		score++
	}
	if isRoutableLANAddress(dev.IPAddress) {
		score++
	}
```

(The term is additive and applied to both candidate and existing in `preferDiscoveredLANDevice`, so a device with a routable address outscores the same device advertised with a link-local one; a device with only a link-local address is unaffected.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go test ./internal/shared/discovery/ -run 'TestIsRoutableLANAddress|TestAppendPreferredLANDevicePrefersRoutable' -v && go build ./...`
Expected: PASS; module builds.

- [ ] **Step 6: Run the discovery package tests (no regression) + vet**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-mesh/go && go vet ./internal/shared/discovery/ ./internal/agent/services/ && go test ./internal/shared/discovery/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-mesh
git add go/internal/shared/discovery/usb_connection.go go/internal/shared/discovery/usb_connection_test.go
git commit -m "feat(discovery): prefer routable addresses over link-local IPv6

$(printf 'Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01UWERTiJ3qvVnBxEYsXJtQq')"
```

---

## Self-Review

**Spec coverage:**
- A — zoned-IPv6 dial via context dialer / passthrough target → Task 1.
- A — TLS peer-identity pinning unchanged → Task 1 Step 4 (only the dial target/dialer change; `tlsCfg` untouched) + Global Constraints.
- A — `meshDialBroker` unchanged → not touched by any task.
- B — routable-address preference in scoring → Task 2.
- B — never drop an address (link-local kept when sole option) → Task 2 Step 1 third case + Step 4 (additive term).
- Testing (A dial path via loopback listener incl. `[::1]`; B scoring table + preference) → Task 1 Step 1, Task 2 Step 1.

**Placeholder scan:** none — every step has concrete code and commands.

**Type consistency:** `meshDialTarget` (string const) and `meshDialContextDialer(string) func(context.Context, string) (net.Conn, error)` match their use in `meshDialLAN`'s `grpc.NewClient`/`grpc.WithContextDialer` call (Task 1 Steps 3-4). `isRoutableLANAddress(string) bool` matches its call in `lanDeviceDiscoveryScore` and the test (Task 2 Steps 1, 4).

**Note on the two tasks' independence:** Task 1 (services) and Task 2 (discovery) touch disjoint files and have no ordering dependency; either may be implemented first.
