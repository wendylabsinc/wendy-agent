# Deterministic NCM USB-C Connectivity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make USB-C-connected Jetson/Pi devices reachable at a fixed IPv6 link-local address (`fe80::5741:1`) so the CLI dials them directly — no mDNS browse, no DHCP, no sudo/nmcli, no ARP guessing.

**Architecture:** The **agent** (which runs on the device and updates OTA) self-assigns the well-known link-local address on its USB gadget interface via netlink, and reports its hostname in `GetAgentVersion` so the CLI can identify the device it reached. The **CLI** enumerates USB-backed host interfaces and dials `[fe80::5741:1%<iface>]` directly, feeding both discovery and a connect-time fallback. Old devices/agents simply don't answer at the well-known address and fall through to today's mDNS path unchanged.

**Design delta vs the spec** (`specs/2026-08-07-usb-deterministic-ncm-design.md`): the spec assigned the address in WendyOS-Builder's `gadget-network-config` and required no agent changes. During planning we found (a) the device's `usb-gadget.nmconnection` uses `ipv6.method=link-local`, where NetworkManager does not honor static addresses, and NM flushes externally-added addresses on re-activation — so a one-line builder change is not actually sufficient; and (b) `GetAgentVersionResponse` has no hostname field, so the CLI could not identify which device answered the well-known address. Both are solved agent-side instead: the agent ensures the address itself (works on **already-shipped** WendyOS via a mere agent update — better rollout than an OS image change) and reports its hostname (proto field 20, backward compatible). No WendyOS-Builder change is needed for v1.

**Tech Stack:** Go 1.26 (module root = repo root `go.mod`, module `github.com/wendylabsinc/wendy`), gRPC, `vishvananda/netlink` (already a direct dep), Go stdlib `testing` package.

## Global Constraints

- Well-known address: `fe80::5741:1` (link-local, /64, defined ONCE as `discovery.WellKnownUSBAddr`).
- Agent ports: plaintext `50051` (`defaultAgentPort` const in `go/internal/cli/commands/helpers.go:38`), mTLS `50052` (port+1).
- All commands below run from the **`go/` directory** unless stated otherwise.
- Before every push: `cd /Users/ethan/Documents/WendyAgent && gofmt -l go/` must print nothing; run `gofmt -w` on anything listed.
- Branch: all commits go on `ed/usb-deterministic-ncm` (create it from `ed/usb-deterministic-ncm-spec`, which holds the design spec: `git checkout ed/usb-deterministic-ncm-spec && git checkout -b ed/usb-deterministic-ncm`).
- Proto regen: `make proto` from `go/` (runs `bash scripts/generate-proto.sh`); source of truth is `Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto` (repo root), generated code lands in `go/proto/gen/agentpb/`.
- The agent binds `[::]:50051` / `[::]:50052` (`go/cmd/wendy-agent/main.go:705,578`) — dual-stack, so it already accepts IPv6 link-local connections. Do not change the listeners.
- Never remove the existing mDNS/nmcli paths — the well-known-address path is additive; old devices must keep working through today's code.

---

### Task 1: Well-known address constant + USB dial candidates (discovery package)

**Files:**
- Create: `go/internal/shared/discovery/usb_direct.go`
- Create: `go/internal/shared/discovery/usb_direct_test.go`
- Modify: `go/internal/shared/discovery/usb_connection.go:92-121` (`looksLikeUSBConnection` — add `ncm`)
- Test (extend): `go/internal/shared/discovery/usb_connection_test.go`

**Interfaces:**
- Consumes: existing unexported `looksLikeUSBConnection(interfaceName, displayName string) bool` (same package).
- Produces (used by Tasks 3, 4, 6):
  - `const WellKnownUSBAddr = "fe80::5741:1"`
  - `type USBDirectCandidate struct { Interface string; Zone string }`
  - `func (c USBDirectCandidate) HostPort(port int) string` → `"[fe80::5741:1%<zone>]:<port>"`
  - `func USBDirectCandidates() []USBDirectCandidate`

- [ ] **Step 1: Write the failing tests**

Create `go/internal/shared/discovery/usb_direct_test.go`:

```go
package discovery

import (
	"net"
	"testing"
)

func TestUSBDirectCandidateHostPort(t *testing.T) {
	c := USBDirectCandidate{Interface: "enxaabbccddeeff", Zone: "enxaabbccddeeff"}
	got := c.HostPort(50051)
	want := "[fe80::5741:1%enxaabbccddeeff]:50051"
	if got != want {
		t.Fatalf("HostPort = %q, want %q", got, want)
	}
}

func TestUSBDirectCandidatesFrom(t *testing.T) {
	ifaces := []net.Interface{
		{Index: 3, Name: "enxaabbccddeeff", Flags: net.FlagUp},            // USB by name → candidate
		{Index: 4, Name: "lo0", Flags: net.FlagUp | net.FlagLoopback},     // loopback → skipped
		{Index: 5, Name: "wlan1", Flags: net.FlagUp},                      // not USB → skipped
		{Index: 6, Name: "usb0", Flags: 0},                                // USB name but DOWN → skipped
		{Index: 7, Name: "ncm0", Flags: net.FlagUp},                       // NCM adapter → candidate
	}

	got := usbDirectCandidatesFrom(ifaces, "linux")
	want := []USBDirectCandidate{
		{Interface: "enxaabbccddeeff", Zone: "enxaabbccddeeff"},
		{Interface: "ncm0", Zone: "ncm0"},
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Windows zones are numeric interface indexes, not names.
	gotWin := usbDirectCandidatesFrom(ifaces, "windows")
	if len(gotWin) != 2 || gotWin[0].Zone != "3" || gotWin[1].Zone != "7" {
		t.Fatalf("windows candidates = %+v, want zones \"3\" and \"7\"", gotWin)
	}
}
```

Add to `go/internal/shared/discovery/usb_connection_test.go` (follow the existing test style in that file):

```go
func TestLooksLikeUSBConnectionNCM(t *testing.T) {
	if !looksLikeUSBConnection("ncm0", "") {
		t.Fatal("ncm0 should be classified as a USB connection")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/shared/discovery/ -run 'TestUSBDirect|TestLooksLikeUSBConnectionNCM' -v`
