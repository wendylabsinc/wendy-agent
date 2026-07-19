# Mesh Friendly-Name Addressing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a meshed container reach a peer by `<devicename>.<org-slug>.cloud.wendy.dev` in addition to the existing arithmetic `device-<N>.cloud.wendy.dev`.

**Architecture:** A thin name→asset-id resolver layer is inserted into the mesh DNS server before the existing VIP arithmetic. Resolution is hybrid: mDNS-first for LAN peers (filtered to the dialer's own org id), cloud-roster fallback for off-LAN peers. The org slug is derived by normalizing the cloud `Organization.name`, learned once from a new narrow `GetMeshRoster` cloud RPC. Everything downstream of the resolved asset id (VIP mapping, proxy, peer dialer) is unchanged.

**Tech Stack:** Go 1.26 (agent), `github.com/miekg/dns`, `github.com/wendylabsinc/wendy/go/internal/shared/discovery` (mDNS), gRPC + `cloudpb` (cloud roster), protoc codegen. Cloud RPC handler: Swift (`~/git/wendy/cloud`, separate repo — see Task 9).

## Global Constraints

- Module path: `github.com/wendylabsinc/wendy`; agent code under `go/`.
- The numeric `device-<N>.cloud.wendy.dev` path MUST remain byte-for-byte unchanged in behavior; friendly names are purely additive.
- A device only resolves within **its own org**: a friendly name whose `<org-slug>` ≠ this device's own org slug returns NXDOMAIN. No cross-org resolution in v1.
- All mesh plumbing is best-effort: resolver/roster failures log a warning and never kill the agent, never affect the numeric path, never affect non-mesh containers.
- DNS labels are `[a-z0-9-]` only. Normalization rule (single source of truth, `mesh.Normalize`): lowercase; replace runs of any char not in `[a-z0-9-]` with a single `-`; trim leading/trailing `-`.
- The `mesh` package must not gain a gRPC or mDNS import; those live in `services`/`discovery` and reach `mesh` only through the injected `Resolver` interface (mirrors the existing `mesh.PeerDialer` seam).
- Cloud identity for agent→cloud calls is carried in gRPC metadata `x-wendy-client-cert` / `x-forwarded-client-cert` = `URI=urn:wendy:org:<orgID>:asset:<assetID>` over server-auth-only TLS, exactly as `brokerDialOpts` builds it (`go/internal/agent/services/tunnel_broker_client.go:154`). Reuse that function; do not invent a new auth path.
- Run agent Go tests with `cd go && go test ./...`; build with `cd go && go build ./...`.

---

### Task 1: Slug/name normalization

**Files:**
- Create: `go/internal/agent/mesh/slug.go`
- Test: `go/internal/agent/mesh/slug_test.go`

**Interfaces:**
- Produces: `func Normalize(s string) string` — used by the DNS grammar (Task 2), the resolver (Task 7), and the roster cache (Task 6) to canonicalize both device names and org slugs to a DNS label.

- [ ] **Step 1: Write the failing test**

```go
package mesh

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"brave-dolphin": "brave-dolphin",
		"Brave Dolphin": "brave-dolphin",
		"ACME Corp.":    "acme-corp",
		"acme_corp":     "acme-corp",
		"  spaced  ":    "spaced",
		"a--b__c":       "a-b-c",
		"Wendy Labs, Inc": "wendy-labs-inc",
		"":              "",
		"---":           "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/mesh/ -run TestNormalize -v`
Expected: FAIL — `undefined: Normalize`.

- [ ] **Step 3: Write minimal implementation**

```go
package mesh

import (
	"regexp"
	"strings"
)

// nonLabelRE matches any run of characters that are not valid in a DNS label.
var nonLabelRE = regexp.MustCompile(`[^a-z0-9]+`)

// Normalize canonicalizes a device name or org name to a single DNS label:
// lowercase, every run of non-[a-z0-9] characters collapsed to one hyphen,
// and leading/trailing hyphens trimmed. It is the single source of truth for
// how friendly names and org slugs are compared across the mesh.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonLabelRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/agent/mesh/ -run TestNormalize -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/mesh/slug.go go/internal/agent/mesh/slug_test.go
git commit -m "feat(mesh): DNS-label normalization for friendly names and org slugs"
```

---

### Task 2: Friendly-name grammar + Resolver seam in the DNS server

**Files:**
- Modify: `go/internal/agent/mesh/dns.go` (add regex near `:16`, struct field + `SetResolver`, handle branch in `handle` at `:102`)
- Modify: `go/internal/agent/mesh/dns_test.go`

**Interfaces:**
- Consumes: `Normalize` (Task 1), `VIPForDevice` (`vip.go:28`).
- Produces:
  - `type Resolver interface { Resolve(name string) (assetID int32, ok bool); OrgSlug() string }`
  - `func (s *DNSServer) SetResolver(r Resolver)` — injected after construction (the DNS server is built at `go/cmd/wendy-agent/main.go:171` before provisioning identity exists, so the resolver is set later, like `SetMeshDNS`).
  - `friendlyNameRE` matching `^([a-z0-9-]+)\.([a-z0-9-]+)\.cloud\.wendy\.dev\.$`.

- [ ] **Step 1: Write the failing test**

```go
// add to dns_test.go
package mesh

import (
	"net"
	"testing"

	"github.com/miekg/dns"
	"go.uber.org/zap/zaptest"
)

// fakeResolver is a test double for the friendly-name resolver.
type fakeResolver struct {
	slug  string
	byName map[string]int32
}

func (f fakeResolver) OrgSlug() string { return f.slug }
func (f fakeResolver) Resolve(name string) (int32, bool) {
	id, ok := f.byName[name]
	return id, ok
}

func answerA(t *testing.T, s *DNSServer, qname string) (rcode int, ip string) {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(qname, dns.TypeA)
	rw := &captureWriter{}
	s.handle(rw, m)
	if rw.msg == nil {
		t.Fatal("no reply written")
	}
	if len(rw.msg.Answer) == 0 {
		return rw.msg.Rcode, ""
	}
	return rw.msg.Rcode, rw.msg.Answer[0].(*dns.A).A.String()
}

func TestFriendlyNameResolves(t *testing.T) {
	s := NewDNSServer(zaptest.NewLogger(t), "")
	s.SetResolver(fakeResolver{slug: "acme", byName: map[string]int32{"brave-dolphin": 215}})

	rcode, ip := answerA(t, s, "brave-dolphin.acme.cloud.wendy.dev.")
	if rcode != dns.RcodeSuccess || ip != "10.99.0.215" {
		t.Fatalf("got rcode=%d ip=%q, want SUCCESS 10.99.0.215", rcode, ip)
	}
}

func TestFriendlyNameWrongOrgIsNXDOMAIN(t *testing.T) {
	s := NewDNSServer(zaptest.NewLogger(t), "")
	s.SetResolver(fakeResolver{slug: "acme", byName: map[string]int32{"brave-dolphin": 215}})

	rcode, _ := answerA(t, s, "brave-dolphin.other-org.cloud.wendy.dev.")
	if rcode != dns.RcodeNameError {
		t.Fatalf("wrong-org: got rcode=%d, want NXDOMAIN", rcode)
	}
}

func TestFriendlyNameUnknownDeviceIsNXDOMAIN(t *testing.T) {
	s := NewDNSServer(zaptest.NewLogger(t), "")
	s.SetResolver(fakeResolver{slug: "acme", byName: map[string]int32{}})

	rcode, _ := answerA(t, s, "ghost.acme.cloud.wendy.dev.")
	if rcode != dns.RcodeNameError {
		t.Fatalf("unknown device: got rcode=%d, want NXDOMAIN", rcode)
	}
}

func TestFriendlyNameNoResolverIsNXDOMAIN(t *testing.T) {
	s := NewDNSServer(zaptest.NewLogger(t), "")
	rcode, _ := answerA(t, s, "brave-dolphin.acme.cloud.wendy.dev.")
	if rcode != dns.RcodeNameError {
		t.Fatalf("no resolver: got rcode=%d, want NXDOMAIN", rcode)
	}
}

func TestNumericNamePathUnchanged(t *testing.T) {
	s := NewDNSServer(zaptest.NewLogger(t), "")
	s.SetResolver(fakeResolver{slug: "acme"})
	rcode, ip := answerA(t, s, "device-215.cloud.wendy.dev.")
	if rcode != dns.RcodeSuccess || ip != "10.99.0.215" {
		t.Fatalf("numeric: got rcode=%d ip=%q, want SUCCESS 10.99.0.215", rcode, ip)
	}
}
```

If `captureWriter` does not already exist in `dns_test.go`, add this minimal `dns.ResponseWriter` stub:

```go
type captureWriter struct {
	dns.ResponseWriter
	msg *dns.Msg
}

func (c *captureWriter) WriteMsg(m *dns.Msg) error { c.msg = m; return nil }
func (c *captureWriter) LocalAddr() net.Addr       { return &net.UDPAddr{} }
func (c *captureWriter) RemoteAddr() net.Addr      { return &net.UDPAddr{} }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/mesh/ -run TestFriendlyName -v`
Expected: FAIL — `s.SetResolver undefined` / compile error.

- [ ] **Step 3: Add the regex and Resolver seam**

In `dns.go`, below `meshNameRE` (`:16-17`) add:

```go
// friendlyNameRE matches <devicename>.<org-slug>.cloud.wendy.dev. — the
// human-readable mesh name resolved via the injected Resolver. It never
// overlaps meshNameRE: the numeric form has one label before ".cloud", this
// form has two.
var friendlyNameRE = regexp.MustCompile(`^([a-z0-9-]+)\.([a-z0-9-]+)\.cloud\.wendy\.dev\.$`)

// Resolver maps a normalized device name to a cloud asset ID within this
// device's own org, and reports this device's own org slug. It is implemented
// in the services package (which owns the mDNS + cloud-roster dependencies)
// and injected via SetResolver, keeping the mesh package free of those deps.
type Resolver interface {
	Resolve(name string) (assetID int32, ok bool)
	OrgSlug() string
}
```

Add a `resolver` field to `DNSServer` (after `upstream string` at `:25`):

```go
	resolver Resolver // friendly-name resolver; nil until SetResolver
```

Add the setter (after `NewDNSServer`, `:45`):

```go
// SetResolver installs the friendly-name resolver. Safe to call once during
// agent startup after provisioning identity is available; a nil resolver
// makes every friendly name NXDOMAIN while the numeric path keeps working.
func (s *DNSServer) SetResolver(r Resolver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolver = r
}
```

- [ ] **Step 4: Branch `handle` to the friendly path**

In `handle` (`dns.go:102`), immediately after the numeric-match block (the block ending with the `_ = w.WriteMsg(resp)` for the numeric answer, i.e. after the `meshNameRE` handling returns) and **before** the final `s.forward(w, r)` fallthrough, insert a friendly-name branch. Concretely, change the early dispatch so it reads:

```go
	q := r.Question[0]
	lower := strings.ToLower(q.Name)
	if m := meshNameRE.FindStringSubmatch(lower); m != nil {
		s.answerNumeric(w, r, q, m[1])
		return
	}
	if m := friendlyNameRE.FindStringSubmatch(lower); m != nil {
		s.answerFriendly(w, r, q, m[1], m[2])
		return
	}
	s.forward(w, r)
}
```

Extract the existing numeric answer body (the `strconv.ParseInt` → `VIPForDevice` → build `resp` sequence) verbatim into:

```go
func (s *DNSServer) answerNumeric(w dns.ResponseWriter, r *dns.Msg, q dns.Question, digits string) {
	id, err := strconv.ParseInt(digits, 10, 32)
	if err != nil {
		s.reply(w, r, dns.RcodeNameError)
		return
	}
	s.answerVIP(w, r, q, int32(id))
}
```

Add the friendly resolver + a shared VIP answer helper:

```go
func (s *DNSServer) answerFriendly(w dns.ResponseWriter, r *dns.Msg, q dns.Question, name, orgSlug string) {
	s.mu.Lock()
	resolver := s.resolver
	s.mu.Unlock()
	if resolver == nil || resolver.OrgSlug() == "" || orgSlug != resolver.OrgSlug() {
		s.reply(w, r, dns.RcodeNameError)
		return
	}
	id, ok := resolver.Resolve(name)
	if !ok {
		s.reply(w, r, dns.RcodeNameError)
		return
	}
	s.answerVIP(w, r, q, id)
}

// answerVIP writes the A record for a resolved asset ID, or NXDOMAIN if the ID
// is outside the VIP range. Shared by the numeric and friendly paths so both
// emit identical answers.
func (s *DNSServer) answerVIP(w dns.ResponseWriter, r *dns.Msg, q dns.Question, id int32) {
	vip, err := VIPForDevice(id)
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
	_ = w.WriteMsg(resp)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/mesh/ -v`
Expected: PASS (all new friendly tests + existing numeric/passthrough tests).

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/mesh/dns.go go/internal/agent/mesh/dns_test.go
git commit -m "feat(mesh): friendly-name DNS grammar + Resolver seam"
```

---

### Task 3: Carry `name` + `orgid` from mDNS TXT onto LANDevice

**Files:**
- Modify: `go/internal/shared/models/devices.go` (`LANDevice` struct, `:52`)
- Modify: `go/internal/shared/discovery/discovery_linux.go` (`setAssetID` sibling, `:152`; call sites `:120-158`, `:281`)
- Test: `go/internal/shared/discovery/discovery_linux_test.go` (create if absent)

**Interfaces:**
- Consumes: `parseAvahiTXT` / `parseMDNSInfoFields` producing `map[string]string` (`discovery_linux.go:180-192`).
- Produces: `LANDevice.OrgID int32` (from TXT `orgid`) and `LANDevice.MeshName string` (from TXT `name`), populated wherever `setAssetID` is already called.

- [ ] **Step 1: Write the failing test**

```go
package discovery

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestSetMeshFields(t *testing.T) {
	dev := &models.LANDevice{}
	setMeshFields(dev, map[string]string{"name": "brave-dolphin", "orgid": "42", "assetid": "215"})
	if dev.MeshName != "brave-dolphin" {
		t.Errorf("MeshName = %q, want brave-dolphin", dev.MeshName)
	}
	if dev.OrgID != 42 {
		t.Errorf("OrgID = %d, want 42", dev.OrgID)
	}
}

func TestSetMeshFieldsAbsent(t *testing.T) {
	dev := &models.LANDevice{}
	setMeshFields(dev, map[string]string{})
	if dev.MeshName != "" || dev.OrgID != 0 {
		t.Errorf("expected zero values, got MeshName=%q OrgID=%d", dev.MeshName, dev.OrgID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/shared/discovery/ -run TestSetMeshFields -v`
Expected: FAIL — `undefined: setMeshFields` and `dev.MeshName`/`dev.OrgID` undefined.

- [ ] **Step 3: Add the fields and parser**

In `models/devices.go`, add to `LANDevice` (after `AssetID`, `:60`):

```go
	// MeshName is the device's friendly mesh name from the `name` TXT record
	// (e.g. "brave-dolphin"); empty when unadvertised or pre-mesh.
	MeshName string `json:"meshName,omitempty"`
	// OrgID is the device's cloud organization id from the `orgid` TXT record;
	// 0 when unprovisioned or pre-mesh.
	OrgID int32 `json:"orgId,omitempty"`
```

In `discovery_linux.go`, add next to `setAssetID` (`:152`):

```go
// setMeshFields copies the friendly mesh name and org id out of the TXT
// records used by the mesh friendly-name resolver.
func setMeshFields(dev *models.LANDevice, txtRecords map[string]string) {
	if v, ok := txtRecords["name"]; ok {
		dev.MeshName = v
	}
	if v, ok := txtRecords["orgid"]; ok {
		if id, err := strconv.ParseInt(v, 10, 32); err == nil && id > 0 {
			dev.OrgID = int32(id)
		}
	}
}
```

Call `setMeshFields(dev, txtRecords)` immediately after each existing `setAssetID(dev, txtRecords)` call (there are call sites around `discovery_linux.go:140` and `:281`; match them so both the avahi-browse and mdns-fallback paths populate the fields).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/shared/discovery/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/models/devices.go go/internal/shared/discovery/discovery_linux.go go/internal/shared/discovery/discovery_linux_test.go
git commit -m "feat(discovery): parse mesh name + orgid TXT records onto LANDevice"
```

---

### Task 4: Advertise numeric `orgid` in the Avahi TXT at provisioning

**Files:**
- Modify: `go/internal/agent/configpartition/apply.go` (`UpdateAvahiForProvisioning` `:357`; `updateAvahiService` `:379`; `updateWendyOSServicePort` `:424`; add `updateOrgIDTXTRecord` mirroring `updateAssetIDTXTRecord` `:471`)
- Modify: `go/cmd/wendy-agent/main.go:604` (pass orgID)
- Test: `go/internal/agent/configpartition/apply_test.go`

**Interfaces:**
- Consumes: existing `updateAssetIDTXTRecord(block string, assetID int32) string` pattern (`apply.go:471`).
- Produces: `UpdateAvahiForProvisioning(logger *zap.Logger, mtlsPort int, assetID, orgID int32)` — **signature gains `orgID int32`**; writes `<txt-record>orgid=<N></txt-record>` alongside `assetid`.

- [ ] **Step 1: Write the failing test**

```go
// add to apply_test.go
func TestUpdateOrgIDTXTRecord(t *testing.T) {
	block := "  <service>\n    <type>_wendyos._udp</type>\n  </service>"
	got := updateOrgIDTXTRecord(block, 42)
	if !strings.Contains(got, "<txt-record>orgid=42</txt-record>") {
		t.Fatalf("orgid record not inserted: %q", got)
	}
	// Idempotent replace, not duplicate.
	got2 := updateOrgIDTXTRecord(got, 7)
	if strings.Count(got2, "<txt-record>orgid=") != 1 || !strings.Contains(got2, "orgid=7") {
		t.Fatalf("orgid record not replaced idempotently: %q", got2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/configpartition/ -run TestUpdateOrgIDTXTRecord -v`
Expected: FAIL — `undefined: updateOrgIDTXTRecord`.

- [ ] **Step 3: Implement `updateOrgIDTXTRecord` and thread `orgID`**

Add to `apply.go` (mirroring `updateAssetIDTXTRecord` at `:471`):

```go
// updateOrgIDTXTRecord inserts or replaces the <txt-record>orgid=…</txt-record>
// entry inside a wendyos avahi <service> block. The mesh friendly-name resolver
// reads this to filter LAN peers to the dialer's own org. orgID <= 0 removes it.
func updateOrgIDTXTRecord(block string, orgID int32) string {
	orgIDRe := regexp.MustCompile(`\s*<txt-record>orgid=[^<]*</txt-record>`)
	if orgID <= 0 {
		return orgIDRe.ReplaceAllString(block, "")
	}
	value := strconv.Itoa(int(orgID))
	if strings.Contains(block, "<txt-record>orgid=") {
		return orgIDRe.ReplaceAllString(block, "\n    <txt-record>orgid="+value+"</txt-record>")
	}
	return strings.Replace(block, "</service>",
		"    <txt-record>orgid="+value+"</txt-record>\n  </service>", 1)
}
```

Thread `orgID` through the call chain: add an `orgID int32` parameter to `updateWendyOSServicePort` (`:424`) and to `updateAvahiService` (`:379`), call `block = updateOrgIDTXTRecord(block, orgID)` right after the existing `updateAssetIDTXTRecord` call inside `updateWendyOSServicePort`, and add `orgID int32` to `UpdateAvahiForProvisioning` (`:357`), passing it down. In `UpdateAvahiForUnprovisioning` pass `orgID = 0` so the record is removed on unprovision.

Update the call site `go/cmd/wendy-agent/main.go:604`:

```go
	// orgID is available from provisioningSvc.ProvisioningInfo() at main.go:583.
	configpartition.UpdateAvahiForProvisioning(logger, mtlsPortNum, assetID, orgID)
```

- [ ] **Step 4: Run tests + build**

Run: `cd go && go test ./internal/agent/configpartition/ -v && go build ./...`
Expected: PASS and clean build (confirms every `UpdateAvahiForProvisioning` caller was updated).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/configpartition/apply.go go/internal/agent/configpartition/apply_test.go go/cmd/wendy-agent/main.go
git commit -m "feat(agent): advertise numeric orgid in avahi TXT at provisioning"
```

---

### Task 5: `GetMeshRoster` proto + Go stubs

**Files:**
- Create: `Proto/cloud/mesh.proto`
- Modify: `go/scripts/generate-proto.sh` (append `mesh.proto` to the `CLOUD_PROTOS` array, ~`:85-95`)
- Generated (do not hand-edit): `go/proto/gen/cloudpb/mesh*.pb.go`

**Interfaces:**
- Produces (Go, package `cloudpb`): `MeshRosterServiceClient` with `GetMeshRoster(ctx, *GetMeshRosterRequest, ...) (*GetMeshRosterResponse, error)`; `GetMeshRosterResponse{ OrgSlug string; Entries []*MeshRosterEntry }`; `MeshRosterEntry{ Name string; AssetId int32 }`.

- [ ] **Step 1: Write the proto**

`Proto/cloud/mesh.proto`:

```proto
syntax = "proto3";

package wendycloud.v1;

// MeshRosterService serves the minimal name directory the WendyOS mesh needs
// to resolve <devicename>.<org-slug>.cloud.wendy.dev to a cloud asset id. It is
// deliberately narrower than AssetService: only {name, asset_id} pairs plus the
// caller's own org slug, scoped by the caller's asset-certificate identity.
service MeshRosterService {
  rpc GetMeshRoster(GetMeshRosterRequest) returns (GetMeshRosterResponse);
}

message GetMeshRosterRequest {}

message MeshRosterEntry {
  string name = 1;      // device asset name, server-normalized to a DNS label
  int32 asset_id = 2;
}

message GetMeshRosterResponse {
  string org_slug = 1;  // caller org's slug, server-normalized to a DNS label
  repeated MeshRosterEntry entries = 2;
}
```

- [ ] **Step 2: Register the proto for codegen**

In `go/scripts/generate-proto.sh`, add `"cloud/mesh.proto"` to the `CLOUD_PROTOS` array (the block near `:85-95`). No `option go_package` is needed — the script supplies the `M…` mapping to `$CLOUD_PKG`.

- [ ] **Step 3: Regenerate stubs**

Run: `cd go && ./scripts/generate-proto.sh`
Expected: `go/proto/gen/cloudpb/mesh.pb.go` and `mesh_grpc.pb.go` created. Requires `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` on PATH.

- [ ] **Step 4: Verify it compiles**

Run: `cd go && go build ./proto/...`
Expected: clean build.

- [ ] **Step 5: Commit**

```bash
git add Proto/cloud/mesh.proto go/scripts/generate-proto.sh go/proto/gen/cloudpb/
git commit -m "feat(proto): GetMeshRoster RPC for mesh friendly-name resolution"
```

---

### Task 6: Roster cache + `GetMeshRoster` client

**Files:**
- Create: `go/internal/agent/services/mesh_roster.go`
- Test: `go/internal/agent/services/mesh_roster_test.go`

**Interfaces:**
- Consumes: `mesh.Normalize` (Task 1); `cloudpb.MeshRosterServiceClient`, `GetMeshRosterResponse` (Task 5); `brokerDialOpts` (`tunnel_broker_client.go:154`).
- Produces:
  - `type MeshRoster struct { … }`
  - `func NewMeshRoster(logger *zap.Logger, cloudURL string, orgID, assetID int32, chainPEM string) *MeshRoster`
  - `func (r *MeshRoster) Lookup(name string) (assetID int32, ok bool)` — normalized-name lookup, `ok=false` for unknown or ambiguous names.
  - `func (r *MeshRoster) OrgSlug() string`
  - `func (r *MeshRoster) Sync(ctx context.Context) error` — one refresh from cloud.
  - `func (r *MeshRoster) applyResponse(resp *cloudpb.GetMeshRosterResponse)` — pure cache builder (unit-tested without gRPC), building the normalized map and the ambiguous-name set.

- [ ] **Step 1: Write the failing test**

```go
package services

import (
	"testing"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"go.uber.org/zap/zaptest"
)

func newTestRoster(t *testing.T) *MeshRoster {
	return NewMeshRoster(zaptest.NewLogger(t), "cloud.example:443", 42, 215, "")
}

func TestRosterLookupNormalizes(t *testing.T) {
	r := newTestRoster(t)
	r.applyResponse(&cloudpb.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpb.MeshRosterEntry{
			{Name: "Brave Dolphin", AssetId: 215},
			{Name: "calm-otter", AssetId: 216},
		},
	})
	if r.OrgSlug() != "acme" {
		t.Fatalf("OrgSlug = %q, want acme", r.OrgSlug())
	}
	if id, ok := r.Lookup("brave-dolphin"); !ok || id != 215 {
		t.Fatalf("Lookup(brave-dolphin) = %d,%v want 215,true", id, ok)
	}
	if id, ok := r.Lookup("calm-otter"); !ok || id != 216 {
		t.Fatalf("Lookup(calm-otter) = %d,%v want 216,true", id, ok)
	}
	if _, ok := r.Lookup("ghost"); ok {
		t.Fatal("Lookup(ghost) should be false")
	}
}

func TestRosterDuplicateNameIsAmbiguous(t *testing.T) {
	r := newTestRoster(t)
	r.applyResponse(&cloudpb.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpb.MeshRosterEntry{
			{Name: "Brave Dolphin", AssetId: 215},
			{Name: "brave dolphin", AssetId: 999}, // normalizes to the same label
		},
	})
	if _, ok := r.Lookup("brave-dolphin"); ok {
		t.Fatal("duplicate normalized name must resolve to ok=false")
	}
}

func TestRosterSlugNormalized(t *testing.T) {
	r := newTestRoster(t)
	r.applyResponse(&cloudpb.GetMeshRosterResponse{OrgSlug: "ACME Corp."})
	if r.OrgSlug() != "acme-corp" {
		t.Fatalf("OrgSlug = %q, want acme-corp", r.OrgSlug())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/services/ -run TestRoster -v`
Expected: FAIL — `undefined: NewMeshRoster`.

- [ ] **Step 3: Implement the cache + client**

```go
package services

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/wendylabsinc/wendy/go/internal/agent/mesh"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// MeshRoster caches this org's {normalized name -> asset id} directory and the
// org's own slug, refreshed from the cloud GetMeshRoster RPC. It is the
// off-LAN half of the hybrid friendly-name resolver.
type MeshRoster struct {
	logger   *zap.Logger
	cloudURL string
	orgID    int32
	assetID  int32
	chainPEM string

	mu        sync.RWMutex
	slug      string
	byName    map[string]int32
	ambiguous map[string]struct{}
}

func NewMeshRoster(logger *zap.Logger, cloudURL string, orgID, assetID int32, chainPEM string) *MeshRoster {
	return &MeshRoster{
		logger:    logger,
		cloudURL:  cloudURL,
		orgID:     orgID,
		assetID:   assetID,
		chainPEM:  chainPEM,
		byName:    make(map[string]int32),
		ambiguous: make(map[string]struct{}),
	}
}

func (r *MeshRoster) OrgSlug() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.slug
}

// Lookup returns the asset id for a normalized device name. Unknown and
// ambiguous (duplicate-normalized) names return ok=false.
func (r *MeshRoster) Lookup(name string) (int32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, dup := r.ambiguous[name]; dup {
		return 0, false
	}
	id, ok := r.byName[name]
	return id, ok
}

// applyResponse rebuilds the cache from a roster response. Names that collide
// after normalization are recorded as ambiguous and never resolve.
func (r *MeshRoster) applyResponse(resp *cloudpb.GetMeshRosterResponse) {
	byName := make(map[string]int32, len(resp.GetEntries()))
	ambiguous := make(map[string]struct{})
	for _, e := range resp.GetEntries() {
		n := mesh.Normalize(e.GetName())
		if n == "" {
			continue
		}
		if existing, ok := byName[n]; ok && existing != e.GetAssetId() {
			ambiguous[n] = struct{}{}
			continue
		}
		byName[n] = e.GetAssetId()
	}
	r.mu.Lock()
	r.slug = mesh.Normalize(resp.GetOrgSlug())
	r.byName = byName
	r.ambiguous = ambiguous
	r.mu.Unlock()
}

// Sync performs one GetMeshRoster refresh over the cloud gRPC endpoint using
// the same asset-cert identity the tunnel broker client uses.
func (r *MeshRoster) Sync(ctx context.Context) error {
	dialOpts, md, err := brokerDialOpts(r.logger, r.orgID, r.assetID, r.chainPEM)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(r.cloudURL, dialOpts...)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := cloudpb.NewMeshRosterServiceClient(conn)
	callCtx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, md), 10*time.Second)
	defer cancel()
	resp, err := client.GetMeshRoster(callCtx, &cloudpb.GetMeshRosterRequest{})
	if err != nil {
		return err
	}
	r.applyResponse(resp)
	return nil
}

// RunSync refreshes immediately, then every interval, until ctx is done.
// Failures are logged and retried on the next tick; the cache keeps its last
// good contents so a transient cloud outage never breaks LAN-cached names.
func (r *MeshRoster) RunSync(ctx context.Context, interval time.Duration) {
	if err := r.Sync(ctx); err != nil {
		r.logger.Warn("mesh roster initial sync failed", zap.Error(err))
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Sync(ctx); err != nil {
				r.logger.Warn("mesh roster sync failed", zap.Error(err))
			}
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run TestRoster -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/mesh_roster.go go/internal/agent/services/mesh_roster_test.go
git commit -m "feat(agent): mesh roster cache + GetMeshRoster client"
```

---

### Task 7: Hybrid resolver (mDNS-first, roster fallback)

**Files:**
- Create: `go/internal/agent/services/mesh_resolver.go`
- Test: `go/internal/agent/services/mesh_resolver_test.go`

**Interfaces:**
- Consumes: `mesh.Normalize` (Task 1); `discovery.Discover` + `models.LANDevice.{MeshName,OrgID,AssetID}` (Task 3); `MeshRoster.{Lookup,OrgSlug}` (Task 6).
- Produces: `func NewMeshResolver(logger *zap.Logger, ownOrgID int32, roster *MeshRoster, browse func(context.Context) ([]models.LANDevice, error)) *MeshResolver` implementing `mesh.Resolver` (`Resolve(name) (int32,bool)`, `OrgSlug() string`). `browse` is injected so tests avoid real mDNS; production passes a closure over `discovery.Discover`.

- [ ] **Step 1: Write the failing test**

```go
package services

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"go.uber.org/zap/zaptest"
)

func TestResolverMDNSHitShortCircuits(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "")
	roster.applyResponse(&cloudpb.GetMeshRosterResponse{OrgSlug: "acme"}) // slug only
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if id, ok := r.Resolve("brave-dolphin"); !ok || id != 215 {
		t.Fatalf("mDNS hit = %d,%v want 215,true", id, ok)
	}
}

func TestResolverIgnitesForeignOrgOnLAN(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "")
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 99, AssetID: 700, IsMTLS: true}, // other org
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if _, ok := r.Resolve("brave-dolphin"); ok {
		t.Fatal("must not resolve a same-named device from another org on the LAN")
	}
}

