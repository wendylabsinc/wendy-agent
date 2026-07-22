# Mesh Data Plane v1 (WendyOS agent side) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A container with the `mesh` network entitlement can reach a service on another WendyOS device via `device-<assetID>.cloud.wendy.dev:<port>`, LAN-direct when possible, relayed through the cloud tunnel broker otherwise.

**Architecture:** Cloud asset IDs map deterministically to VIPs in `10.99.0.0/16`. An agent-run DNS server (per-app bridge gateway) answers `device-N` names; a nat REDIRECT rule lands VIP TCP connections on an agent proxy that recovers the original destination via `SO_ORIGINAL_DST` and hands off to a LAN-first peer dialer (new `MeshDial` gRPC on the peer's mTLS port) with cloud-broker `ClientTunnel` fallback. Spec: `specs/2026-07-02-mesh-data-plane-design.md`.

**Tech Stack:** Go 1.26 (module `github.com/wendylabsinc/wendy`, go.mod at repo root), `github.com/miekg/dns` (promote from indirect), `golang.org/x/sys/unix`, existing `hashicorp/mdns`-based `go/internal/shared/discovery`, raw-`protoc` codegen via `make proto` in `go/`.

## Global Constraints

- Run all commands from the repo root `/Users/joannisorlandos/git/wendy/wendyos-mesh` unless a step says otherwise. Tests: `go test ./go/internal/...` style (go.mod is at repo root).
- All host networking shells out to `iptables`/`nsenter`/`ip` — no netlink library (matches `go/internal/agent/hostnetwork/mesh.go:31-35`).
- Mesh plumbing is best-effort in the agent: startup failures log warnings and never crash the agent (pattern: `main.go:171-177`).
- iptables-backed tests use real iptables gated by `requireIPTables(t)` and clean up after themselves (pattern: `go/internal/agent/hostnetwork/mesh_egress_test.go`).
- v1 operates only on the default service CIDR `10.99.0.0/16`. Valid device IDs: 1–65534.
- Ports already taken: 50051 (agent plaintext), 50052 (agent mTLS). Mesh proxy uses **50058**.
- New v2 protos: file in `Proto/wendy/agent/services/v2/`, path added to the `V2_AGENT_PROTOS` array in `go/scripts/generate-proto.sh` (lines 41-55), regenerate with `cd go && make proto`. Generated package: `agentpbv2` = `github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2`.
- Commit after every task with a `feat(agent): ...` / `test(agent): ...` style message.

---

### Task 1: VIP ↔ device-ID mapping

**Files:**
- Create: `go/internal/agent/mesh/vip.go`
- Test: `go/internal/agent/mesh/vip_test.go`

**Interfaces:**
- Consumes: nothing (pure).
- Produces: `mesh.DefaultServiceCIDR = "10.99.0.0/16"`, `mesh.MinDeviceID = 1`, `mesh.MaxDeviceID = 65534`, `func VIPForDevice(deviceID int32) (netip.Addr, error)`, `func DeviceForVIP(vip netip.Addr) (int32, error)`. Used by Tasks 2, 4/5.

- [ ] **Step 1: Write the failing test**

```go
package mesh

import (
	"net/netip"
	"testing"
)

func TestVIPForDevice(t *testing.T) {
	cases := []struct {
		id      int32
		want    string
		wantErr bool
	}{
		{215, "10.99.0.215", false},
		{1, "10.99.0.1", false},
		{256, "10.99.1.0", false},
		{65534, "10.99.255.254", false},
		{0, "", true},
		{65535, "", true},
		{-1, "", true},
	}
	for _, c := range cases {
		got, err := VIPForDevice(c.id)
		if c.wantErr != (err != nil) {
			t.Fatalf("VIPForDevice(%d): err = %v, wantErr %v", c.id, err, c.wantErr)
		}
		if err == nil && got.String() != c.want {
			t.Fatalf("VIPForDevice(%d) = %s, want %s", c.id, got, c.want)
		}
	}
}

func TestDeviceForVIP(t *testing.T) {
	cases := []struct {
		vip     string
		want    int32
		wantErr bool
	}{
		{"10.99.0.215", 215, false},
		{"10.99.255.254", 65534, false},
		{"10.99.0.0", 0, true},     // ID 0 invalid
		{"10.99.255.255", 0, true}, // ID 65535 invalid
		{"10.98.0.5", 0, true},     // outside CIDR
		{"192.168.1.1", 0, true},
	}
	for _, c := range cases {
		got, err := DeviceForVIP(netip.MustParseAddr(c.vip))
		if c.wantErr != (err != nil) {
			t.Fatalf("DeviceForVIP(%s): err = %v, wantErr %v", c.vip, err, c.wantErr)
		}
		if err == nil && got != c.want {
			t.Fatalf("DeviceForVIP(%s) = %d, want %d", c.vip, got, c.want)
		}
	}
}

func TestVIPRoundTrip(t *testing.T) {
	for _, id := range []int32{1, 255, 256, 4097, 65534} {
		vip, err := VIPForDevice(id)
		if err != nil {
			t.Fatalf("VIPForDevice(%d): %v", id, err)
		}
		back, err := DeviceForVIP(vip)
		if err != nil {
			t.Fatalf("DeviceForVIP(%s): %v", vip, err)
		}
		if back != id {
			t.Fatalf("round trip %d → %s → %d", id, vip, back)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/mesh/ -run 'TestVIP|TestDeviceForVIP' -v`
Expected: FAIL (package doesn't compile: `VIPForDevice` undefined)

- [ ] **Step 3: Write the implementation**

```go
// Package mesh implements the WendyOS mesh data plane: the deterministic
// device-ID↔VIP mapping, the per-app DNS server that answers
// device-N.cloud.wendy.dev names, and the transparent TCP proxy that carries
// mesh VIP connections to peer devices.
package mesh

import (
	"fmt"
	"net/netip"
)

// DefaultServiceCIDR is the mesh service CIDR v1 operates on. The wendy.json
// schema accepts other CIDRs (the route/ACCEPT plumbing honors them), but DNS
// answers and VIP→device resolution assume this network.
const DefaultServiceCIDR = "10.99.0.0/16"

var meshPrefix = netip.MustParsePrefix(DefaultServiceCIDR)

// Device IDs 0 and 65535 map to the CIDR's network and broadcast addresses,
// so the valid range excludes them.
const (
	MinDeviceID = 1
	MaxDeviceID = 65534
)

// VIPForDevice maps a cloud asset ID to its mesh VIP: device N →
// 10.99.<N/256>.<N%256>. Pure function; no allocation state exists anywhere.
func VIPForDevice(deviceID int32) (netip.Addr, error) {
	if deviceID < MinDeviceID || deviceID > MaxDeviceID {
		return netip.Addr{}, fmt.Errorf("mesh: device ID %d outside VIP range [%d, %d]", deviceID, MinDeviceID, MaxDeviceID)
	}
	base := meshPrefix.Addr().As4()
	return netip.AddrFrom4([4]byte{base[0], base[1], byte(deviceID >> 8), byte(deviceID)}), nil
}

// DeviceForVIP is the inverse of VIPForDevice.
func DeviceForVIP(vip netip.Addr) (int32, error) {
	if !vip.Is4() || !meshPrefix.Contains(vip) {
		return 0, fmt.Errorf("mesh: %s is outside the mesh service CIDR %s", vip, DefaultServiceCIDR)
	}
	b := vip.As4()
	id := int32(b[2])<<8 | int32(b[3])
	if id < MinDeviceID || id > MaxDeviceID {
		return 0, fmt.Errorf("mesh: %s maps to invalid device ID %d", vip, id)
	}
	return id, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/agent/mesh/ -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/mesh/
git commit -m "feat(agent): mesh VIP <-> device-ID mapping"
```

---

### Task 2: Mesh DNS server

**Files:**
- Create: `go/internal/agent/mesh/dns.go`
- Test: `go/internal/agent/mesh/dns_test.go`
- Modify: `go.mod` (promote `github.com/miekg/dns v1.1.72` from indirect to direct)

**Interfaces:**
- Consumes: `VIPForDevice` (Task 1).
- Produces: `func NewDNSServer(logger *zap.Logger, upstream string) *DNSServer`, `func (s *DNSServer) EnsureListener(gatewayIP string) error` (refcounted; binds UDP `gatewayIP:53`), `func (s *DNSServer) ReleaseListener(gatewayIP string)`. Used by Task 9 (container wiring) and Task 10 (main.go, upstream `"127.0.0.53:53"`).

- [ ] **Step 1: Promote the dependency**

Run: `go get github.com/miekg/dns@v1.1.72 && go mod tidy`
Expected: `go.mod` now lists `github.com/miekg/dns v1.1.72` without `// indirect`.

- [ ] **Step 2: Write the failing test**

```go
package mesh

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"
)

// startTestDNS binds a listener on 127.0.0.1 with an ephemeral port and
// returns the bound address.
func startTestDNS(t *testing.T, upstream string) (*DNSServer, string) {
	t.Helper()
	s := NewDNSServer(zap.NewNop(), upstream)
	s.port = 0 // ephemeral for tests; production default is 53
	if err := s.EnsureListener("127.0.0.1"); err != nil {
		t.Fatalf("EnsureListener: %v", err)
	}
	t.Cleanup(func() { s.ReleaseListener("127.0.0.1") })
	return s, s.listenerAddr("127.0.0.1")
}

func query(t *testing.T, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	c := &dns.Client{Timeout: 2 * time.Second}
	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("query %s: %v", name, err)
	}
	return resp
}

func TestDNSAnswersMeshName(t *testing.T) {
	_, addr := startTestDNS(t, "")
	resp := query(t, addr, "device-215.cloud.wendy.dev.", dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("rcode=%d answers=%d, want NOERROR with 1 answer", resp.Rcode, len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok || a.A.String() != "10.99.0.215" {
		t.Fatalf("answer = %v, want A 10.99.0.215", resp.Answer[0])
	}
}

func TestDNSMeshNameAAAAEmptyNoError(t *testing.T) {
	_, addr := startTestDNS(t, "")
	resp := query(t, addr, "device-215.cloud.wendy.dev.", dns.TypeAAAA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Fatalf("rcode=%d answers=%d, want NOERROR with 0 answers", resp.Rcode, len(resp.Answer))
	}
}

func TestDNSOutOfRangeIsNXDOMAIN(t *testing.T) {
	_, addr := startTestDNS(t, "")
	for _, name := range []string{"device-0.cloud.wendy.dev.", "device-70000.cloud.wendy.dev."} {
		resp := query(t, addr, name, dns.TypeA)
		if resp.Rcode != dns.RcodeNameError {
			t.Fatalf("%s: rcode=%d, want NXDOMAIN", name, resp.Rcode)
		}
	}
}

func TestDNSForwardsNonMeshNames(t *testing.T) {
	// Fake upstream that answers everything with 192.0.2.1.
	up, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstreamSrv := &dns.Server{PacketConn: up, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 5},
			A:   net.ParseIP("192.0.2.1"),
		})
		_ = w.WriteMsg(m)
	})}
	go upstreamSrv.ActivateAndServe()
	t.Cleanup(func() { _ = upstreamSrv.Shutdown() })

	_, addr := startTestDNS(t, up.LocalAddr().String())
	resp := query(t, addr, "example.com.", dns.TypeA)
	if len(resp.Answer) != 1 {
		t.Fatalf("forwarded answers = %d, want 1", len(resp.Answer))
	}
}

func TestDNSNoUpstreamIsServfail(t *testing.T) {
	_, addr := startTestDNS(t, "")
	resp := query(t, addr, "example.com.", dns.TypeA)
	if resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode=%d, want SERVFAIL", resp.Rcode)
	}
}

func TestDNSListenerRefcount(t *testing.T) {
	s, _ := startTestDNS(t, "")
	if err := s.EnsureListener("127.0.0.1"); err != nil { // refs=2
		t.Fatal(err)
	}
	s.ReleaseListener("127.0.0.1") // refs=1, still listening
	if s.listenerAddr("127.0.0.1") == "" {
		t.Fatal("listener shut down while still referenced")
	}
	s.ReleaseListener("127.0.0.1") // refs=0, shut down
	if s.listenerAddr("127.0.0.1") != "" {
		t.Fatal("listener still up after last release")
	}
	// startTestDNS cleanup releases once more; make that a no-op by re-adding.
	if err := s.EnsureListener("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./go/internal/agent/mesh/ -run TestDNS -v`
Expected: FAIL (does not compile: `NewDNSServer` undefined)

- [ ] **Step 4: Write the implementation**

```go
package mesh

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"
)

// meshNameRE matches the only names this server is authoritative for.
var meshNameRE = regexp.MustCompile(`^device-([0-9]{1,5})\.cloud\.wendy\.dev\.$`)

// DNSServer answers device-N.cloud.wendy.dev with the device's mesh VIP and
// forwards every other query to the host's upstream resolver. One UDP
// listener is bound per app bridge gateway address, refcounted by the number
// of running meshed containers behind that bridge.
type DNSServer struct {
	logger   *zap.Logger
	upstream string // host resolver, e.g. "127.0.0.53:53"; empty → SERVFAIL for non-mesh names
	port     int    // 53 in production; overridable for tests

	mu        sync.Mutex
	listeners map[string]*gatewayListener
}

type gatewayListener struct {
	refs int
	srv  *dns.Server
	addr string
}

func NewDNSServer(logger *zap.Logger, upstream string) *DNSServer {
	return &DNSServer{
		logger:    logger,
		upstream:  upstream,
		port:      53,
		listeners: make(map[string]*gatewayListener),
	}
}

// EnsureListener binds a UDP DNS listener on gatewayIP (idempotent,
// refcounted). Callers pair every EnsureListener with a ReleaseListener.
func (s *DNSServer) EnsureListener(gatewayIP string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.listeners[gatewayIP]; ok {
		l.refs++
		return nil
	}
	pc, err := net.ListenPacket("udp", net.JoinHostPort(gatewayIP, strconv.Itoa(s.port)))
	if err != nil {
		return fmt.Errorf("mesh dns: listen %s: %w", gatewayIP, err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(s.handle)}
	go func() {
		if err := srv.ActivateAndServe(); err != nil {
			s.logger.Warn("mesh dns listener exited", zap.String("gateway", gatewayIP), zap.Error(err))
		}
	}()
	s.listeners[gatewayIP] = &gatewayListener{refs: 1, srv: srv, addr: pc.LocalAddr().String()}
	s.logger.Info("mesh dns listening", zap.String("addr", pc.LocalAddr().String()))
	return nil
}

// ReleaseListener drops one reference; the listener shuts down when the last
// meshed container behind that gateway stops. Releasing an unknown gateway is
// a no-op.
func (s *DNSServer) ReleaseListener(gatewayIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.listeners[gatewayIP]
	if !ok {
		return
	}
	l.refs--
	if l.refs > 0 {
		return
	}
	delete(s.listeners, gatewayIP)
	if err := l.srv.Shutdown(); err != nil {
		s.logger.Warn("mesh dns shutdown", zap.String("gateway", gatewayIP), zap.Error(err))
	}
}

// listenerAddr returns the bound address for a gateway, or "" if not
// listening. Used by tests.
func (s *DNSServer) listenerAddr(gatewayIP string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.listeners[gatewayIP]; ok {
		return l.addr
	}
	return ""
}

func (s *DNSServer) handle(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) != 1 {
		s.reply(w, r, dns.RcodeRefused)
		return
	}
	q := r.Question[0]
	m := meshNameRE.FindStringSubmatch(strings.ToLower(q.Name))
	if m == nil {
		s.forward(w, r)
		return
	}
	id, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil {
		s.reply(w, r, dns.RcodeNameError)
		return
	}
	vip, err := VIPForDevice(int32(id))
	if err != nil {
		s.reply(w, r, dns.RcodeNameError)
		return
	}
	resp := new(dns.Msg)
	resp.SetReply(r)
	resp.Authoritative = true
	if q.Qtype == dns.TypeA {
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.IP(vip.AsSlice()),
		})
	}
	// AAAA and other types for a valid mesh name: NOERROR with no answers.
	_ = w.WriteMsg(resp)
}

func (s *DNSServer) forward(w dns.ResponseWriter, r *dns.Msg) {
	if s.upstream == "" {
		s.reply(w, r, dns.RcodeServerFailure)
		return
	}
	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	resp, _, err := c.Exchange(r, s.upstream)
	if err != nil {
		s.reply(w, r, dns.RcodeServerFailure)
		return
	}
	_ = w.WriteMsg(resp)
}

func (s *DNSServer) reply(w dns.ResponseWriter, r *dns.Msg, rcode int) {
	m := new(dns.Msg)
	m.SetRcode(r, rcode)
	_ = w.WriteMsg(m)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./go/internal/agent/mesh/ -v`
Expected: PASS (all Task 1 + Task 2 tests)

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/mesh/ go.mod go.sum
git commit -m "feat(agent): mesh DNS server answering device-N.cloud.wendy.dev"
```

---

### Task 3: nat REDIRECT primitives in hostnetwork

**Files:**
- Create: `go/internal/agent/hostnetwork/mesh_redirect.go`
- Test: `go/internal/agent/hostnetwork/mesh_redirect_test.go`

**Interfaces:**
- Consumes: `MeshChainName`, `exitCode` (existing, `mesh.go:16,96`), `requireIPTables(t)` test helper (existing in package tests).
- Produces: `func InitMeshNATChain() error` (nat-table WENDY-MESH chain + PREROUTING jump; called from main.go in Task 10), `func AddMeshRedirect(containerIP, serviceCIDR string, proxyPort int) error`, `func RemoveMeshRedirect(containerIP, serviceCIDR string, proxyPort int) error`. Used by Task 9.

- [ ] **Step 1: Write the failing test** (mirrors `mesh_egress_test.go`: real iptables, `requireIPTables(t)` skip, cleanup)

```go
package hostnetwork

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func meshRedirectFixture(t *testing.T) {
	t.Helper()
	requireIPTables(t)
	if err := InitMeshNATChain(); err != nil {
		t.Fatalf("InitMeshNATChain: %v", err)
	}
	t.Cleanup(func() {
		exec.Command("iptables", "-t", "nat", "-F", MeshChainName).Run()
		exec.Command("iptables", "-t", "nat", "-D", "PREROUTING", "-j", MeshChainName).Run()
		exec.Command("iptables", "-t", "nat", "-X", MeshChainName).Run()
	})
}

func meshRedirectCount(t *testing.T, containerIP, serviceCIDR string, proxyPort int) int {
	t.Helper()
	out, err := exec.Command("iptables", "-t", "nat", "-S", MeshChainName).CombinedOutput()
	if err != nil {
		t.Fatalf("iptables -t nat -S %s failed: %v\n%s", MeshChainName, err, out)
	}
	want := fmt.Sprintf("-s %s/32 -d %s -p tcp -j REDIRECT --to-ports %d", containerIP, serviceCIDR, proxyPort)
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, want) {
			count++
		}
	}
	return count
}