Expected: FAIL — `undefined: USBDirectCandidate` / `undefined: usbDirectCandidatesFrom`, and `ncm0` not matching (it currently falls through to the sysfs check, which fails for a nonexistent interface).

- [ ] **Step 3: Implement**

Create `go/internal/shared/discovery/usb_direct.go`:

```go
package discovery

import (
	"net"
	"runtime"
	"strconv"
)

// WellKnownUSBAddr is the fixed IPv6 link-local address every WendyOS device
// assigns to its USB gadget interface (0x57 0x41 = "WA"). Dialing it on a
// USB-backed host interface reaches the device with zero host configuration —
// no DHCP lease, no mDNS resolution, no NetworkManager profile. See
// specs/2026-08-07-usb-deterministic-ncm-design.md.
const WellKnownUSBAddr = "fe80::5741:1"

// USBDirectCandidate identifies one USB-backed host interface on which the
// well-known device address can be dialed.
type USBDirectCandidate struct {
	// Interface is the host interface name, used for display and for the
	// LANDevice USB annotation.
	Interface string
	// Zone is the IPv6 zone identifier for dialing: the interface name on
	// unix-likes, the numeric interface index on Windows (Windows zone IDs
	// are indexes, and Go's dialer passes them through verbatim).
	Zone string
}

// HostPort returns the dialable "[fe80::5741:1%zone]:port" address.
func (c USBDirectCandidate) HostPort(port int) string {
	return net.JoinHostPort(WellKnownUSBAddr+"%"+c.Zone, strconv.Itoa(port))
}

// netInterfacesFn lists host network interfaces; a package var so tests can
// inject fixtures (mirrors osLookupHostFn-style seams elsewhere in the CLI).
var netInterfacesFn = net.Interfaces

// USBDirectCandidates returns one dial candidate per up, non-loopback,
// USB-backed network interface on this host. An empty result means no USB
// gadget link is present.
func USBDirectCandidates() []USBDirectCandidate {
	ifaces, err := netInterfacesFn()
	if err != nil {
		return nil
	}
	return usbDirectCandidatesFrom(ifaces, runtime.GOOS)
}

func usbDirectCandidatesFrom(ifaces []net.Interface, goos string) []USBDirectCandidate {
	var out []USBDirectCandidate
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagLoopback != 0 || ifaces[i].Flags&net.FlagUp == 0 {
			continue
		}
		if !looksLikeUSBConnection(ifaces[i].Name, "") {
			continue
		}
		zone := ifaces[i].Name
		if goos == "windows" {
			zone = strconv.Itoa(ifaces[i].Index)
		}
		out = append(out, USBDirectCandidate{Interface: ifaces[i].Name, Zone: zone})
	}
	return out
}
```

In `go/internal/shared/discovery/usb_connection.go`, add an `ncm` case to `looksLikeUSBConnection`'s switch, directly after the existing `ecm` case:

```go
	case strings.Contains(combined, "ncm"):
		return true
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/shared/discovery/ -v`
Expected: PASS (the whole package, to catch regressions in the existing classification tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/discovery/usb_direct.go go/internal/shared/discovery/usb_direct_test.go go/internal/shared/discovery/usb_connection.go go/internal/shared/discovery/usb_connection_test.go
git commit -m "feat(discovery): well-known USB link-local address + dial candidates"
```

---

### Task 2: `hostname` in GetAgentVersionResponse (proto + agent)

**Files:**
- Modify: `Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto` (message `GetAgentVersionResponse`, last field is `cpu_count = 19`)
- Regenerate: `go/proto/gen/agentpb/` via `make proto`
- Modify: `go/internal/agent/services/agent_service.go:80` (`GetAgentVersion`)
- Test (extend): `go/internal/agent/services/agent_service_test.go`

**Interfaces:**
- Produces (used by Tasks 4 and 6): `GetAgentVersionResponse.hostname` (proto field 20) → Go getter `resp.GetHostname() string`. Empty string on agents predating the field — callers MUST treat empty as "identity unknown".

- [ ] **Step 1: Add the proto field**

In `Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto`, inside `message GetAgentVersionResponse`, after `uint32 cpu_count = 19;`:

```proto
    // Device hostname (gethostname(2)). The CLI uses it to identify a device
    // reached over the USB well-known link-local address, where no mDNS TXT
    // records are available. Empty on agents predating this field.
    string hostname = 20;
```

- [ ] **Step 2: Regenerate**

Run: `make proto`
Expected: `go/proto/gen/agentpb/wendy_agent_v1_service.pb.go` regenerates with a `Hostname` field + `GetHostname()` getter. `git diff --stat go/proto/gen/` should show only generated files changed. If unrelated files churn with protoc version strings, follow the repo's established recovery: regenerate, then `git checkout -- <files unrelated to this change>`.

- [ ] **Step 3: Write the failing agent test**

Add to `go/internal/agent/services/agent_service_test.go` (the method reads no receiver fields, so a zero-value service is safe — mirror existing construction in that file if it differs):

```go
func TestGetAgentVersionReportsHostname(t *testing.T) {
	s := &AgentService{}
	resp, err := s.GetAgentVersion(context.Background(), &agentpb.GetAgentVersionRequest{})
	if err != nil {
		t.Fatalf("GetAgentVersion: %v", err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable: %v", err)
	}
	if resp.GetHostname() != host {
		t.Fatalf("Hostname = %q, want %q", resp.GetHostname(), host)
	}
}
```

Run: `go test ./internal/agent/services/ -run TestGetAgentVersionReportsHostname -v`
Expected: FAIL — `Hostname = ""`.

- [ ] **Step 4: Implement**

In `go/internal/agent/services/agent_service.go`, inside `GetAgentVersion` after the `resp := &agentpb.GetAgentVersionResponse{...}` literal (the `os` package is already imported in this file):

```go
	if hn, err := os.Hostname(); err == nil {
		resp.Hostname = hn
	}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/agent/services/ -run TestGetAgentVersion -v`
Expected: PASS (new test and any existing GetAgentVersion tests).

- [ ] **Step 6: Commit**

```bash
git add Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto go/proto/gen/ go/internal/agent/services/agent_service.go go/internal/agent/services/agent_service_test.go
git commit -m "feat(agent): report device hostname in GetAgentVersion"
```

---

### Task 3: Agent self-assigns the well-known address on gadget interfaces

**Files:**
- Create: `go/internal/agent/usbgadget/lladdr_linux.go`
- Create: `go/internal/agent/usbgadget/lladdr_other.go`
- Create: `go/internal/agent/usbgadget/lladdr_linux_test.go`
- Modify: `go/cmd/wendy-agent/main.go` (after the agent gRPC server goroutine that logs "Agent gRPC server listening", ~line 718, before the "Local control socket" comment block; `ctx` from line 269 and `logger` are in scope)

**Interfaces:**
- Consumes: `discovery.WellKnownUSBAddr` (Task 1).
- Produces: `func EnsureWellKnownAddress(ctx context.Context, interval time.Duration, logger *zap.Logger)` in package `usbgadget` — blocks until ctx cancels; call it in a goroutine. The `lladdr_other.go` variant is a no-op that returns immediately.

Rationale (from the design delta): NetworkManager's `ipv6.method=link-local` profile on the device does not carry static addresses and flushes foreign addresses on re-activation, and the Jetson FUSB301 role-switch can bring `usb0` up late — so a periodic re-ensure loop in the agent is the robust mechanism, and it reaches already-shipped devices through a normal agent update.

- [ ] **Step 1: Write the failing test**

Create `go/internal/agent/usbgadget/lladdr_linux_test.go`:

```go
//go:build linux

package usbgadget

import (
	"context"
	"net"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"go.uber.org/zap"
)

func TestEnsureOnceAddsAddressToGadgetInterfacesOnly(t *testing.T) {
	origIfaces, origLink, origAdd := netInterfacesFn, linkByNameFn, addrAddFn
	defer func() { netInterfacesFn, linkByNameFn, addrAddFn = origIfaces, origLink, origAdd }()

	netInterfacesFn = func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 2, Name: "eth0", Flags: net.FlagUp},
			{Index: 3, Name: "usb0", Flags: net.FlagUp},
			{Index: 4, Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
		}, nil
	}
	var lookedUp, added []string
	linkByNameFn = func(name string) (netlink.Link, error) {
		lookedUp = append(lookedUp, name)
		return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}, nil
	}
	addrAddFn = func(link netlink.Link, addr *netlink.Addr) error {
		added = append(added, link.Attrs().Name+" "+addr.IPNet.String())
		return nil
	}

	ensureOnce(context.Background(), zap.NewNop())

	if len(lookedUp) != 1 || lookedUp[0] != "usb0" {
		t.Fatalf("looked up %v, want only usb0", lookedUp)
	}
	if len(added) != 1 || added[0] != "usb0 fe80::5741:1/64" {
		t.Fatalf("added %v, want [\"usb0 fe80::5741:1/64\"]", added)
	}
}