func TestResolverFallsBackToRoster(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "")
	roster.applyResponse(&cloudpb.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpb.MeshRosterEntry{{Name: "calm-otter", AssetId: 216}},
	})
	browse := func(context.Context) ([]models.LANDevice, error) { return nil, nil } // LAN empty
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if id, ok := r.Resolve("calm-otter"); !ok || id != 216 {
		t.Fatalf("roster fallback = %d,%v want 216,true", id, ok)
	}
	if r.OrgSlug() != "acme" {
		t.Fatalf("OrgSlug = %q want acme", r.OrgSlug())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/services/ -run TestResolver -v`
Expected: FAIL — `undefined: NewMeshResolver`.

- [ ] **Step 3: Implement the resolver**

```go
package services

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/mesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// MeshResolver implements mesh.Resolver with the hybrid strategy: an mDNS
// browse (filtered to our own org id) resolves LAN peers cloud-free; anything
// not found on the LAN falls back to the cloud-synced roster cache. OrgSlug is
// always the roster's (cloud-learned) value.
type MeshResolver struct {
	logger   *zap.Logger
	ownOrgID int32
	roster   *MeshRoster
	browse   func(context.Context) ([]models.LANDevice, error)
}

func NewMeshResolver(logger *zap.Logger, ownOrgID int32, roster *MeshRoster, browse func(context.Context) ([]models.LANDevice, error)) *MeshResolver {
	return &MeshResolver{logger: logger, ownOrgID: ownOrgID, roster: roster, browse: browse}
}

func (r *MeshResolver) OrgSlug() string { return r.roster.OrgSlug() }

// Resolve returns the asset id for a normalized device name, mDNS first.
func (r *MeshResolver) Resolve(name string) (int32, bool) {
	if id, ok := r.resolveLAN(name); ok {
		return id, true
	}
	return r.roster.Lookup(name)
}

func (r *MeshResolver) resolveLAN(name string) (int32, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	devices, err := r.browse(ctx)
	if err != nil {
		return 0, false
	}
	var found int32
	matches := 0
	for _, d := range devices {
		if d.OrgID != r.ownOrgID || d.AssetID == 0 {
			continue
		}
		if mesh.Normalize(d.MeshName) != name {
			continue
		}
		if matches == 0 {
			found = d.AssetID
		} else if d.AssetID != found {
			// Two different LAN devices share the name -> ambiguous.
			return 0, false
		}
		matches++
	}
	if matches == 0 {
		return 0, false
	}
	return found, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run TestResolver -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/mesh_resolver.go go/internal/agent/services/mesh_resolver_test.go
git commit -m "feat(agent): hybrid mDNS+roster mesh friendly-name resolver"
```

---

### Task 8: Wire the resolver + roster sync into agent startup

**Files:**
- Modify: `go/cmd/wendy-agent/main.go` (mesh DNS construction `:171`; mesh dialer/identity block `:583-606`)

**Interfaces:**
- Consumes: `mesh.DNSServer.SetResolver` (Task 2); `NewMeshRoster`/`RunSync` (Task 6); `NewMeshResolver` (Task 7); `discovery.Discover` (`discovery.go:50`); `provisioningSvc.ProvisioningInfo()` → `(cloudHost string, orgID, assetID int32, _)`; `provisioningSvc.ProvisioningCerts()` → `(certPEM, chainPEM string, keyData []byte)`.

- [ ] **Step 1: Add a cloud-gRPC URL helper**

Next to `brokerURLForCloudHost` in `main.go`, add (mirror its host/port derivation; the cloud API gRPC endpoint is the same host `GetMeshRoster` is served on):

```go
// cloudGRPCURLForCloudHost returns the cloud API gRPC target for the mesh
// roster RPC, derived from the provisioning cloud host the same way the broker
// URL is. If WENDY_CLOUD_URL is set it wins (dev/self-host override).
func cloudGRPCURLForCloudHost(cloudHost string) string {
	if v := os.Getenv("WENDY_CLOUD_URL"); v != "" {
		return v
	}
	return brokerURLForCloudHost(cloudHost) // same host:port; MeshRosterService is served there
}
```

- [ ] **Step 2: Build the roster + resolver and inject them**

In the mesh block after `meshDialer` is constructed (`main.go:594`), and given `meshDNS` from `:172`, add:

```go
	// Friendly-name resolution: <devicename>.<org-slug>.cloud.wendy.dev.
	meshRoster := services.NewMeshRoster(logger, cloudGRPCURLForCloudHost(cloudHost), orgID, assetID, chainPEM)
	meshBrowse := func(ctx context.Context) ([]models.LANDevice, error) {
		col, err := discovery.Discover(ctx, discovery.DiscoveryOptions{})
		if err != nil {
			return nil, err
		}
		return col.LANDevices, nil
	}
	meshResolver := services.NewMeshResolver(logger, orgID, meshRoster, meshBrowse)
	meshDNS.SetResolver(meshResolver)
	wg.Add(1)
	go func() {
		defer wg.Done()
		meshRoster.RunSync(ctx, 5*time.Minute)
	}()
```

Add imports if absent: `"github.com/wendylabsinc/wendy/go/internal/shared/discovery"` and `"github.com/wendylabsinc/wendy/go/internal/shared/models"`. If the agent supports live re-provisioning via the `OnProvisioned` callback (as `meshDialer.UpdateIdentity` does at `mesh_dialer.go:122`), also rebuild/refresh `meshRoster` there so a device enrolled while running learns its slug without a restart; if that callback is not readily reachable for the roster, note it as a known limitation (first roster sync happens on next agent start).

- [ ] **Step 3: Build**

Run: `cd go && go build ./...`
Expected: clean build.

- [ ] **Step 4: Run the mesh + services test suites**

Run: `cd go && go test ./internal/agent/mesh/... ./internal/agent/services/... ./internal/shared/discovery/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/cmd/wendy-agent/main.go
git commit -m "feat(agent): wire hybrid mesh friendly-name resolver + roster sync"
```

---

### Task 9: Cloud `GetMeshRoster` handler (separate repo — `~/git/wendy/cloud`)

> This task lands in the Swift cloud repo, not WendyOS. It is the authoritative source for off-LAN name resolution and the org slug. Do the WendyOS agent tasks first (the mDNS path works without it); this unlocks the roster fallback. Because it is a different codebase, **read the existing patterns before writing** rather than following invented code.

**Contract (must match Task 5 proto byte-for-byte):**
- `MeshRosterService.GetMeshRoster(GetMeshRosterRequest{}) -> GetMeshRosterResponse{ org_slug, entries[]{name, asset_id} }`.
- Caller identity comes **only** from the asset certificate (`urn:wendy:org:<id>:asset:<id>`); the org is taken from the cert, never from a request field.
- Returns only compute devices in the caller's org (`is_compute_device = true`), each as `{name = normalize(asset.name), asset_id}`; `org_slug = normalize(organization.name)`.
- Reject non-asset (user) certs with `PermissionDenied`. Reject when `orgs.mesh_enabled = false` with `PermissionDenied` (same flag gating the rest of the mesh, per the base design).
- `normalize` MUST match `mesh.Normalize` exactly (Task 1): lowercase, non-`[a-z0-9]` runs → single `-`, trim `-`.

- [ ] **Step 1: Read the existing cloud patterns**

Read, in `~/git/wendy/cloud`: the `AssetService` handler (list-assets-by-org query + asset-cert authz), the `TunnelBrokerService.ClientTunnel` handler (asset-cert identity extraction + `orgs.mesh_enabled` check added by the base mesh design), the `service-protos` submodule layout, the `PostgresModels` `.query.sql` convention, and `BrokerFixture` test setup. (See the `reference_cloud_repo_layout` memory.)

- [ ] **Step 2: Add the proto to the cloud `service-protos` submodule** matching `Proto/cloud/mesh.proto` from Task 5, and regenerate Swift stubs per that repo's codegen.

- [ ] **Step 3: Implement `GetMeshRoster`** following the AssetService authz + query patterns: extract org from the asset cert, gate on `mesh_enabled`, `SELECT name, id FROM assets WHERE organization_id = $1 AND is_compute_device = true`, normalize names + the org name, return them.

- [ ] **Step 4: Tests (BrokerFixture):** asset cert on own org → entries + slug; user cert → `PermissionDenied`; `mesh_enabled = false` → `PermissionDenied`; response contains no fields beyond `{name, asset_id}` + slug; a Swift `normalize` unit test whose table matches Task 1's `TestNormalize` cases exactly.

- [ ] **Step 5: Commit** in the cloud repo with a message referencing this plan.

---

### Task 10: E2E + HelloMesh switch to the friendly name

**Files:**
- Modify: HelloMesh sample `app.py` `MESH_TARGET` default and its README (paths per `specs/2026-07-02-mesh-data-plane-plan.md:2341-2343`).

**Interfaces:** consumes the fully wired agent (Tasks 1-8) and the cloud RPC (Task 9).

- [ ] **Step 1: Point HelloMesh at the friendly name**

Change the `MESH_TARGET` default to `<devicename>.<org-slug>.cloud.wendy.dev:8080` (keep the env override), and update the README to document finding a device's name + org slug (`wendy cloud discover` / dashboard) and that `device-<assetID>.cloud.wendy.dev:8080` remains the numeric fallback.

- [ ] **Step 2: E2E on two devices, shared LAN (mDNS path)**

Deploy HelloHTTP on device B and HelloMesh on device A with `MESH_TARGET=<B-name>.<org-slug>.cloud.wendy.dev:8080`. Expect `OK 200`. Verify in the agent log on A that resolution took the mDNS path (LAN hit).

- [ ] **Step 3: E2E with LAN blocked (roster + broker path)**

Block mDNS/LAN reachability between A and B; repeat. Expect `OK 200` via the cloud roster + broker relay. Confirm the agent log shows the roster lookup and broker fallback.

- [ ] **Step 4: Regression — numeric name still works**

With the same setup, set `MESH_TARGET=device-<Bid>.cloud.wendy.dev:8080`. Expect `OK 200`, confirming the numeric path is unchanged.

- [ ] **Step 5: Commit**

```bash
git add <helloMesh app.py + README paths>
git commit -m "docs(mesh): HelloMesh uses <devicename>.<org-slug> friendly name"
```

---

## Self-Review

**Spec coverage:**
- Grammar (both forms, NXDOMAIN rules) → Task 2. ✅
- Hybrid resolution (mDNS-first, roster fallback) → Tasks 3 (mDNS fields), 6 (roster), 7 (hybrid), 8 (wiring). ✅
- Org slug (own-org enforcement, derived by normalization) → Tasks 1 (normalize), 2 (enforcement), 6 (slug from roster), 9 (server-side derivation). ✅
- mDNS advertises numeric `orgid` → Task 4. ✅
- Cloud `GetMeshRoster` (narrow, asset-cert scoped, `mesh_enabled`-gated) → Tasks 5 (proto), 9 (handler). ✅
- Edge cases: name normalization (Task 1), duplicate → NXDOMAIN (Tasks 6, 7 ambiguity + Task 2 NXDOMAIN), cold cache/off-LAN (Task 7 fallthrough → connection refused), own-slug-not-yet-learned (Task 2 `OrgSlug()==""` → NXDOMAIN). ✅
- Backward compat: numeric path unchanged (Task 2 regression test + Task 10 Step 4). ✅
- Testing (unit agent, unit cloud, E2E both paths) → Tasks 1-8 unit, 9 cloud, 10 E2E. ✅

**Placeholder scan:** No TBD/TODO; every code step carries complete code; the one cross-repo task (9) is explicitly read-first with a byte-exact contract rather than fabricated Swift.

**Type consistency:** `Resolver{Resolve(string)(int32,bool); OrgSlug()string}` is defined in Task 2 and implemented in Task 7; `MeshRoster.{Lookup,OrgSlug,applyResponse,Sync,RunSync}` used consistently across Tasks 6-8; `LANDevice.{MeshName,OrgID}` defined in Task 3 and consumed in Task 7; `GetMeshRosterResponse{OrgSlug,Entries[]{Name,AssetId}}` consistent across Tasks 5, 6, 9; `Normalize` signature consistent across Tasks 1, 6, 7. `UpdateAvahiForProvisioning` gains `orgID int32` in Task 4 with the caller updated in the same task.

## Known cross-repo dependency

Off-LAN friendly-name resolution and the org slug depend on Task 9 shipping in the cloud repo and the deployment serving `MeshRosterService` at the cloud gRPC host derived in Task 8. Until then, friendly names resolve on the LAN (mDNS) only, and `device-<N>` works everywhere.