func TestAddMeshRedirectCreatesRule(t *testing.T) {
	meshRedirectFixture(t)
	if err := AddMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
		t.Fatalf("AddMeshRedirect: %v", err)
	}
	if got := meshRedirectCount(t, "10.88.0.7", "10.99.0.0/16", 50058); got != 1 {
		t.Fatalf("rule count = %d, want 1", got)
	}
}

func TestAddMeshRedirectIsIdempotent(t *testing.T) {
	meshRedirectFixture(t)
	for i := 0; i < 2; i++ {
		if err := AddMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
			t.Fatalf("AddMeshRedirect #%d: %v", i+1, err)
		}
	}
	if got := meshRedirectCount(t, "10.88.0.7", "10.99.0.0/16", 50058); got != 1 {
		t.Fatalf("rule count after double add = %d, want 1", got)
	}
}

func TestRemoveMeshRedirect(t *testing.T) {
	meshRedirectFixture(t)
	if err := AddMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
		t.Fatalf("AddMeshRedirect: %v", err)
	}
	if err := RemoveMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
		t.Fatalf("RemoveMeshRedirect: %v", err)
	}
	if got := meshRedirectCount(t, "10.88.0.7", "10.99.0.0/16", 50058); got != 0 {
		t.Fatalf("rule count after remove = %d, want 0", got)
	}
	// Removing again is success (idempotent).
	if err := RemoveMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
		t.Fatalf("second RemoveMeshRedirect: %v", err)
	}
}