func TestEnsureOnceTreatsEEXISTAsSuccess(t *testing.T) {
	origIfaces, origLink, origAdd := netInterfacesFn, linkByNameFn, addrAddFn
	defer func() { netInterfacesFn, linkByNameFn, addrAddFn = origIfaces, origLink, origAdd }()

	netInterfacesFn = func() ([]net.Interface, error) {
		return []net.Interface{{Index: 3, Name: "usb0", Flags: net.FlagUp}}, nil
	}
	linkByNameFn = func(name string) (netlink.Link, error) {
		return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}, nil
	}
	addrAddFn = func(netlink.Link, *netlink.Addr) error { return syscall.EEXIST }

	// Must not panic and must not log at error level; just verify it completes.
	ensureOnce(context.Background(), zap.NewNop())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOOS=linux go build ./internal/agent/usbgadget/ 2>&1 | head -5; go test ./internal/agent/usbgadget/ -v`
Expected: FAIL/build error — package does not exist yet. (On macOS the `_linux` test file is skipped by build tags; the compile check with `GOOS=linux go build` is the meaningful signal there. The test itself runs in CI on Linux; on a Mac dev machine, at minimum the cross-compile must pass.)

- [ ] **Step 3: Implement**

Create `go/internal/agent/usbgadget/lladdr_linux.go`:

```go
//go:build linux

// Package usbgadget keeps the device's USB gadget network interface reachable
// at the well-known IPv6 link-local address that the CLI dials directly
// (specs/2026-08-07-usb-deterministic-ncm-design.md).
package usbgadget

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// Seams for tests.
var (
	netInterfacesFn = net.Interfaces
	linkByNameFn    = netlink.LinkByName
	addrAddFn       = netlink.AddrAdd
)

// EnsureWellKnownAddress applies the well-known link-local address to every
// USB gadget interface, once immediately and then every interval until ctx is
// cancelled. The periodic re-apply survives NetworkManager re-activating the
// usb-gadget profile (which flushes addresses it did not configure) and the
// gadget interface appearing late (Jetson USB role-switch races).
func EnsureWellKnownAddress(ctx context.Context, interval time.Duration, logger *zap.Logger) {
	ensureOnce(ctx, logger)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ensureOnce(ctx, logger)
		}
	}
}

func ensureOnce(_ context.Context, logger *zap.Logger) {
	ifaces, err := netInterfacesFn()
	if err != nil {
		return
	}
	for i := range ifaces {
		name := ifaces[i].Name
		// Device-side gadget interfaces are usbN on every supported board
		// (see the usb-gadget.nmconnection profile, pinned to usb0).
		if ifaces[i].Flags&net.FlagLoopback != 0 || !strings.HasPrefix(name, "usb") {
			continue
		}
		link, err := linkByNameFn(name)
		if err != nil {
			continue
		}
		addr, err := netlink.ParseAddr(discovery.WellKnownUSBAddr + "/64")
		if err != nil {
			logger.Error("parsing well-known USB address", zap.Error(err))
			return
		}
		// nodad: the only peer on a gadget link is the USB host, which never
		// claims this address; skipping DAD makes the address usable instantly.
		addr.Flags = unix.IFA_F_NODAD
		addr.Scope = int(netlink.SCOPE_LINK)
		if err := addrAddFn(link, addr); err != nil && !errors.Is(err, syscall.EEXIST) {
			logger.Debug("adding well-known USB address", zap.String("iface", name), zap.Error(err))
		}
	}
}
```

Create `go/internal/agent/usbgadget/lladdr_other.go`:

```go
//go:build !linux

package usbgadget

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// EnsureWellKnownAddress is a no-op off Linux: only Linux devices expose a USB
// gadget interface (the macOS agent is never USB-attached hardware).
func EnsureWellKnownAddress(context.Context, time.Duration, *zap.Logger) {}
```

In `go/cmd/wendy-agent/main.go`, after the agent-server goroutine block that ends with:

```go
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("Agent gRPC server listening", zap.String("port", agentPort))
			if err := agentServer.Serve(agentLis); err != nil {
				logger.Error("Agent gRPC server error", zap.Error(err))
			}
		}()
	}
```

add (and add `"github.com/wendylabsinc/wendy/go/internal/agent/usbgadget"` to the imports):

```go
	// Keep the USB gadget interface reachable at the well-known link-local
	// address the CLI dials directly (no mDNS/DHCP needed over USB-C).
	go usbgadget.EnsureWellKnownAddress(ctx, 30*time.Second, logger)
```

- [ ] **Step 4: Run tests and cross-compile**

Run: `go test ./internal/agent/usbgadget/ -v && GOOS=linux GOARCH=arm64 go build ./cmd/wendy-agent/ && GOOS=darwin go build ./cmd/wendy-agent/`
Expected: tests PASS on Linux (or compile-only on macOS), both builds succeed.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/usbgadget/ go/cmd/wendy-agent/main.go
git commit -m "feat(agent): self-assign well-known USB link-local address"
```

---

### Task 4: CLI direct probe + resolveAddrOnce zone-aware guard

**Files:**
- Create: `go/internal/cli/commands/usb_direct.go`
- Create: `go/internal/cli/commands/usb_direct_test.go`
- Modify: `go/internal/cli/commands/helpers.go:1181-1190` (`resolveAddrOnce`)

**Interfaces:**
- Consumes: `discovery.USBDirectCandidates()` / `USBDirectCandidate.HostPort(port)` (Task 1); existing package var `getAgentVersionAtAddress func(ctx, address) (bool, *agentpb.GetAgentVersionResponse, error)` (`helpers.go:524`, already a test seam); `resp.GetHostname()` (Task 2); const `defaultAgentPort` (int, 50051); `normalizeMDNSHost` (`helpers.go:1223`); `models.LANDevice`, `models.InterfaceLAN`.
- Produces (used by Tasks 5, 6):
  - `var usbDirectCandidatesFn = discovery.USBDirectCandidates` (test seam)
  - `const usbDirectProbeBudget = 8 * time.Second`
  - `func probeUSBDirectDevices(ctx context.Context) []models.LANDevice`
  - `func mergeUSBDirectDevices(devices, probed []models.LANDevice) []models.LANDevice`

- [ ] **Step 1: Write the failing tests**

Create `go/internal/cli/commands/usb_direct_test.go`:

```go
package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func withUSBDirectStubs(t *testing.T, cands []discovery.USBDirectCandidate,
	probe func(ctx context.Context, address string) (bool, *agentpb.GetAgentVersionResponse, error)) {
	t.Helper()
	origCands, origProbe := usbDirectCandidatesFn, getAgentVersionAtAddress
	usbDirectCandidatesFn = func() []discovery.USBDirectCandidate { return cands }
	getAgentVersionAtAddress = probe
	t.Cleanup(func() { usbDirectCandidatesFn, getAgentVersionAtAddress = origCands, origProbe })
}

func TestProbeUSBDirectDevicesBuildsLANDevice(t *testing.T) {
	withUSBDirectStubs(t,
		[]discovery.USBDirectCandidate{{Interface: "enx001122334455", Zone: "enx001122334455"}},
		func(_ context.Context, address string) (bool, *agentpb.GetAgentVersionResponse, error) {
			want := "[fe80::5741:1%enx001122334455]:50051"
			if address != want {
				t.Errorf("probe address = %q, want %q", address, want)
			}
			return true, &agentpb.GetAgentVersionResponse{Hostname: "wendy-orin", Version: "1.2.3", Os: "linux"}, nil
		})

	devs := probeUSBDirectDevices(context.Background())
	if len(devs) != 1 {
		t.Fatalf("got %d devices, want 1", len(devs))
	}
	d := devs[0]
	if d.Hostname != "wendy-orin.local" || d.DisplayName != "wendy-orin" ||
		d.IPAddress != "fe80::5741:1%enx001122334455" || d.Port != 50051 ||
		!d.IsMTLS || d.USB == "" || d.NetworkInterface != "enx001122334455" ||
		!d.IsWendyDevice || d.AgentVersion != "1.2.3" || d.InterfaceType != string(models.InterfaceLAN) {
		t.Fatalf("unexpected device: %+v", d)
	}
}

func TestProbeUSBDirectDevicesSkipsNonAnswering(t *testing.T) {
	withUSBDirectStubs(t,
		[]discovery.USBDirectCandidate{{Interface: "enxdead", Zone: "enxdead"}},
		func(context.Context, string) (bool, *agentpb.GetAgentVersionResponse, error) {
			return false, nil, context.DeadlineExceeded
		})
	if devs := probeUSBDirectDevices(context.Background()); len(devs) != 0 {
		t.Fatalf("got %d devices, want 0", len(devs))
	}
}

func TestProbeUSBDirectDevicesNoCandidatesDialsNothing(t *testing.T) {
	withUSBDirectStubs(t, nil,
		func(context.Context, string) (bool, *agentpb.GetAgentVersionResponse, error) {
			t.Fatal("probe must not be called with no candidates")
			return false, nil, nil
		})
	if devs := probeUSBDirectDevices(context.Background()); devs != nil {
		t.Fatalf("got %v, want nil", devs)
	}
}

func TestMergeUSBDirectDevices(t *testing.T) {
	existing := []models.LANDevice{
		{Hostname: "wendy-orin.local", IPAddress: "192.168.1.50", Port: 50051, ID: "abc123"},
		{Hostname: "wendy-pi.local", IPAddress: "192.168.1.60", Port: 50051},
	}
	probed := []models.LANDevice{
		// Matches wendy-orin by hostname → enriches, does not duplicate.
		{Hostname: "wendy-orin.local", DisplayName: "wendy-orin", IPAddress: "fe80::5741:1%enxa", NetworkInterface: "enxa", USB: "enxa", Port: 50051},
		// No mDNS counterpart → appended.
		{Hostname: "wendy-thor.local", DisplayName: "wendy-thor", IPAddress: "fe80::5741:1%enxb", NetworkInterface: "enxb", USB: "enxb", Port: 50051},
	}

	got := mergeUSBDirectDevices(existing, probed)
	if len(got) != 3 {
		t.Fatalf("got %d devices, want 3: %+v", len(got), got)
	}
	orin := got[0]
	if orin.ID != "abc123" || orin.USB != "enxa" || orin.NetworkInterface != "enxa" {
		t.Fatalf("orin not enriched: %+v", orin)
	}
	if orin.IPAddress != "192.168.1.50" {
		t.Fatalf("mDNS-discovered address must be preserved, got %q", orin.IPAddress)
	}
	if got[2].Hostname != "wendy-thor.local" {
		t.Fatalf("probed-only device not appended: %+v", got[2])
	}
}

func TestResolveAddrOnceSkipsResolutionForZonedIPLiteral(t *testing.T) {
	origLookup := osLookupHostFn
	osLookupHostFn = func(context.Context, string) ([]string, error) {
		t.Fatal("resolver must not run for a zoned IPv6 literal")
		return nil, nil
	}
	t.Cleanup(func() { osLookupHostFn = origLookup })

	addr := "[fe80::5741:1%enx0]:50051"
	if got := resolveAddrOnce(context.Background(), addr); got != addr {
		t.Fatalf("resolveAddrOnce = %q, want unchanged %q", got, addr)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/commands/ -run 'TestProbeUSBDirect|TestMergeUSBDirect|TestResolveAddrOnceSkips' -v`