func TestInitMeshNATChainIsIdempotent(t *testing.T) {
	meshRedirectFixture(t)
	if err := InitMeshNATChain(); err != nil {
		t.Fatalf("second InitMeshNATChain: %v", err)
	}
	out, err := exec.Command("iptables", "-t", "nat", "-S", "PREROUTING").CombinedOutput()
	if err != nil {
		t.Fatalf("iptables -t nat -S PREROUTING: %v", err)
	}
	jumps := strings.Count(string(out), "-j "+MeshChainName)
	if jumps != 1 {
		t.Fatalf("PREROUTING jump count = %d, want 1", jumps)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/hostnetwork/ -run 'MeshRedirect|MeshNATChain' -v`
Expected: FAIL to compile (`InitMeshNATChain` undefined). On a machine without root/iptables the tests would SKIP once compiling — that's fine; CI on Linux exercises them.

- [ ] **Step 3: Write the implementation** (mirror `mesh.go`/`mesh_egress.go` shapes exactly)

```go
package hostnetwork

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// InitMeshNATChain ensures the WENDY-MESH chain exists in the nat table and
// that PREROUTING jumps into it. Idempotent, safe on every agent startup, and
// never flushes existing rules — the nat-table twin of InitMeshChain.
func InitMeshNATChain() error {
	if err := ensureNATChain(MeshChainName); err != nil {
		return fmt.Errorf("hostnetwork: ensure nat chain %s: %w", MeshChainName, err)
	}
	if err := ensurePreroutingJump(MeshChainName); err != nil {
		return fmt.Errorf("hostnetwork: ensure PREROUTING jump to %s: %w", MeshChainName, err)
	}
	return nil
}

func ensureNATChain(chain string) error {
	out, err := exec.Command("iptables", "-t", "nat", "-N", chain).CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(string(out), "already exists") {
		return nil
	}
	return fmt.Errorf("iptables -t nat -N %s: %w (%s)", chain, err, strings.TrimSpace(string(out)))
}

func ensurePreroutingJump(chain string) error {
	cmd := exec.Command("iptables", "-t", "nat", "-C", "PREROUTING", "-j", chain)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if exitCode(err) != 1 {
		return fmt.Errorf("iptables -t nat -C PREROUTING -j %s: %w (%s)", chain, err, strings.TrimSpace(string(out)))
	}
	out, err = exec.Command("iptables", "-t", "nat", "-A", "PREROUTING", "-j", chain).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables -t nat -A PREROUTING -j %s: %w (%s)", chain, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// meshRedirectArgs returns the nat rule (sans verb) steering one meshed
// container's TCP traffic toward the mesh service CIDR into the local mesh
// proxy port. Shared by add/remove/check so the three can never drift.
func meshRedirectArgs(containerIP, serviceCIDR string, proxyPort int) []string {
	return []string{
		"-t", "nat",
		"-s", containerIP + "/32",
		"-d", serviceCIDR,
		"-p", "tcp",
		"-j", "REDIRECT",
		"--to-ports", strconv.Itoa(proxyPort),
	}
}

// AddMeshRedirect idempotently installs the REDIRECT rule for one container.
func AddMeshRedirect(containerIP, serviceCIDR string, proxyPort int) error {
	exists, err := meshRedirectExists(containerIP, serviceCIDR, proxyPort)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	args := append([]string{"-A", MeshChainName}, meshRedirectArgs(containerIP, serviceCIDR, proxyPort)...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables -t nat -A %s: %w (%s)", MeshChainName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveMeshRedirect idempotently removes the REDIRECT rule for one container.
func RemoveMeshRedirect(containerIP, serviceCIDR string, proxyPort int) error {
	exists, err := meshRedirectExists(containerIP, serviceCIDR, proxyPort)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	args := append([]string{"-D", MeshChainName}, meshRedirectArgs(containerIP, serviceCIDR, proxyPort)...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables -t nat -D %s: %w (%s)", MeshChainName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func meshRedirectExists(containerIP, serviceCIDR string, proxyPort int) (bool, error) {
	args := append([]string{"-C", MeshChainName}, meshRedirectArgs(containerIP, serviceCIDR, proxyPort)...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, fmt.Errorf("iptables -t nat -C %s: %w (%s)", MeshChainName, err, strings.TrimSpace(string(out)))
}
```

- [ ] **Step 4: Run test to verify it passes (or skips cleanly off-Linux)**

Run: `go test ./go/internal/agent/hostnetwork/ -v`
Expected: PASS on Linux with iptables; SKIP messages from `requireIPTables` otherwise. Either way: compiles, no failures.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/hostnetwork/
git commit -m "feat(agent): nat REDIRECT primitives for mesh proxy interception"
```

---

### Task 4: Original-destination recovery + transparent proxy

**Files:**
- Create: `go/internal/agent/mesh/origdst.go`, `go/internal/agent/mesh/origdst_linux.go`, `go/internal/agent/mesh/origdst_other.go`, `go/internal/agent/mesh/proxy.go`
- Test: `go/internal/agent/mesh/origdst_test.go`, `go/internal/agent/mesh/proxy_test.go`

**Interfaces:**
- Consumes: `DeviceForVIP` (Task 1).
- Produces: `mesh.ProxyPort = 50058`, `type PeerDialer interface { DialDevice(ctx context.Context, deviceID int32, port uint16) (net.Conn, error) }`, `func NewProxy(logger *zap.Logger, dialer PeerDialer) *Proxy`, `func (p *Proxy) Start(addr string) error`, `func (p *Proxy) Close() error`. `PeerDialer` is implemented by Task 8's `services.MeshDialer`; `Proxy` is started in Task 10.

- [ ] **Step 1: Write the failing tests**

`origdst_test.go`:

```go
package mesh

import (
	"encoding/binary"
	"testing"
)

func TestAddrPortFromSockaddrIn(t *testing.T) {
	// struct sockaddr_in: [0:2] family, [2:4] port (network order), [4:8] IPv4.
	var b [16]byte
	binary.LittleEndian.PutUint16(b[0:2], 2) // AF_INET
	binary.BigEndian.PutUint16(b[2:4], 8080)
	copy(b[4:8], []byte{10, 99, 0, 215})
	got := addrPortFromSockaddrIn(b[:])
	if got.String() != "10.99.0.215:8080" {
		t.Fatalf("got %s, want 10.99.0.215:8080", got)
	}
}
```

`proxy_test.go`:

```go
package mesh

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeDialer records the dial and hands back one end of a pipe whose other
// end echoes with a prefix.
type fakeDialer struct {
	gotDevice int32
	gotPort   uint16
	err       error
}

func (f *fakeDialer) DialDevice(_ context.Context, deviceID int32, port uint16) (net.Conn, error) {
	f.gotDevice, f.gotPort = deviceID, port
	if f.err != nil {
		return nil, f.err
	}
	a, b := net.Pipe()
	go func() {
		buf := make([]byte, 64)
		n, _ := b.Read(buf)
		fmt.Fprintf(b, "peer:%s", buf[:n])
		b.Close()
	}()
	return a, nil
}

func startTestProxy(t *testing.T, d PeerDialer, dst netip.AddrPort) net.Addr {
	t.Helper()
	p := NewProxy(zap.NewNop(), d)
	p.origDst = func(*net.TCPConn) (netip.AddrPort, error) { return dst, nil }
	if err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p.Addr()
}

func TestProxyRelaysToDialedPeer(t *testing.T) {
	d := &fakeDialer{}
	addr := startTestProxy(t, d, netip.MustParseAddrPort("10.99.0.215:8080"))

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	conn.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "peer:hello" {
		t.Fatalf("relayed %q, want %q", got, "peer:hello")
	}
	if d.gotDevice != 215 || d.gotPort != 8080 {
		t.Fatalf("dialed device %d port %d, want 215/8080", d.gotDevice, d.gotPort)
	}
}

func TestProxyClosesOnNonMeshVIP(t *testing.T) {
	d := &fakeDialer{}
	addr := startTestProxy(t, d, netip.MustParseAddrPort("192.168.1.1:80"))
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != io.EOF {
		t.Fatalf("expected EOF for non-mesh destination, got %v", err)
	}
	if d.gotDevice != 0 {
		t.Fatal("dialer must not be called for a non-mesh destination")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./go/internal/agent/mesh/ -run 'TestAddrPort|TestProxy' -v`
Expected: FAIL to compile (`addrPortFromSockaddrIn`, `NewProxy` undefined)

- [ ] **Step 3: Write the implementations**

`origdst.go` (portable pure helper):

```go
package mesh

import (
	"encoding/binary"
	"net/netip"
)

// addrPortFromSockaddrIn decodes a raw struct sockaddr_in as returned by
// getsockopt(SO_ORIGINAL_DST): bytes [2:4] are the port in network order,
// [4:8] the IPv4 address.
func addrPortFromSockaddrIn(b []byte) netip.AddrPort {
	port := binary.BigEndian.Uint16(b[2:4])
	var ip [4]byte
	copy(ip[:], b[4:8])
	return netip.AddrPortFrom(netip.AddrFrom4(ip), port)
}
```

`origdst_linux.go`:

```go
//go:build linux

package mesh

import (
	"fmt"
	"net"
	"net/netip"

	"golang.org/x/sys/unix"
)

// originalDst recovers the pre-REDIRECT destination of a connection that
// arrived via the WENDY-MESH nat REDIRECT rule.
func originalDst(conn *net.TCPConn) (netip.AddrPort, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var (
		addr    netip.AddrPort
		sockErr error
	)
	if err := raw.Control(func(fd uintptr) {
		mreq, err := unix.GetsockoptIPv6Mreq(int(fd), unix.IPPROTO_IP, unix.SO_ORIGINAL_DST)
		if err != nil {
			sockErr = err
			return
		}
		addr = addrPortFromSockaddrIn(mreq.Multiaddr[:])
	}); err != nil {
		return netip.AddrPort{}, err
	}
	if sockErr != nil {
		return netip.AddrPort{}, fmt.Errorf("getsockopt SO_ORIGINAL_DST: %w", sockErr)
	}
	return addr, nil
}
```

`origdst_other.go`:

```go
//go:build !linux

package mesh

import (
	"errors"
	"net"
	"net/netip"
)

func originalDst(*net.TCPConn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("mesh: SO_ORIGINAL_DST is only available on linux")
}
```

`proxy.go`:

```go
package mesh

import (
	"context"
	"io"
	"net"
	"net/netip"
	"time"

	"go.uber.org/zap"
)

// ProxyPort is the host TCP port the WENDY-MESH nat REDIRECT rule points at.
// 50051 is the agent's plaintext port and 50052 its mTLS port; 50058 is
// otherwise unused by the agent.
const ProxyPort = 50058

// PeerDialer opens a byte stream to a port on another mesh device. The
// context bounds dialing only; the returned conn outlives it.
type PeerDialer interface {
	DialDevice(ctx context.Context, deviceID int32, port uint16) (net.Conn, error)
}

// Proxy terminates REDIRECTed mesh VIP connections, recovers where the
// container was actually connecting, and splices the connection onto a
// PeerDialer stream toward that device.
type Proxy struct {
	logger      *zap.Logger
	dialer      PeerDialer
	origDst     func(*net.TCPConn) (netip.AddrPort, error) // swapped in tests
	dialTimeout time.Duration
	ln          net.Listener
}

func NewProxy(logger *zap.Logger, dialer PeerDialer) *Proxy {
	return &Proxy{
		logger:      logger,
		dialer:      dialer,
		origDst:     originalDst,
		dialTimeout: 15 * time.Second,
	}
}

func (p *Proxy) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	p.ln = ln
	go p.acceptLoop()
	return nil
}

func (p *Proxy) Addr() net.Addr { return p.ln.Addr() }

func (p *Proxy) Close() error {
	if p.ln != nil {
		return p.ln.Close()
	}
	return nil
}

func (p *Proxy) acceptLoop() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handleConn(conn)
	}
}

func (p *Proxy) handleConn(conn net.Conn) {
	defer conn.Close()
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	dst, err := p.origDst(tcp)
	if err != nil {
		p.logger.Warn("mesh proxy: original destination unavailable", zap.Error(err))
		return
	}
	deviceID, err := DeviceForVIP(dst.Addr())
	if err != nil {
		p.logger.Warn("mesh proxy: destination is not a mesh VIP", zap.String("dst", dst.String()), zap.Error(err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.dialTimeout)
	peer, err := p.dialer.DialDevice(ctx, deviceID, dst.Port())
	cancel()
	if err != nil {
		p.logger.Warn("mesh proxy: dialing device failed",
			zap.Int32("device_id", deviceID), zap.Uint16("port", dst.Port()), zap.Error(err))
		return
	}
	defer peer.Close()
	relayBytes(conn, peer)
}

// relayBytes splices two connections until both directions finish,
// propagating half-closes where the transport supports them.
func relayBytes(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		type closeWriter interface{ CloseWrite() error }
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/agent/mesh/ -v && GOOS=linux GOARCH=arm64 go build ./go/internal/agent/mesh/`
Expected: PASS, and the linux cross-build compiles `origdst_linux.go`.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/mesh/
git commit -m "feat(agent): mesh transparent proxy with SO_ORIGINAL_DST recovery"
```

---

### Task 5: MeshDial proto + generated code

**Files:**
- Create: `Proto/wendy/agent/services/v2/mesh_service.proto`
- Modify: `go/scripts/generate-proto.sh` (`V2_AGENT_PROTOS` array, lines 41-55)
- Generated: `go/proto/gen/agentpb/v2/mesh_service*.pb.go`

**Interfaces:**
- Produces: `agentpbv2.WendyMeshServiceServer` / `...Client`, `MeshDialMessage`/`MeshDialOpen`/`MeshDialData`, `RegisterWendyMeshServiceServer`. Used by Tasks 6, 8, 10.

- [ ] **Step 1: Write the proto**

First open `Proto/wendy/agent/services/v2/timesync_service.proto` and copy its exact `package` and `option` header lines (the plan assumes `package wendy.agent.services.v2;` — match whatever that file declares). Then:

```proto
syntax = "proto3";

package wendy.agent.services.v2;

option go_package = "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2;agentpbv2";

// WendyMeshService is the LAN-direct mesh data plane between two WendyOS
// devices. A peer agent (not the CLI) opens MeshDial over the mTLS port,
// authenticated with its asset certificate; the stream carries one TCP
// connection to a local port, mirroring the cloud broker's tunnel semantics.
service WendyMeshService {
  rpc MeshDial(stream MeshDialMessage) returns (stream MeshDialData);
}

// First client message on a MeshDial stream: which local TCP port to connect.
message MeshDialOpen {
  uint32 port = 1;
}

message MeshDialData {
  bytes payload = 1;
  bool half_close = 2;
}

// Client → server framing: first message must be `open`, all subsequent
// messages must be `data`.
message MeshDialMessage {
  oneof content {
    MeshDialOpen open = 1;
    MeshDialData data = 2;
  }
}
```

- [ ] **Step 2: Register it with the generator**

In `go/scripts/generate-proto.sh`, add `wendy/agent/services/v2/mesh_service.proto` to the `V2_AGENT_PROTOS` array (keep the array's existing ordering style).

- [ ] **Step 3: Generate and verify**

Run: `cd go && make proto && cd ..`
Expected: `go/proto/gen/agentpb/v2/mesh_service.pb.go` and `mesh_service_grpc.pb.go` exist. Then `go build ./go/...` passes.

- [ ] **Step 4: Commit**

```bash
git add Proto/wendy/agent/services/v2/mesh_service.proto go/scripts/generate-proto.sh go/proto/gen/
git commit -m "feat(proto): WendyMeshService MeshDial bidi stream"
```

---

### Task 6: MeshDial server implementation + mtls client-config helper

**Files:**
- Create: `go/internal/agent/services/mesh_service.go`
- Test: `go/internal/agent/services/mesh_service_test.go`
- Modify: `go/internal/agent/mtls/server.go` (add `NewClientTLSConfig`)

**Interfaces:**
- Consumes: `certs.IdentityFromCert(leaf) (WendyIdentity, bool, error)` with `WendyIdentity{OrgID int32, EntityType string ("user"|"asset"), EntityID string}` (`go/internal/shared/certs/orgident.go:14-31`); `mtls.NewTLSConfig(certPEM, chainPEM, keyPEM, logger, notBeforeFloor)` (`mtls/server.go:27`); generated types from Task 5.
- Produces: `services.NewMeshService(logger *zap.Logger, configPath string) *MeshService` (registered in Task 10); `mtls.NewClientTLSConfig(certPEM, chainPEM, keyPEM string, logger *zap.Logger) (*tls.Config, error)` (used by Task 8's LAN dial). Org equality is already enforced by the mandatory mTLS interceptors (`mtls/server.go:94-95`); this service additionally requires `EntityType == "asset"`.
- Device-side kill switch: mesh is disabled iff the file `<configPath>/mesh-disabled` exists (default enabled, matching the cloud org default; cloud sync of the org flag lands with the cloud phase).

- [ ] **Step 1: Add the mtls client helper (no test of its own — exercised in Task 8; keep it minimal)**

Append to `go/internal/agent/mtls/server.go`:

```go
// NewClientTLSConfig returns a TLS config for one agent dialing another
// agent's mTLS port (mesh LAN path): it presents this device's asset
// certificate and verifies the peer's chain with the same custom verifier the
// server side uses (Go's built-in verification can't handle ML-DSA chains).
// Hostname verification is intentionally skipped — device certs carry wendy
// URN SANs, not DNS names.
func NewClientTLSConfig(certPEM, chainPEM, keyPEM string, logger *zap.Logger) (*tls.Config, error) {
	base, err := NewTLSConfig(certPEM, chainPEM, keyPEM, logger, time.Time{})
	if err != nil {
		return nil, err
	}
	if base.VerifyPeerCertificate == nil {
		// InsecureSkipVerify below is only safe because the custom verifier
		// replaces Go's built-in one; never hand out a config without it.
		return nil, errors.New("mtls: base TLS config has no peer verifier")
	}
	return &tls.Config{
		Certificates:          base.Certificates,
		InsecureSkipVerify:    true, // verification is NOT disabled: VerifyPeerCertificate below performs the full (ML-DSA-aware) chain check
		VerifyPeerCertificate: base.VerifyPeerCertificate,
	}, nil
}
```

(Adjust field names to whatever `NewTLSConfig` actually returns if they differ — it sets `Certificates` and `VerifyPeerCertificate` per `mtls/server.go:27-70`.)

- [ ] **Step 2: Write the failing service test**

The generated stream interface only needs `Send`/`Recv`/`Context`, so a fake stream suffices — no gRPC server required. The identity check needs a real `*x509.Certificate` with a wendy URN SAN; build one with a self-signed cert carrying `URIs: [urn:wendy:org:7:asset:215]` (see `certs/orgident.go:31-88` for the URN format).

```go
package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func certWithURN(t *testing.T, urn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(urn)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{u},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func ctxWithPeerCert(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}},
	})
}

// fakeMeshDialStream implements agentpbv2.WendyMeshService_MeshDialServer.
type fakeMeshDialStream struct {
	agentpbv2.WendyMeshService_MeshDialServer // panics if an unstubbed method is hit
	ctx  context.Context
	in   chan *agentpbv2.MeshDialMessage
	out  chan *agentpbv2.MeshDialData
}

func (f *fakeMeshDialStream) Context() context.Context { return f.ctx }
func (f *fakeMeshDialStream) Recv() (*agentpbv2.MeshDialMessage, error) {
	m, ok := <-f.in
	if !ok {
		return nil, io.EOF
	}
	return m, nil
}
func (f *fakeMeshDialStream) Send(d *agentpbv2.MeshDialData) error {
	f.out <- d
	return nil
}

func newMeshServiceForTest(t *testing.T) (*MeshService, string) {
	t.Helper()
	dir := t.TempDir()
	return NewMeshService(zap.NewNop(), dir), dir
}

func TestMeshDialRejectsUserCert(t *testing.T) {
	svc, _ := newMeshServiceForTest(t)
	stream := &fakeMeshDialStream{ctx: ctxWithPeerCert(certWithURN(t, "urn:wendy:org:7:user:9"))}
	err := svc.MeshDial(stream)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
}

func TestMeshDialRejectsNoPeer(t *testing.T) {
	svc, _ := newMeshServiceForTest(t)
	stream := &fakeMeshDialStream{ctx: context.Background()}
	if status.Code(svc.MeshDial(stream)) != codes.PermissionDenied {
		t.Fatal("want PermissionDenied without mTLS peer info")
	}
}

func TestMeshDialRejectsWhenDisabled(t *testing.T) {
	svc, dir := newMeshServiceForTest(t)
	if err := os.WriteFile(filepath.Join(dir, "mesh-disabled"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stream := &fakeMeshDialStream{ctx: ctxWithPeerCert(certWithURN(t, "urn:wendy:org:7:asset:215"))}
	if status.Code(svc.MeshDial(stream)) != codes.PermissionDenied {
		t.Fatal("want PermissionDenied when mesh-disabled file exists")
	}
}

func TestMeshDialRequiresOpenFirst(t *testing.T) {
	svc, _ := newMeshServiceForTest(t)
	in := make(chan *agentpbv2.MeshDialMessage, 1)
	in <- &agentpbv2.MeshDialMessage{Content: &agentpbv2.MeshDialMessage_Data{Data: &agentpbv2.MeshDialData{Payload: []byte("x")}}}
	stream := &fakeMeshDialStream{ctx: ctxWithPeerCert(certWithURN(t, "urn:wendy:org:7:asset:215")), in: in}
	if status.Code(svc.MeshDial(stream)) != codes.InvalidArgument {
		t.Fatal("want InvalidArgument when first message is not open")
	}
}

func TestMeshDialRelaysToLocalPort(t *testing.T) {
	// Local echo server standing in for a published app port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		fmt.Fprintf(c, "echo:%s", buf[:n])
		c.Close()
	}()

	svc, _ := newMeshServiceForTest(t)
	// Point local dialing at the test listener regardless of requested port.
	svc.dialLocal = func(_ string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout("tcp", ln.Addr().String(), timeout)
	}

	in := make(chan *agentpbv2.MeshDialMessage, 3)
	out := make(chan *agentpbv2.MeshDialData, 3)
	in <- &agentpbv2.MeshDialMessage{Content: &agentpbv2.MeshDialMessage_Open{Open: &agentpbv2.MeshDialOpen{Port: 8080}}}
	in <- &agentpbv2.MeshDialMessage{Content: &agentpbv2.MeshDialMessage_Data{Data: &agentpbv2.MeshDialData{Payload: []byte("ping")}}}
	close(in)
	stream := &fakeMeshDialStream{ctx: ctxWithPeerCert(certWithURN(t, "urn:wendy:org:7:asset:215")), in: in, out: out}

	if err := svc.MeshDial(stream); err != nil {
		t.Fatalf("MeshDial: %v", err)
	}
	var got []byte
	for d := range collectUntilHalfClose(out) {
		got = append(got, d...)
	}
	if string(got) != "echo:ping" {
		t.Fatalf("relayed %q, want %q", got, "echo:ping")
	}
}

// collectUntilHalfClose drains payloads until a half_close frame arrives.
func collectUntilHalfClose(out chan *agentpbv2.MeshDialData) <-chan []byte {
	ch := make(chan []byte)
	go func() {
		defer close(ch)
		for d := range out {
			if len(d.Payload) > 0 {
				ch <- d.Payload
			}
			if d.HalfClose {
				return
			}
		}
	}()
	return ch
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestMeshDial -v`
Expected: FAIL to compile (`NewMeshService` undefined)

- [ ] **Step 4: Write the implementation**

```go
package services

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// meshDisabledFile, when present in the agent config dir, turns off the
// device side of the mesh (LAN MeshDial). Mesh defaults to enabled, matching
// the cloud org flag's default; the cloud phase will sync the org flag into
// this file.
const meshDisabledFile = "mesh-disabled"

// MeshService is the serving side of the LAN-direct mesh path: a peer agent
// opens MeshDial over this device's mTLS port and the stream carries one TCP
// connection to a local port. The cloud-relay serving path needs no service —
// it reuses the existing tunnel broker DialRequest handling.
type MeshService struct {
	agentpbv2.UnimplementedWendyMeshServiceServer
	logger           *zap.Logger
	meshDisabledPath string
	dialLocal        func(addr string, timeout time.Duration) (net.Conn, error) // swapped in tests
}

func NewMeshService(logger *zap.Logger, configPath string) *MeshService {
	return &MeshService{
		logger:           logger,
		meshDisabledPath: filepath.Join(configPath, meshDisabledFile),
		dialLocal: func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, timeout)
		},
	}
}

func (s *MeshService) MeshDial(stream agentpbv2.WendyMeshService_MeshDialServer) error {
	ident, err := assetIdentityFromContext(stream.Context())
	if err != nil {
		return err
	}
	if s.meshDisabled() {
		return status.Error(codes.PermissionDenied, "mesh is disabled on this device")
	}
	first, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "reading open message: %v", err)
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "first MeshDial message must be open")
	}
	if open.Port == 0 || open.Port > 65535 {
		return status.Errorf(codes.InvalidArgument, "invalid port %d", open.Port)
	}
	// Same SSRF stance as the broker path (tunnel_broker_client.go:207-213):
	// only local services are reachable.
	conn, err := s.dialLocal(net.JoinHostPort("127.0.0.1", strconv.Itoa(int(open.Port))), 10*time.Second)
	if err != nil {
		return status.Errorf(codes.Unavailable, "dialing local port %d: %v", open.Port, err)
	}
	defer conn.Close()
	s.logger.Info("mesh dial accepted",
		zap.Int32("caller_org", ident.OrgID),
		zap.String("caller_asset", ident.EntityID),
		zap.Uint32("port", open.Port))
	return s.relay(stream, conn)
}

// assetIdentityFromContext requires an mTLS peer whose leaf certificate
// carries a wendy asset identity. Org equality against this device's org is
// already enforced by the server's mandatory mTLS interceptors; this adds the
// asset-vs-user distinction those interceptors don't check.
func assetIdentityFromContext(ctx context.Context) (certs.WendyIdentity, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return certs.WendyIdentity{}, status.Error(codes.PermissionDenied, "mesh dial requires mTLS")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return certs.WendyIdentity{}, status.Error(codes.PermissionDenied, "mesh dial requires a client certificate")
	}
	ident, found, err := certs.IdentityFromCert(tlsInfo.State.PeerCertificates[0])
	if err != nil || !found {
		return certs.WendyIdentity{}, status.Error(codes.PermissionDenied, "client certificate carries no wendy identity")
	}
	if ident.EntityType != "asset" {
		return certs.WendyIdentity{}, status.Error(codes.PermissionDenied, "mesh dial requires an asset certificate")
	}
	return ident, nil
}