Expected: FAIL — `undefined: usbDirectCandidatesFn`, `undefined: probeUSBDirectDevices`, etc.; the resolveAddrOnce test fails via the `t.Fatal` in the resolver stub.

- [ ] **Step 3: Implement**

Create `go/internal/cli/commands/usb_direct.go`:

```go
package commands

import (
	"context"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// usbDirectProbeBudget bounds one well-known-address probe. A dead candidate
// fails within one NDP neighbor-resolution timeout (~3s); a live agent answers
// the TCP SYN immediately but its ML-DSA mTLS handshake can take several
// seconds on Jetson-class hardware (see mtlsProbeTimeout), hence the headroom.
const usbDirectProbeBudget = 8 * time.Second

// usbDirectCandidatesFn is a seam for tests.
var usbDirectCandidatesFn = discovery.USBDirectCandidates

// probeUSBDirectDevices dials the well-known USB link-local address on every
// USB-backed host interface in parallel and returns a LANDevice for each Wendy
// agent that answers. Devices on WendyOS images without the well-known address
// (or with no agent listening) simply never answer; the caller's mDNS path
// still covers those. Identity comes from GetAgentVersion instead of mDNS TXT
// records, so no multicast needs to work on the link.
func probeUSBDirectDevices(ctx context.Context) []models.LANDevice {
	candidates := usbDirectCandidatesFn()
	if len(candidates) == 0 {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, usbDirectProbeBudget)
	defer cancel()

	results := make([]*models.LANDevice, len(candidates))
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		go func(i int, cand discovery.USBDirectCandidate) {
			defer wg.Done()
			isMTLS, resp, err := getAgentVersionAtAddress(pctx, cand.HostPort(defaultAgentPort))
			if err != nil || resp == nil {
				return
			}
			hostname := discovery.SanitiseDisplayName(resp.GetHostname())
			dev := models.LANDevice{
				DisplayName:      hostname,
				IPAddress:        discovery.WellKnownUSBAddr + "%" + cand.Zone,
				Port:             defaultAgentPort,
				IsMTLS:           isMTLS,
				InterfaceType:    string(models.InterfaceLAN),
				NetworkInterface: cand.Interface,
				USB:              cand.Interface,
				IsWendyDevice:    true,
				AgentVersion:     resp.GetVersion(),
				OS:               resp.GetOs(),
				OSVersion:        resp.GetOsVersion(),
				CPUArchitecture:  resp.GetCpuArchitecture(),
			}
			if resp.GetDeviceType() != "" {
				dev.DeviceType = resp.GetDeviceType()
			}
			if hostname != "" {
				dev.Hostname = hostname + ".local"
			} else {
				// Pre-hostname-field agent: still show it, identified by port.
				dev.DisplayName = "USB device (" + cand.Interface + ")"
			}
			results[i] = &dev
		}(i, candidates[i])
	}
	wg.Wait()

	var out []models.LANDevice
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// mergeUSBDirectDevices folds direct-probe results into an mDNS-discovered LAN
// device list. A probed device whose hostname matches an existing entry
// enriches it in place (USB annotation, interface); identity fields and the
// mDNS-advertised address are preserved, since preferDiscoveredLANDevice
// scoring and lanAgentAddresses ordering already know how to dial USB-annotated
// devices. A probed device with no counterpart is appended — that is the case
// where mDNS is broken and the direct probe is the only path.
func mergeUSBDirectDevices(devices, probed []models.LANDevice) []models.LANDevice {
	for _, p := range probed {
		matched := false
		for i := range devices {
			sameHost := p.Hostname != "" && normalizeMDNSHost(devices[i].Hostname) == normalizeMDNSHost(p.Hostname)
			sameIface := devices[i].NetworkInterface != "" && devices[i].NetworkInterface == p.NetworkInterface
			if !sameHost && !sameIface {
				continue
			}
			matched = true
			if devices[i].USB == "" {
				devices[i].USB = p.USB
			}
			if devices[i].NetworkInterface == "" {
				devices[i].NetworkInterface = p.NetworkInterface
			}
			if devices[i].IPAddress == "" {
				devices[i].IPAddress = p.IPAddress
			}
			if devices[i].AgentVersion == "" {
				devices[i].AgentVersion = p.AgentVersion
			}
			break
		}
		if !matched {
			devices = append(devices, p)
		}
	}
	return devices
}
```

Note on `OSVersion`/`DeviceType`: `GetOsVersion()`/`GetDeviceType()` return `string` (proto3 optional getters return the zero value when unset) — check the generated getters and drop the `if` wrapper if the getter already returns plain `string` for `OSVersion` too; assign directly in that case.

In `go/internal/cli/commands/helpers.go`, `resolveAddrOnce`, make the literal-IP guard zone-aware — replace:

```go
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return addr // not host:port, or already a literal IP
	}
```

with:

```go
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr // not host:port
	}
	hostNoZone := host
	if i := strings.IndexByte(hostNoZone, '%'); i >= 0 {
		hostNoZone = hostNoZone[:i] // net.ParseIP rejects zone suffixes
	}
	if net.ParseIP(hostNoZone) != nil {
		return addr // already a literal IP (possibly zoned, e.g. USB link-local)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/commands/ -run 'TestProbeUSBDirect|TestMergeUSBDirect|TestResolveAddrOnce' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/usb_direct.go go/internal/cli/commands/usb_direct_test.go go/internal/cli/commands/helpers.go
git commit -m "feat(cli): probe well-known USB address for direct device discovery"
```

---

### Task 5: Wire the probe into discover and the picker

**Files:**
- Modify: `go/internal/cli/commands/discover.go:123` (`discoverJSON`) and `:153` (`discoverOnce` work func) — the only two `discovery.Discover(` call sites in commands
- Modify: `go/internal/cli/commands/helpers.go:2302` (`pickDevice`, after the `go discovery.DiscoverLANContinuous(discoverCtx, lanCh)` line)
- Test (extend): `go/internal/cli/commands/usb_direct_test.go`

**Interfaces:**
- Consumes: `probeUSBDirectDevices`, `mergeUSBDirectDevices` (Task 4); `discovery.Discover`, `discovery.DiscoveryOptions`, `models.InterfaceLAN`.
- Produces: `func discoverWithUSBDirect(ctx context.Context, opts discovery.DiscoveryOptions) (*models.DevicesCollection, error)` — drop-in replacement for `discovery.Discover` in commands.

- [ ] **Step 1: Write the failing test**

Add to `go/internal/cli/commands/usb_direct_test.go`:

```go
func TestShouldProbeUSBDirect(t *testing.T) {
	cases := []struct {
		types []models.InterfaceType
		want  bool
	}{
		{nil, true}, // "all types"
		{[]models.InterfaceType{models.InterfaceLAN}, true},
		{[]models.InterfaceType{models.InterfaceBluetooth}, false},
	}
	for _, c := range cases {
		if got := shouldProbeUSBDirect(discovery.DiscoveryOptions{Types: c.types}); got != c.want {
			t.Fatalf("shouldProbeUSBDirect(%v) = %v, want %v", c.types, got, c.want)
		}
	}
}
```

Run: `go test ./internal/cli/commands/ -run TestShouldProbeUSBDirect -v`
Expected: FAIL — `undefined: shouldProbeUSBDirect`.

- [ ] **Step 2: Implement the wrapper**

Add to `go/internal/cli/commands/usb_direct.go`:

```go
// shouldProbeUSBDirect mirrors Discover's type filter: the probe produces
// LAN-type devices, so it runs whenever LAN discovery would.
func shouldProbeUSBDirect(opts discovery.DiscoveryOptions) bool {
	if len(opts.Types) == 0 {
		return true
	}
	for _, t := range opts.Types {
		if t == models.InterfaceLAN {
			return true
		}
	}
	return false
}

// discoverWithUSBDirect runs standard discovery and the USB well-known-address
// probe concurrently, then merges the probe results into the LAN device list.
// Drop-in replacement for discovery.Discover at command call sites.
func discoverWithUSBDirect(ctx context.Context, opts discovery.DiscoveryOptions) (*models.DevicesCollection, error) {
	var probed []models.LANDevice
	var wg sync.WaitGroup
	if shouldProbeUSBDirect(opts) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probed = probeUSBDirectDevices(ctx)
		}()
	}
	collection, err := discovery.Discover(ctx, opts)
	wg.Wait()
	if err == nil && len(probed) > 0 {
		collection.LANDevices = mergeUSBDirectDevices(collection.LANDevices, probed)
	}
	return collection, err
}
```

- [ ] **Step 3: Swap the call sites**

In `go/internal/cli/commands/discover.go`, replace both `discovery.Discover(ctx, opts)` calls (lines 123 and 153) with `discoverWithUSBDirect(ctx, opts)`. The existing post-processing (`resolveLANVersions`, `annotateLANUSBFromEthernet`, `sortLANDevicesForDiscover`) stays exactly as is — USB-annotated entries already sort first.

In `go/internal/cli/commands/helpers.go` (`pickDevice`), directly after:

```go
	go discovery.DiscoverLANContinuous(discoverCtx, lanCh)
```

add:

```go
	// USB well-known-address probe: a USB-attached device appears in the
	// picker even when mDNS is broken on this host. The picker's MergeItem
	// dedupes it against the mDNS entry for the same device.
	go func() {
		for _, dev := range probeUSBDirectDevices(discoverCtx) {
			select {
			case lanCh <- dev:
			case <-discoverCtx.Done():
				return
			}
		}
	}()
```

- [ ] **Step 4: Run tests and build**