func (s *MeshService) meshDisabled() bool {
	_, err := os.Stat(s.meshDisabledPath)
	return err == nil
}

// relay mirrors TunnelBrokerClient.relay (tunnel_broker_client.go:251) with
// MeshDial framing.
func (s *MeshService) relay(stream agentpbv2.WendyMeshService_MeshDialServer, conn net.Conn) error {
	errCh := make(chan error, 2)

	go func() { // stream → local conn
		for {
			msg, err := stream.Recv()
			if err != nil {
				conn.Close()
				errCh <- nil
				return
			}
			d := msg.GetData()
			if d == nil {
				continue
			}
			if len(d.Payload) > 0 {
				if _, err := conn.Write(d.Payload); err != nil {
					errCh <- nil
					return
				}
			}
			if d.HalfClose {
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}
			}
		}
	}()

	go func() { // local conn → stream
		buf := make([]byte, 256*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if sendErr := stream.Send(&agentpbv2.MeshDialData{Payload: buf[:n]}); sendErr != nil {
					errCh <- nil
					return
				}
			}
			if err != nil {
				_ = stream.Send(&agentpbv2.MeshDialData{HalfClose: true})
				errCh <- nil
				return
			}
		}
	}()

	<-errCh
	<-errCh
	return nil
}
```

Note for the implementer: the test's `fakeMeshDialStream.Send` writes to an unbuffered-ish channel that the test drains — if the relay deadlocks, revisit channel buffering in the test, not the relay. The test closes `out` nowhere; `collectUntilHalfClose` exits on the half-close frame.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./go/internal/agent/services/ -run TestMeshDial -v && go build ./go/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/services/mesh_service.go go/internal/agent/services/mesh_service_test.go go/internal/agent/mtls/server.go
git commit -m "feat(agent): MeshDial LAN service with asset-cert authz"
```