Run: `go test ./internal/cli/commands/ -run 'TestShouldProbeUSBDirect|TestProbeUSBDirect|TestMergeUSBDirect' -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Run the full commands + discovery test suites**

Run: `go test ./internal/cli/commands/ ./internal/shared/discovery/ -count=1`
Expected: PASS — no regressions in discover/picker tests.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/usb_direct.go go/internal/cli/commands/usb_direct_test.go go/internal/cli/commands/discover.go go/internal/cli/commands/helpers.go
git commit -m "feat(cli): surface USB direct-probed devices in discover and picker"
```

---

### Task 6: Connect-time USB fallback with hostname identity check

**Files:**
- Modify: `go/internal/cli/commands/usb_direct.go` (add `usbDirectFallback`)
- Modify: `go/internal/cli/commands/helpers.go:999-1043` (`connectToAgent` error-branch chain)
- Test (extend): `go/internal/cli/commands/usb_direct_test.go`

**Interfaces:**
- Consumes: `usbDirectCandidatesFn`, `usbDirectProbeBudget` (Task 4); `connectWithAutoTLS(ctx, addr)` (`helpers.go:1114`); `normalizeMDNSHost`; `grpcclient.AgentConnection` (`AgentService` field is the `agentpb.WendyAgentServiceClient` interface — fakeable by embedding); `resp.GetHostname()` (Task 2).
- Produces: `func usbDirectFallback(ctx context.Context, wantHost string) (*grpcclient.AgentConnection, bool)` and the seam `var usbDirectConnectFn = connectWithAutoTLS`.

Safety property (from the spec's error-handling section): the fallback must return a connection ONLY when the device's reported hostname matches the requested device — silently connecting to whichever device happens to be on USB would target the wrong machine. An empty reported hostname (old agent) therefore never matches.

- [ ] **Step 1: Write the failing tests**

Add to `go/internal/cli/commands/usb_direct_test.go` (imports to add: `"google.golang.org/grpc"`, `"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"`):

```go
// fakeVersionClient overrides only GetAgentVersion; every other method of the
// embedded (nil) interface panics if called, which is what we want in tests.
type fakeVersionClient struct {
	agentpb.WendyAgentServiceClient
	resp *agentpb.GetAgentVersionResponse
	err  error
}

func (f fakeVersionClient) GetAgentVersion(context.Context, *agentpb.GetAgentVersionRequest, ...grpc.CallOption) (*agentpb.GetAgentVersionResponse, error) {
	return f.resp, f.err
}

func TestUSBDirectFallbackMatchesHostname(t *testing.T) {
	withUSBDirectStubs(t,
		[]discovery.USBDirectCandidate{{Interface: "enxa", Zone: "enxa"}},
		getAgentVersionAtAddress) // unused by fallback; keep original semantics

	origConnect := usbDirectConnectFn
	usbDirectConnectFn = func(_ context.Context, addr string) (*grpcclient.AgentConnection, error) {
		if addr != "[fe80::5741:1%enxa]:50051" {
			t.Errorf("dial addr = %q", addr)
		}
		return &grpcclient.AgentConnection{
			AgentService: fakeVersionClient{resp: &agentpb.GetAgentVersionResponse{Hostname: "wendy-orin"}},
		}, nil
	}
	t.Cleanup(func() { usbDirectConnectFn = origConnect })

	conn, ok := usbDirectFallback(context.Background(), "wendy-orin.local")
	if !ok || conn == nil {
		t.Fatal("expected a matched connection")
	}
}

func TestUSBDirectFallbackRejectsWrongOrUnknownHostname(t *testing.T) {
	for name, resp := range map[string]*agentpb.GetAgentVersionResponse{
		"wrong device":   {Hostname: "wendy-pi"},
		"old agent":      {Hostname: ""},
	} {
		t.Run(name, func(t *testing.T) {
			withUSBDirectStubs(t,
				[]discovery.USBDirectCandidate{{Interface: "enxa", Zone: "enxa"}},
				getAgentVersionAtAddress)
			origConnect := usbDirectConnectFn
			usbDirectConnectFn = func(context.Context, string) (*grpcclient.AgentConnection, error) {
				return &grpcclient.AgentConnection{AgentService: fakeVersionClient{resp: resp}}, nil
			}
			t.Cleanup(func() { usbDirectConnectFn = origConnect })

			if _, ok := usbDirectFallback(context.Background(), "wendy-orin.local"); ok {
				t.Fatal("must not connect to a device with a different or unknown hostname")
			}
		})
	}
}

func TestUSBDirectFallbackNoCandidates(t *testing.T) {
	withUSBDirectStubs(t, nil, getAgentVersionAtAddress)
	if _, ok := usbDirectFallback(context.Background(), "wendy-orin.local"); ok {
		t.Fatal("no candidates must mean no fallback")
	}
}
```

Run: `go test ./internal/cli/commands/ -run TestUSBDirectFallback -v`
Expected: FAIL — `undefined: usbDirectFallback` / `usbDirectConnectFn`.

- [ ] **Step 2: Implement the fallback**

Add to `go/internal/cli/commands/usb_direct.go` (imports to add: `"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"`, `"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"`):

```go
// usbDirectConnectFn is a seam for tests.
var usbDirectConnectFn = connectWithAutoTLS

// usbDirectFallback attempts to reach the requested device over the USB
// well-known address after normal resolution/connection failed (e.g. mDNS
// broken on this host, stale stored address). It returns a live connection
// ONLY when the device's reported hostname matches the requested one; an
// empty hostname (agent predating the field) never matches — connecting to
// whichever device happens to be plugged in would silently target the wrong
// machine.
func usbDirectFallback(ctx context.Context, wantHost string) (*grpcclient.AgentConnection, bool) {
	want := normalizeMDNSHost(wantHost)
	if want == "" {
		return nil, false
	}
	for _, cand := range usbDirectCandidatesFn() {
		pctx, cancel := context.WithTimeout(ctx, usbDirectProbeBudget)
		conn, err := usbDirectConnectFn(pctx, cand.HostPort(defaultAgentPort))
		if err != nil {
			cancel()
			continue
		}
		resp, verr := conn.AgentService.GetAgentVersion(pctx, &agentpb.GetAgentVersionRequest{})
		cancel()
		if verr != nil || resp.GetHostname() == "" || normalizeMDNSHost(resp.GetHostname()) != want {
			conn.Close()
			continue
		}
		return conn, true
	}
	return nil, false
}
```

- [ ] **Step 3: Wire into connectToAgent**

In `go/internal/cli/commands/helpers.go`, inside `connectToAgent`'s error handling: the chain currently reads (abridged):

```go
			if retried {
				conn = retriedConn
			} else if syncedConn, ok := autoSyncTimeAndRetry(...); ok {
				conn = syncedConn
			} else if errors.Is(connErr, errProvisionedAgentUnauthorized) {
				...
			} else if isDefault && !jsonOutput && !cfg.nonInteractive && isInteractiveTerminal() {
```

Insert a new branch immediately before the `isDefault && !jsonOutput` interactive-recovery branch (so time-sync and cert-refresh remedies, which fix THIS address, still run first):

```go
			} else if usbConn, ok := usbDirectFallback(ctx, hostname); ok {
				// The stored address is unreachable but the same device (verified
				// by hostname) is on USB — use it directly.
				conn = usbConn
			} else if isDefault && !jsonOutput && !cfg.nonInteractive && isInteractiveTerminal() {
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cli/commands/ -count=1`
Expected: PASS — including all existing connectToAgent-adjacent tests.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/usb_direct.go go/internal/cli/commands/usb_direct_test.go go/internal/cli/commands/helpers.go
git commit -m "feat(cli): fall back to USB well-known address when a device is unreachable"
```

---

### Task 7: Full-repo verification and cleanup

**Files:** none new — verification only.

- [ ] **Step 1: Format and vet**

Run from repo root: `gofmt -l go/` (must print nothing; `gofmt -w` anything listed), then from `go/`: `go vet ./...`
Expected: clean.

- [ ] **Step 2: Full test suite**

Run: `make test` (from `go/`; runs `go test ./... -v -count=1 -timeout 120s`)
Expected: PASS. Known pre-existing failures unrelated to this work (if any) must be listed in the final report, not silently ignored.

- [ ] **Step 3: Cross-compile all shipped targets**

Run:
```bash
GOOS=linux  GOARCH=arm64 go build ./cmd/wendy ./cmd/wendy-agent
GOOS=darwin GOARCH=arm64 go build ./cmd/wendy ./cmd/wendy-agent
GOOS=windows GOARCH=amd64 go build ./cmd/wendy
```
Expected: all succeed (Windows zone-index path and the linux-only usbgadget package both compile).

- [ ] **Step 4: Commit any final fixes**

```bash
git add -A go/
git commit -m "chore: gofmt/vet fixes for USB deterministic NCM" || true
```

---

### Task 8: On-device verification & throughput benchmark (HARDWARE — user-run)

This task needs a Pi 5 and/or Orin Nano on USB-C and cannot run in CI. Execute after Tasks 1–7 land on the device via a dev agent push (recipe in `project_voiceassistant_pi_volume_tools` memory / PR #1574 notes).

- [ ] **Step 1: Address present on device**

On the device: `ip -6 addr show dev usb0 | grep fe80::5741:1`
Expected: `inet6 fe80::5741:1/64 scope link nodad` (appears ≤30 s after agent start; survives `nmcli connection up usb-gadget`).

- [ ] **Step 2: Deterministic discovery without mDNS**

On the host, suppress mDNS influence (macOS: rely on the probe path; Linux: `sudo systemctl stop avahi-daemon` first), then: `time wendy discover`
Expected: the USB device appears with its hostname in < 2 s of scan start; restart avahi afterwards on Linux.

- [ ] **Step 3: Deterministic connect**

`time wendy apps list --device <hostname>.local` (a cheap agent RPC) with only USB attached.
Expected: connects in < 1 s after the address is cached; no sudo/nmcli prompt at any point.

- [ ] **Step 4: Fallback correctness**

With the device's Wi-Fi disconnected (stored LAN address stale): run any agent command against the stored device name.
Expected: command succeeds via the USB fallback; `WENDY_TLS_DEBUG=1` output shows the `fe80::5741:1%` dial. Then plug in a DIFFERENT device and request the first device's name: expected: clean "could not be reached" error, NOT a connection to the wrong device.

- [ ] **Step 5: Throughput baseline and regression check**

Measure a chunk-diff deploy over USB before and after this branch (same app, warm cache invalidated the same way):
```bash
time wendy run --device <hostname>.local   # note the push-phase duration both runs
```
Expected: post-branch throughput ≥ pre-branch (the transport is unchanged; only resolution latency should drop). Record MB/s for the tuning follow-up.

- [ ] **Step 6: Link-speed inventory for the SuperSpeed follow-up**

On each board: `cat /sys/class/udc/*/current_speed /sys/class/udc/*/maximum_speed`
Record per board (Pi 4/5 expected `high-speed`; Orin may support `super-speed`). If any Orin reports `maximum_speed: super-speed` but `current_speed: high-speed`, file the WendyOS-Builder follow-up to enable SS device mode (out of scope for this plan, per the spec's measurement-first rule).

---

## Self-review notes

- Spec coverage: well-known address (T1/T3), direct dial + identity (T2/T4), discover integration (T5), connect fast-path/fallback (T6), interface hardening incl. `ncm` + Windows zones (T1/T4), old-device fallback (probe simply times out; T4/T6 tests), error handling (wrong-peer via hostname check T6; ModemManager untouched), testing (unit per task, on-device T8), throughput measurement-first (T8). The spec's builder one-liner is superseded by T3 — see the design-delta paragraph.
- The `nmcli` shared-mode auto-offer is intentionally untouched (spec: kept for device-upstream internet, decoupled from connectivity).
- Deliberate narrowing of the spec's "race the direct dial against the existing resolution": the plan connects via USB as a fallback after the normal path fails (T6) rather than racing during resolution. When mDNS works, today's path is already fast; racing would add up to ~8 s of dead-probe latency to the legacy-USB-device resolution path (a user-visible regression the spec forbids). If field usage shows the broken-mDNS fallback latency matters, a concurrent race scoped to hostname-matched candidates is the follow-up.