---

### Task 7: Asset ID on mDNS — avahi TXT + discovery parsing

**Files:**
- Modify: `go/internal/agent/configpartition/apply.go` (`UpdateAvahiForProvisioning`, lines ~353-456) and its call sites `go/cmd/wendy-agent/main.go:534,625,641`
- Modify: `go/internal/shared/models/` (LANDevice struct — find with `grep -rn "IsMTLS" go/internal/shared/models/`)
- Modify: `go/internal/shared/discovery/discovery_linux.go` (TXT parsing ~lines 119-143), `discovery_darwin.go` (~233-252), `discovery_windows.go` (equivalent block)
- Test: extend the existing tests beside each parser (`grep -l parseAvahiResolveLine go/internal/shared/discovery/*_test.go`) and `go/internal/agent/configpartition/apply_test.go` (or create it if absent)

**Interfaces:**
- Consumes: existing avahi XML rewrite in `apply.go` (scans `/etc/avahi/services/` for the file containing `_wendyos._udp`, upserts TXT records like `tls=true` at apply.go:421,452-456, restarts avahi-daemon).
- Produces: devices advertise `assetid=<N>` TXT once provisioned; `models.LANDevice` gains `AssetID int32` (0 = unknown/unprovisioned), populated by all three platform parsers. Used by Task 8's LAN lookup.

- [ ] **Step 1: Write failing parser test** (linux parser shown; mirror for darwin if its parser has a test file)

Add to the discovery linux test file, following its existing table style:

```go
func TestParseAvahiResolveLineAssetID(t *testing.T) {
	line := `=;eth0;IPv4;mydevice;_wendyos._udp;local;mydevice.local;192.168.1.50;50052;"id=abc" "name=mydevice" "tls=true" "assetid=215"`
	dev, ok := parseAvahiResolveLine(line)
	if !ok {
		t.Fatal("parseAvahiResolveLine returned !ok")
	}
	if dev.AssetID != 215 {
		t.Fatalf("AssetID = %d, want 215", dev.AssetID)
	}
}

func TestParseAvahiResolveLineNoAssetID(t *testing.T) {
	line := `=;eth0;IPv4;mydevice;_wendyos._udp;local;mydevice.local;192.168.1.50;50051;"id=abc" "name=mydevice"`
	dev, ok := parseAvahiResolveLine(line)
	if !ok {
		t.Fatal("parseAvahiResolveLine returned !ok")
	}
	if dev.AssetID != 0 {
		t.Fatalf("AssetID = %d, want 0 for unprovisioned device", dev.AssetID)
	}
}
```

(Adjust the literal line format to match the existing test fixtures in that file — the field order is `=;iface;protocol;name;type;domain;hostname;address;port;txt` per discovery_linux.go:90-146.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/shared/discovery/ -run AssetID -v`
Expected: FAIL (`dev.AssetID` undefined)

- [ ] **Step 3: Implement**

1. Add to the LANDevice struct: `AssetID int32` with comment `// Cloud asset ID from the assetid TXT record; 0 when the device is unprovisioned or pre-mesh.`
2. In each platform parser where TXT records are mapped (`txtRecords["tls"]` etc.), add:

```go
if v, ok := txtRecords["assetid"]; ok {
	if id, err := strconv.ParseInt(v, 10, 32); err == nil && id > 0 {
		dev.AssetID = int32(id)
	}
}
```

3. In `apply.go`'s `UpdateAvahiForProvisioning`, thread the asset ID through (signature gains `assetID int32`) and upsert a `<txt-record>assetid=N</txt-record>` exactly the way the `tls=true` record is upserted (apply.go:421,452-456). In `UpdateAvahiForUnprovisioning`, remove the `assetid` record the way `tls` is reverted. Update the three call sites in `main.go` (534, 625, 641) — the asset ID is available there from the provisioning flow (`ProvisioningInfo()` / the enrollment request).
4. Add/extend `apply_test.go`: write a temp dir with a minimal service XML containing `_wendyos._udp`, call `UpdateAvahiForProvisioning` pointed at it (the function takes the directory or is testable via its file-scanning helper — follow how existing tests in that package fake the avahi dir; if none exist, factor the directory path into a parameter defaulting to `/etc/avahi/services`), and assert the output contains `<txt-record>assetid=215</txt-record>`.

- [ ] **Step 4: Run tests**

Run: `go test ./go/internal/shared/discovery/ ./go/internal/agent/configpartition/ -v && go build ./go/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/discovery/ go/internal/shared/models/ go/internal/agent/configpartition/ go/cmd/wendy-agent/main.go
git commit -m "feat(agent): advertise cloud asset ID over mDNS and parse it in discovery"
```

---

### Task 8: LAN-first peer dialer with broker fallback

**Files:**
- Create: `go/internal/agent/services/mesh_dialer.go`
- Test: `go/internal/agent/services/mesh_dialer_test.go`
- Modify: `go/internal/agent/services/tunnel_broker_client.go` (extract `buildDialOpts` body into a package-level func)

**Interfaces:**
- Consumes: `brokerDialOpts` (extracted below); `cloudpb.TunnelBrokerServiceClient.ClientTunnel` + `ClientTunnelOpen{AssetId, Host: "localhost", Port}` framing (pattern: `go/internal/cli/commands/cloud_tunnel.go:284-357`); `agentpbv2.WendyMeshServiceClient.MeshDial` (Task 5); `mtls.NewClientTLSConfig` (Task 6); `discovery.Discover` + `LANDevice.AssetID` (Task 7).
- Produces: `services.NewMeshDialer(logger *zap.Logger, brokerURL string, orgID, assetID int32, certPEM, keyPEM, chainPEM string) *MeshDialer` implementing `mesh.PeerDialer` (`DialDevice(ctx, deviceID int32, port uint16) (net.Conn, error)`). Used by Task 10.

- [ ] **Step 1: Extract the broker dial options** (pure refactor, existing tests must stay green)

In `tunnel_broker_client.go`, turn the body of `(c *TunnelBrokerClient) buildDialOpts()` into:

```go
// brokerDialOpts returns gRPC dial options and identity metadata for any
// agent-originated connection to the tunnel broker. Shared by the presence
// client (serving side) and the mesh dialer (dialing side).
func brokerDialOpts(orgID, assetID int32, chainPEM string) ([]grpc.DialOption, metadata.MD, error) {
	// … body moved verbatim from buildDialOpts, with c.orgID → orgID,
	// c.assetID → assetID, c.chainPEM → chainPEM …
}

func (c *TunnelBrokerClient) buildDialOpts() ([]grpc.DialOption, metadata.MD, error) {
	return brokerDialOpts(c.orgID, c.assetID, c.chainPEM)
}
```

Run: `go test ./go/internal/agent/services/ -v` — expected PASS (pure refactor).

- [ ] **Step 2: Write the failing dialer test** (fake LAN lookup + fake LAN dial + fake broker dial; asserts ordering, fallback, and cache)

```go
package services

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testDialer() (*MeshDialer, *dialerProbes) {
	p := &dialerProbes{}
	d := NewMeshDialer(zap.NewNop(), "broker.example:443", 7, 100, "", "", "")
	d.lanLookup = func(ctx context.Context, assetID int32) (string, bool) {
		p.lookups++
		return p.lanAddr, p.lanAddr != ""
	}
	d.dialLAN = func(ctx context.Context, hostport string, port uint16) (net.Conn, error) {
		p.lanDials++
		if p.lanErr != nil {
			return nil, p.lanErr
		}
		a, _ := net.Pipe()
		return a, nil
	}
	d.dialBroker = func(ctx context.Context, deviceID int32, port uint16) (net.Conn, error) {
		p.brokerDials++
		if p.brokerErr != nil {
			return nil, p.brokerErr
		}
		a, _ := net.Pipe()
		return a, nil
	}
	return d, p
}

type dialerProbes struct {
	lanAddr               string
	lanErr, brokerErr     error
	lookups, lanDials, brokerDials int
}

func TestDialDeviceUsesLANWhenAvailable(t *testing.T) {
	d, p := testDialer()
	p.lanAddr = "192.168.1.50:50052"
	conn, err := d.DialDevice(context.Background(), 215, 8080)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if p.lanDials != 1 || p.brokerDials != 0 {
		t.Fatalf("lan=%d broker=%d, want 1/0", p.lanDials, p.brokerDials)
	}
}

func TestDialDeviceFallsBackToBroker(t *testing.T) {
	d, p := testDialer()
	p.lanAddr = "192.168.1.50:50052"
	p.lanErr = errors.New("connection refused")
	conn, err := d.DialDevice(context.Background(), 215, 8080)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if p.lanDials != 1 || p.brokerDials != 1 {
		t.Fatalf("lan=%d broker=%d, want 1/1", p.lanDials, p.brokerDials)
	}
}

func TestDialDeviceBrokerOnlyWhenNoLANPeer(t *testing.T) {
	d, p := testDialer()
	p.lanAddr = "" // not found on LAN
	conn, err := d.DialDevice(context.Background(), 215, 8080)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if p.lanDials != 0 || p.brokerDials != 1 {
		t.Fatalf("lan=%d broker=%d, want 0/1", p.lanDials, p.brokerDials)
	}
}

func TestDialDeviceCachesLANOutcome(t *testing.T) {
	d, p := testDialer()
	p.lanAddr = "192.168.1.50:50052"
	now := time.Now()
	d.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		conn, err := d.DialDevice(context.Background(), 215, 8080)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	if p.lookups != 1 {
		t.Fatalf("lookups = %d, want 1 (cached)", p.lookups)
	}

	now = now.Add(2 * lanCacheTTL) // expire
	conn, err := d.DialDevice(context.Background(), 215, 8080)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if p.lookups != 2 {
		t.Fatalf("lookups after TTL = %d, want 2", p.lookups)
	}
}

func TestDialDeviceCachesNegativeOutcome(t *testing.T) {
	d, p := testDialer()
	p.lanAddr = ""
	for i := 0; i < 3; i++ {
		conn, err := d.DialDevice(context.Background(), 215, 8080)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	if p.lookups != 1 {
		t.Fatalf("lookups = %d, want 1 (negative result cached)", p.lookups)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestDialDevice -v`
Expected: FAIL to compile (`NewMeshDialer` undefined)

- [ ] **Step 4: Write the implementation**

```go
package services

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"github.com/wendylabsinc/wendy/go/internal/agent/mtls"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

const (
	// lanBudget bounds mDNS discovery + LAN connect before falling back to
	// the cloud relay; the outcome cache keeps repeat dials from re-paying it.
	lanBudget   = 1 * time.Second
	lanCacheTTL = 60 * time.Second
)

// MeshDialer implements mesh.PeerDialer: LAN-direct MeshDial when the peer is
// discoverable locally, cloud-broker ClientTunnel otherwise.
type MeshDialer struct {
	logger    *zap.Logger
	brokerURL string
	orgID     int32
	assetID   int32
	certPEM   string
	keyPEM    string
	chainPEM  string

	// Seams (overridden in tests).
	lanLookup  func(ctx context.Context, assetID int32) (hostport string, ok bool)
	dialLAN    func(ctx context.Context, hostport string, port uint16) (net.Conn, error)
	dialBroker func(ctx context.Context, deviceID int32, port uint16) (net.Conn, error)
	now        func() time.Time

	mu    sync.Mutex
	cache map[int32]lanCacheEntry
}

type lanCacheEntry struct {
	hostport string // "" = not on LAN
	expires  time.Time
}

func NewMeshDialer(logger *zap.Logger, brokerURL string, orgID, assetID int32, certPEM, keyPEM, chainPEM string) *MeshDialer {
	d := &MeshDialer{
		logger:    logger,
		brokerURL: brokerURL,
		orgID:     orgID,
		assetID:   assetID,
		certPEM:   certPEM,
		keyPEM:    keyPEM,
		chainPEM:  chainPEM,
		now:       time.Now,
		cache:     make(map[int32]lanCacheEntry),
	}
	d.lanLookup = d.discoverOnLAN
	d.dialLAN = d.meshDialLAN
	d.dialBroker = d.meshDialBroker
	return d
}

// DialDevice opens a byte stream to port on the given mesh device. ctx bounds
// dialing only; the returned conn has an independent lifetime.
func (d *MeshDialer) DialDevice(ctx context.Context, deviceID int32, port uint16) (net.Conn, error) {
	if hostport, ok := d.lanAddr(ctx, deviceID); ok {
		conn, err := d.dialLAN(ctx, hostport, port)
		if err == nil {
			return conn, nil
		}
		d.logger.Warn("mesh: LAN dial failed, falling back to cloud relay",
			zap.Int32("device_id", deviceID), zap.String("lan_addr", hostport), zap.Error(err))
	}
	return d.dialBroker(ctx, deviceID, port)
}

func (d *MeshDialer) lanAddr(ctx context.Context, deviceID int32) (string, bool) {
	d.mu.Lock()
	if e, ok := d.cache[deviceID]; ok && d.now().Before(e.expires) {
		d.mu.Unlock()
		return e.hostport, e.hostport != ""
	}
	d.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, lanBudget)
	hostport, ok := d.lanLookup(lctx, deviceID)
	cancel()
	if !ok {
		hostport = ""
	}
	d.mu.Lock()
	d.cache[deviceID] = lanCacheEntry{hostport: hostport, expires: d.now().Add(lanCacheTTL)}
	d.mu.Unlock()
	return hostport, hostport != ""
}

// discoverOnLAN browses _wendyos._udp for a provisioned device advertising
// the target asset ID.
func (d *MeshDialer) discoverOnLAN(ctx context.Context, assetID int32) (string, bool) {
	devices, err := discovery.Discover(ctx, discovery.Options{})
	if err != nil {
		return "", false
	}
	for _, dev := range devices {
		if dev.AssetID == assetID && dev.IsMTLS && dev.IPAddress != "" {
			return net.JoinHostPort(dev.IPAddress, strconv.Itoa(dev.Port)), true
		}
	}
	return "", false
}
```

(If `discovery.Discover`'s options type differs, match its real signature — the CLI call site is `go/internal/cli/commands/discover.go:123`.)

LAN dial + broker dial + the stream→net.Conn adapter (same file):

```go
// meshDialLAN connects to a peer agent's mTLS port and opens a MeshDial
// stream for one TCP connection. The stream gets its own cancellable context
// so it survives past the dial ctx; Close on the returned conn tears it down.
func (d *MeshDialer) meshDialLAN(ctx context.Context, hostport string, port uint16) (net.Conn, error) {
	tlsCfg, err := mtls.NewClientTLSConfig(d.certPEM, d.chainPEM, d.keyPEM, d.logger)
	if err != nil {
		return nil, fmt.Errorf("mesh: client TLS config: %w", err)
	}
	cc, err := grpc.NewClient(hostport, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(context.Background())
	stream, err := agentpbv2.NewWendyMeshServiceClient(cc).MeshDial(sctx)
	if err != nil {
		cancel()
		cc.Close()
		return nil, err
	}
	if err := stream.Send(&agentpbv2.MeshDialMessage{
		Content: &agentpbv2.MeshDialMessage_Open{Open: &agentpbv2.MeshDialOpen{Port: uint32(port)}},
	}); err != nil {
		cancel()
		cc.Close()
		return nil, err
	}
	return streamNetConn(&meshDialAdapter{stream: stream}, func() { cancel(); cc.Close() }), nil
}

// meshDialBroker opens a ClientTunnel to the target asset through the cloud
// broker, authenticated with this device's asset identity — the same relay
// the CLI uses, from the other kind of caller.
func (d *MeshDialer) meshDialBroker(_ context.Context, deviceID int32, port uint16) (net.Conn, error) {
	opts, md, err := brokerDialOpts(d.orgID, d.assetID, d.chainPEM)
	if err != nil {
		return nil, err
	}
	cc, err := grpc.NewClient(d.brokerURL, opts...)
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(metadata.NewOutgoingContext(context.Background(), md))
	stream, err := cloudpb.NewTunnelBrokerServiceClient(cc).ClientTunnel(sctx)
	if err != nil {
		cancel()
		cc.Close()
		return nil, err
	}
	if err := stream.Send(&cloudpb.ClientTunnelMessage{
		Content: &cloudpb.ClientTunnelMessage_Open{Open: &cloudpb.ClientTunnelOpen{
			AssetId: deviceID,
			Host:    "localhost",
			Port:    uint32(port),
		}},
	}); err != nil {
		cancel()
		cc.Close()
		return nil, err
	}
	return streamNetConn(&clientTunnelAdapter{stream: stream}, func() { cancel(); cc.Close() }), nil
}

// tunnelStream abstracts the two stream framings (MeshDial vs ClientTunnel)
// behind one send/recv shape so streamNetConn can serve both.
type tunnelStream interface {
	send(payload []byte, halfClose bool) error
	recv() (payload []byte, halfClose bool, err error)
	closeSend() error
}

type meshDialAdapter struct {
	stream agentpbv2.WendyMeshService_MeshDialClient
}

func (a *meshDialAdapter) send(p []byte, hc bool) error {
	return a.stream.Send(&agentpbv2.MeshDialMessage{
		Content: &agentpbv2.MeshDialMessage_Data{Data: &agentpbv2.MeshDialData{Payload: p, HalfClose: hc}},
	})
}
func (a *meshDialAdapter) recv() ([]byte, bool, error) {
	m, err := a.stream.Recv()
	if err != nil {
		return nil, false, err
	}
	return m.Payload, m.HalfClose, nil
}
func (a *meshDialAdapter) closeSend() error { return a.stream.CloseSend() }

type clientTunnelAdapter struct {
	stream cloudpb.TunnelBrokerService_ClientTunnelClient
}

func (a *clientTunnelAdapter) send(p []byte, hc bool) error {
	return a.stream.Send(&cloudpb.ClientTunnelMessage{
		Content: &cloudpb.ClientTunnelMessage_Data{Data: &cloudpb.TunnelData{Payload: p, HalfClose: hc}},
	})
}
func (a *clientTunnelAdapter) recv() ([]byte, bool, error) {
	m, err := a.stream.Recv()
	if err != nil {
		return nil, false, err
	}
	return m.Payload, m.HalfClose, nil
}
func (a *clientTunnelAdapter) closeSend() error { return a.stream.CloseSend() }

// streamNetConn exposes a tunnelStream as a net.Conn via an in-process pipe,
// mirroring openBrokerTunnel (cloud_tunnel.go:284-357). teardown runs when
// both relay directions finish.
func streamNetConn(s tunnelStream, teardown func()) net.Conn {
	local, remote := net.Pipe()
	var once sync.Once
	finish := func() { once.Do(teardown) }

	go func() { // stream → pipe
		defer remote.Close()
		defer finish()
		for {
			payload, halfClose, err := s.recv()
			if err != nil {
				return
			}
			if len(payload) > 0 {
				if _, err := remote.Write(payload); err != nil {
					return
				}
			}
			if halfClose {
				return
			}
		}
	}()

	go func() { // pipe → stream
		defer finish()
		buf := make([]byte, 256*1024)
		for {
			n, err := remote.Read(buf)
			if n > 0 {
				if sendErr := s.send(buf[:n], false); sendErr != nil {
					return
				}
			}
			if err != nil {
				_ = s.send(nil, true)
				_ = s.closeSend()
				return
			}
		}
	}()

	return local
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./go/internal/agent/services/ -v && go build ./go/...`
Expected: PASS (dialer tests + all pre-existing services tests, including tunnel broker tests after the refactor)

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/services/
git commit -m "feat(agent): LAN-first mesh peer dialer with cloud-broker fallback"
```

---

### Task 9: Container wiring — REDIRECT, DNS listener, resolv.conf

**Files:**
- Modify: `go/internal/agent/containerd/mesh_wiring.go`, `go/internal/agent/containerd/client.go`
- Test: `go/internal/agent/containerd/mesh_wiring_test.go` (extend)

**Interfaces:**
- Consumes: `applyMeshEgress(entitlements, appID, netnsPath, ip)` called at `client.go:1194` (fail-closed start hook); `teardownMeshEgress(entitlements, appID, ip)` at `client.go:2117`; `meshGateway(appID)` (`mesh_wiring.go:47`); the hosts-file mount pattern at `client.go:802-820`; `hostnetwork.AddMeshRedirect`/`RemoveMeshRedirect` (Task 3); `mesh.DNSServer` (Task 2); `mesh.ProxyPort` (Task 4).
- Produces: `func (c *Client) SetMeshDNS(d *mesh.DNSServer)` (called from main.go in Task 10). Behavior: meshed containers additionally get a REDIRECT rule (fail-closed like the route/ACCEPT), a refcounted DNS listener on their bridge gateway (best-effort — DNS failure logs a warning but doesn't block the start; VIP literals still work), and a read-only `/etc/resolv.conf` bind mount pointing at the gateway.

- [ ] **Step 1: Write failing tests for the resolv.conf writer**

Add to `mesh_wiring_test.go`:

```go
func TestWriteMeshResolvConf(t *testing.T) {
	dir := t.TempDir()
	path, err := writeMeshResolvConfIn(dir, "myapp")
	if err != nil {
		t.Fatalf("writeMeshResolvConfIn: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := meshGateway("myapp")
	if err != nil {
		t.Fatal(err)
	}
	want := "nameserver " + gw + "\noptions ndots:1\n"
	if string(data) != want {
		t.Fatalf("resolv.conf = %q, want %q", data, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/containerd/ -run TestWriteMeshResolvConf -v`
Expected: FAIL to compile (`writeMeshResolvConfIn` undefined)

- [ ] **Step 3: Implement the wiring**

Additions to `mesh_wiring.go`:

```go
// meshResolvConfDir holds per-app resolv.conf files pointing meshed
// containers at the mesh DNS server on their bridge gateway.
const meshResolvConfDir = "/run/wendy/mesh"

// writeMeshResolvConfIn writes the resolv.conf for one app under baseDir and
// returns its path. Split from writeMeshResolvConf for testability.
func writeMeshResolvConfIn(baseDir, appID string) (string, error) {
	gw, err := meshGateway(appID)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(baseDir, appID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "resolv.conf")
	content := fmt.Sprintf("nameserver %s\noptions ndots:1\n", gw)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func writeMeshResolvConf(appID string) (string, error) {
	return writeMeshResolvConfIn(meshResolvConfDir, appID)
}
```

Extend `applyMeshEgress` (after the existing `AddMeshRule` call at mesh_wiring.go:116, keeping its fail-closed style):

```go
	if err := hostnetwork.AddMeshRedirect(ip, params.cidr, mesh.ProxyPort); err != nil {
		// Roll back what we installed; the start must fail closed.
		if rmErr := hostnetwork.RemoveMeshRule(ip, params.cidr); rmErr != nil {
			c.logger.Warn("mesh: rollback of ACCEPT rule failed", zap.Error(rmErr))
		}
		return fmt.Errorf("adding mesh REDIRECT rule: %w", err)
	}
	// DNS is best-effort: without it, hostnames fail but VIP literals work.
	if c.meshDNS != nil {
		if err := c.meshDNS.EnsureListener(params.gateway); err != nil {
			c.logger.Warn("mesh: DNS listener unavailable; device-N hostnames will not resolve",
				zap.String("gateway", params.gateway), zap.Error(err))
		}
	}
```

Extend `teardownMeshEgress` (after the `RemoveMeshRule` call at mesh_wiring.go:159, same errors-logged-not-returned style):

```go
	if err := hostnetwork.RemoveMeshRedirect(ip, cidr, mesh.ProxyPort); err != nil {
		c.logger.Warn("mesh: removing REDIRECT rule", zap.Error(err))
	}
	if c.meshDNS != nil {
		if gw, err := meshGateway(appID); err == nil {
			c.meshDNS.ReleaseListener(gw)
		}
	}
```

Client plumbing in `client.go`:

```go
// Near the other Client fields:
	meshDNS *mesh.DNSServer // nil when mesh DNS is unavailable

// SetMeshDNS injects the mesh DNS server; called once at agent startup.
func (c *Client) SetMeshDNS(d *mesh.DNSServer) { c.meshDNS = d }
```

Create-time resolv.conf mount: in the block that builds per-container mounts for isolated apps (the hosts-file mount at `client.go:802-820` is the template — same guard style, appending a `specs.Mount`), add:

```go
	if _, ok := findMeshEntitlement(svcEntitlements); ok && appCfg.Isolation == "isolated" {
		if resolvPath, err := writeMeshResolvConf(appID); err == nil {
			mounts = append(mounts, specs.Mount{
				Destination: "/etc/resolv.conf",
				Type:        "bind",
				Source:      resolvPath,
				Options:     []string{"rbind", "ro"},
			})
		} else {
			c.logger.Warn("mesh: resolv.conf setup failed; container keeps image resolv.conf",
				zap.String("app", appID), zap.Error(err))
		}
	}
```

(`svcEntitlements` = the same per-service entitlement slice the surrounding create path already uses for `applyNetwork`; match the local variable names at the insertion point, and reuse the exact mount-options style of the hosts mount at client.go:802-820.)

- [ ] **Step 4: Run tests**

Run: `go test ./go/internal/agent/containerd/ -v && go build ./go/...`
Expected: PASS (new + all existing mesh_wiring tests)

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/containerd/
git commit -m "feat(agent): wire mesh REDIRECT, DNS listener, and resolv.conf into container lifecycle"
```

---

### Task 10: Agent startup wiring in main.go

**Files:**
- Modify: `go/cmd/wendy-agent/main.go`

**Interfaces:**
- Consumes: everything above. Anchors: `InitMeshChain` call at `main.go:171-177` (add `InitMeshNATChain` beside it, same non-fatal style); provisioning info + broker URL resolution around `main.go:387-400` (where `NewTunnelBrokerClient` is built — reuse the same `brokerURL`, `orgID`, `assetID`, `chainPEM`); cert material used at `main.go:473-478` (`certPEM`, `chainPEM`, `keyPEM` — the mesh dialer needs them, so construct it after they're loaded); service registration closure at `main.go:408-428`.
- Produces: a running mesh data plane on agent start.

- [ ] **Step 1: Wire it**

1. Next to `InitMeshChain` (main.go:171-177), same pattern:

```go
	if err := hostnetwork.InitMeshNATChain(); err != nil {
		logger.Warn("failed to init mesh nat chain", zap.Error(err))
	}
```

2. After the cert material and provisioning info are both in scope (after main.go:~400 and the certPEM/keyPEM loads — verify order when editing; move construction later if needed):

```go
	meshDNS := mesh.NewDNSServer(logger, "127.0.0.53:53")
	containerdClient.SetMeshDNS(meshDNS)

	meshDialer := services.NewMeshDialer(logger, brokerURL, orgID, assetID, certPEM, keyPEM, chainPEM)
	meshProxy := mesh.NewProxy(logger, meshDialer)
	if err := meshProxy.Start(fmt.Sprintf(":%d", mesh.ProxyPort)); err != nil {
		logger.Warn("mesh proxy failed to start; mesh egress disabled", zap.Error(err))
	}

	meshSvc := services.NewMeshService(logger, configPath)
```

(`containerdClient` = whatever the containerd client variable is named in main.go — find it where `StartContainer`'s owner is constructed. If it's constructed after this point, call `SetMeshDNS` right after its construction instead.)

3. In `registerAllServices` (main.go:408-428):

```go
	agentpbv2.RegisterWendyMeshServiceServer(srv, meshSvc)
```

Note: `registerAllServices` also registers on the plaintext and local-socket servers; `MeshDial` fails closed there because `assetIdentityFromContext` finds no TLS peer — that's intended.

- [ ] **Step 2: Build + full test sweep**

Run: `go build ./go/... && go test ./go/cmd/wendy-agent/ ./go/internal/agent/... ./go/internal/shared/... && GOOS=linux GOARCH=arm64 go build ./go/cmd/wendy-agent`
Expected: all PASS, both builds succeed.

- [ ] **Step 3: Commit**

```bash
git add go/cmd/wendy-agent/main.go
git commit -m "feat(agent): start mesh DNS, proxy, and MeshDial service at boot"
```

---

### Task 11: HelloMesh example + README update

**Files:**
- Modify: `Examples/HelloMesh/client/app.py` (default `MESH_TARGET`), `Examples/HelloMesh/README.md`

- [ ] **Step 1: Update the example**

In `app.py`, change the `MESH_TARGET` default from the raw VIP to `device-1.cloud.wendy.dev:8080` (keep the env override). In the README:
- Replace the "Current status (read this)" section: the data plane now exists — polling `device-<assetID>.cloud.wendy.dev:8080` reaches host port 8080 on that device, LAN-direct or relayed.
- Document how to find a device's asset ID (`wendy cloud discover` / the cloud dashboard) and to set `MESH_TARGET=device-<assetID>.cloud.wendy.dev:8080`.
- Keep the `iptables -S WENDY-MESH` / `nsenter … ip route` verification commands as an appendix ("Debugging the plumbing"), adding `iptables -t nat -S WENDY-MESH` for the REDIRECT rule.
- Note the org-level control: mesh is on by default; org admins can disable it (cloud API), and a device-local `mesh-disabled` file in the agent config dir kills the serving side.

- [ ] **Step 2: Commit**

```bash
git add Examples/HelloMesh/
git commit -m "docs(examples): HelloMesh uses device-N mesh hostnames"
```

---

### Task 12: Final verification

- [ ] **Step 1: Full sweep**

Run from repo root:
```bash
gofmt -l go/ | (! grep .) && go vet ./go/... && go build ./go/... && go test ./go/...
GOOS=linux GOARCH=arm64 go build ./go/cmd/wendy-agent
```
Expected: no gofmt diffs, vet clean, all tests pass, ARM64 agent builds.

- [ ] **Step 2: Commit any straggler fixes, then push**

```bash
git push
```

**Hardware/E2E checklist (needs two devices + the cloud phase for the relay leg; LAN leg testable as soon as both devices run this branch):**
1. Both devices enrolled (asset certs present), same org, both running this agent build (`wendy device update --binary`).
2. Device B: `wendy run` HelloHTTP (publishes host port 8080). Confirm `avahi-browse -rpt _wendyos._udp` on device A shows B's `assetid=<N>` TXT.
3. Device A: HelloMesh with `MESH_TARGET=device-<N>.cloud.wendy.dev:8080` → expect `OK 200`.
4. Kill the LAN path (block B's LAN IP or move A to another network) → next connection (after the 60s cache TTL) should relay via broker — requires the cloud-phase authz change; until then expect `PermissionDenied` in device A's agent log, which itself verifies the fallback fired.
5. Negative: touch `<configPath>/mesh-disabled` on B → LAN dials get `PermissionDenied`.
6. Check INPUT policy: container→gateway:53 UDP (DNS) and REDIRECTed TCP to :50058 terminate on the host — if either is dropped by the device's INPUT chain, DNS/proxy fail; verify with `iptables -S INPUT` and fix in the OS image if needed (record findings in the PR).

## Out of scope (tracked separately)

- **Cloud phase** (~/git/wendy/cloud, Swift): `orgs.mesh_enabled` flag + API, broker accepting asset certs on `ClientTunnel` (same-org + flag), BrokerFixture authz tests, syncing the flag to devices' `mesh-disabled` file. Gets its own spec/plan in that repo.
- UDP, per-device allowlists, service-name discovery, non-default serviceCIDRs, dashboard UI.
