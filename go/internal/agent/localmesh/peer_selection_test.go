package localmesh

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestPeerSelectionLANAndRadioDiversity(t *testing.T) {
	now := time.Now()
	snapshot := NodeSnapshot{}
	p := NewPeerSelection(func() NodeSnapshot { return snapshot })
	p.now = func() time.Time { return now }
	p.Seen(selectionDevice(10), RadioNAN)
	if !p.AllowRadio(selectionDevice(10), RadioNAN) || !p.AllowRadio(selectionDevice(10), RadioBLE) {
		t.Fatal("an available radio peer was rejected")
	}
	snapshot.Links = []PeerLink{{Device: selectionDevice(10), Cost: 512}}
	if !p.AllowRadio(selectionDevice(10), RadioBLE) {
		t.Fatal("a second radio should provide failover when there is no other candidate")
	}
	p.Seen(selectionDevice(11), RadioBLE)
	if p.AllowRadio(selectionDevice(10), RadioBLE) || !p.AllowRadio(selectionDevice(11), RadioBLE) {
		t.Fatal("a different peer should fill the open slot before duplicating asset 10")
	}
	p.Failed(selectionDevice(11), RadioBLE)
	p.Seen(selectionDevice(11), RadioBLE)
	if !p.AllowRadio(selectionDevice(10), RadioBLE) {
		t.Fatal("continuing advertisements from a failed candidate suppressed fallback")
	}
	now = now.Add(31 * time.Second)
	if p.AllowRadio(selectionDevice(10), RadioBLE) {
		t.Fatal("candidate was not reconsidered after failure cooldown")
	}
	p.Connected(selectionDevice(11), RadioBLE)
	if p.AllowRadio(selectionDevice(10), RadioBLE) {
		t.Fatal("healthy candidate did not reclaim the diverse slot")
	}
	now = now.Add(61 * time.Second)
	if !p.AllowRadio(selectionDevice(10), RadioBLE) {
		t.Fatal("expired candidate hints should not suppress fallback")
	}
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(10), Cost: 128})
	if p.AllowRadio(selectionDevice(10), RadioNAN) || p.AllowRadio(selectionDevice(10), RadioBLE) || !p.HasLAN(selectionDevice(10)) {
		t.Fatal("authenticated LAN must displace both radios")
	}
}

func TestPeerSelectionConfiguredTCPAndDirectLAN(t *testing.T) {
	snapshot := NodeSnapshot{Links: []PeerLink{{Device: selectionDevice(10), Cost: 256}}}
	p := NewPeerSelection(func() NodeSnapshot { return snapshot })
	if p.HasLAN(selectionDevice(10)) || !p.AllowRadio(selectionDevice(10), RadioNAN) || !p.AllowRadio(selectionDevice(10), RadioBLE) {
		t.Fatal("configured TCP must coexist with NAN and BLE")
	}
	if p.AllowLAN(selectionDevice(10), 256) {
		t.Fatal("configured TCP must not be counted as discovered LAN")
	}
	for _, cost := range []uint16{64, 128} {
		snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(10), Cost: cost})
		if !p.HasLAN(selectionDevice(10)) || p.AllowRadio(selectionDevice(10), RadioNAN) || p.AllowRadio(selectionDevice(10), RadioBLE) {
			t.Fatalf("direct LAN cost %d must suppress radios", cost)
		}
		snapshot.Links = snapshot.Links[:1]
	}
}

func TestPeerSelectionCapsOnlyDistinctRadioPeers(t *testing.T) {
	snapshot := NodeSnapshot{Links: []PeerLink{
		{Device: selectionDevice(10), Cost: 512}, {Device: selectionDevice(11), Cost: 4096}, {Device: selectionDevice(12), Cost: 512},
		{Device: selectionDevice(1), Cost: 64}, {Device: selectionDevice(2), Cost: 64},
		{Device: selectionDevice(3), Cost: 128}, {Device: selectionDevice(4), Cost: 256},
	}}
	p := NewPeerSelection(func() NodeSnapshot { return snapshot })
	if p.AllowRadio(selectionDevice(13), RadioBLE) || p.AllowRadio(selectionDevice(4), RadioNAN) {
		t.Fatal("radio selection exceeded the three-distinct-peer target")
	}
	if !p.AllowRadio(selectionDevice(10), RadioNAN) {
		t.Fatal("an existing sole radio link must be retained")
	}
	// If simultaneous handshakes temporarily add a fourth asset, the
	// periodic radio watchers shed the excess radio peer independently of LAN.
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(13), Cost: 4096})
	if p.AllowRadio(selectionDevice(13), RadioBLE) || !p.AllowRadio(selectionDevice(10), RadioNAN) {
		t.Fatal("four-peer overshoot did not converge toward three")
	}
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(12), Cost: 64})
	if p.AllowRadio(selectionDevice(12), RadioNAN) || !p.AllowRadio(selectionDevice(13), RadioBLE) {
		t.Fatal("a direct LAN link must replace only its own radio and free that radio slot")
	}
}

func TestPeerSelectionLANReplacesRadioAndSlowerLAN(t *testing.T) {
	snapshot := NodeSnapshot{Links: []PeerLink{
		{Device: selectionDevice(10), Cost: 4096}, {Device: selectionDevice(11), Cost: 512}, {Device: selectionDevice(12), Cost: 4096},
	}}
	p := NewPeerSelection(func() NodeSnapshot { return snapshot })
	if !p.AllowLAN(selectionDevice(13), 64) {
		t.Fatal("three radio peers must not block a direct LAN neighbor")
	}
	snapshot.Links = []PeerLink{
		{Device: selectionDevice(10), Cost: 128}, {Device: selectionDevice(11), Cost: 128}, {Device: selectionDevice(12), Cost: 128},
	}
	if !p.AllowLAN(selectionDevice(13), 64) {
		t.Fatal("existing Wi-Fi neighbors must not block a new wired neighbor")
	}
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(13), Cost: 64})
	if !p.AllowLAN(selectionDevice(12), 128) || !p.AllowLAN(selectionDevice(13), 64) {
		t.Fatal("wired links must not evict a different Wi-Fi-only neighbor")
	}
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(10), Cost: 64})
	if p.AllowLAN(selectionDevice(10), 128) || !p.AllowLAN(selectionDevice(10), 64) {
		t.Fatal("wired links must replace a slower link to the same principal")
	}
	if p.AllowLAN(selectionDevice(10), 256) {
		t.Fatal("higher-cost LAN duplicate should be rejected")
	}
}

// Exercise admission at both endpoints and periodic pruning, including a
// highest-UUID late join. The old local top-three rule leaves that node isolated
// even though every pair has a working Ethernet path.
func TestLANFleetRemainsConnectedThroughJoinsAndPruning(t *testing.T) {
	for _, size := range []int{5, 8, 16} {
		for seed := int64(0); seed < 4; seed++ {
			t.Run(fmt.Sprintf("nodes=%d/order=%d", size, seed), func(t *testing.T) {
				snapshots := make([]NodeSnapshot, size)
				policies := make([]*PeerSelection, size)
				for i := range policies {
					policies[i] = NewPeerSelection(func() NodeSnapshot { return snapshots[i] })
				}
				edges := make([][2]int, 0)
				for a := 0; a < size; a++ {
					for b := a + 1; b < size; b++ {
						edges = append(edges, [2]int{a, b})
					}
				}
				random := rand.New(rand.NewSource(seed))
				random.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
				connected := make(map[[2]int]bool)
				refresh := func() {
					for i := range snapshots {
						snapshots[i].Links = nil
					}
					for edge := range connected {
						a, b := edge[0], edge[1]
						snapshots[a].Links = append(snapshots[a].Links, PeerLink{Device: selectionDevice(b + 1), Cost: 64})
						snapshots[b].Links = append(snapshots[b].Links, PeerLink{Device: selectionDevice(a + 1), Cost: 64})
					}
				}
				step := func(active int) {
					for _, edge := range edges {
						a, b := edge[0], edge[1]
						allowed := b < active && policies[a].AllowLAN(selectionDevice(b+1), 64) && policies[b].AllowLAN(selectionDevice(a+1), 64)
						if allowed {
							connected[edge] = true
						} else {
							delete(connected, edge)
						}
						refresh()
					}
				}
				for _, active := range []int{size - 1, size, size - 1, size} {
					for tick := 0; tick < 5; tick++ {
						step(active)
					}
					seen := map[int]bool{0: true}
					queue := []int{0}
					for len(queue) > 0 {
						node := queue[0]
						queue = queue[1:]
						for edge := range connected {
							a, b := edge[0], edge[1]
							if b == node {
								a, b = b, a
							}
							if a == node && !seen[b] {
								seen[b] = true
								queue = append(queue, b)
							}
						}
					}
					if len(seen) != active {
						t.Fatalf("only %d/%d devices connected after admission and pruning", len(seen), active)
					}
				}
			})
		}
	}
}

func TestPopulatedLANRetainsRadioBridge(t *testing.T) {
	snapshot := NodeSnapshot{}
	for i := 1; i <= 8; i++ {
		snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(i), Cost: 64})
	}
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(9), Cost: 256})
	p := NewPeerSelection(func() NodeSnapshot { return snapshot })
	bridge := selectionDevice(10)
	for _, medium := range []RadioMedium{RadioNAN, RadioBLE} {
		if !p.AllowRadio(bridge, medium) {
			t.Fatal("LAN or configured TCP peers consumed the radio bridge's slot")
		}
	}
	snapshot.Links = append(snapshot.Links, PeerLink{Device: bridge, Cost: 512})
	for tick := 0; tick < 5; tick++ {
		if !p.AllowRadio(bridge, RadioNAN) {
			t.Fatal("periodic pruning removed the only bridge to another LAN")
		}
	}
}

func TestDuplicateRadiosNeverPruneBothPaths(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("full=%v", full), func(t *testing.T) {
			peer := selectionDevice(1)
			snapshot := NodeSnapshot{Links: []PeerLink{{Device: peer, Cost: 4096}}}
			if full {
				snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(2), Cost: 512}, PeerLink{Device: selectionDevice(3), Cost: 512})
			}
			p := NewPeerSelection(func() NodeSnapshot { return snapshot })
			p.Seen(selectionDevice(4), RadioBLE)
			if !p.AllowRadio(peer, RadioBLE) || !p.AllowRadio(peer, RadioNAN) {
				t.Fatal("retain the sole authenticated BLE path while admitting a preferred NAN replacement")
			}
			snapshot.Links = append(snapshot.Links, PeerLink{Device: peer, Cost: 512})
			if !p.AllowRadio(peer, RadioNAN) || p.AllowRadio(peer, RadioBLE) {
				t.Fatal("both providers must agree to retain NAN when shedding the duplicate BLE link")
			}
		})
	}
}

func TestAllowRadioReasonNamesVetoBranch(t *testing.T) {
	now := time.Now()
	snapshot := NodeSnapshot{}
	p := NewPeerSelection(func() NodeSnapshot { return snapshot })
	p.now = func() time.Time { return now }

	if allow, reason := p.AllowRadioReason(selectionDevice(10), RadioBLE); !allow || reason != "first-radio" {
		t.Fatalf("empty snapshot: allow=%v reason=%q", allow, reason)
	}
	if allow, _ := p.AllowRadioReason(meshid.Device{}, RadioBLE); allow {
		t.Fatal("invalid asset must be vetoed")
	}

	// LAN supersedes: same asset over cheap link.
	snapshot.Links = []PeerLink{{Device: selectionDevice(10), Cost: 64}}
	if allow, reason := p.AllowRadioReason(selectionDevice(10), RadioBLE); allow || reason != "lan-supersedes(cost=64)" {
		t.Fatalf("LAN veto: allow=%v reason=%q", allow, reason)
	}

	// Peer cap: three distinct peers, asset absent.
	snapshot.Links = []PeerLink{{Device: selectionDevice(1), Cost: 512}, {Device: selectionDevice(2), Cost: 512}, {Device: selectionDevice(3), Cost: 512}}
	if allow, reason := p.AllowRadioReason(selectionDevice(10), RadioBLE); allow || reason != "peer-cap-no-slot(connected=3)" {
		t.Fatalf("cap veto: allow=%v reason=%q", allow, reason)
	}

	// Second radio with full slots: asset present via NAN (cost 512), BLE queried.
	snapshot.Links = []PeerLink{{Device: selectionDevice(10), Cost: 512}, {Device: selectionDevice(1), Cost: 512}, {Device: selectionDevice(2), Cost: 512}}
	if allow, reason := p.AllowRadioReason(selectionDevice(10), RadioBLE); allow || reason != "second-radio-slots-full(connected=3)" {
		t.Fatalf("second-radio veto: allow=%v reason=%q", allow, reason)
	}

	// Second radio displaced by a fresh different candidate.
	snapshot.Links = []PeerLink{{Device: selectionDevice(10), Cost: 512}, {Device: selectionDevice(1), Cost: 512}}
	p.Seen(selectionDevice(2), RadioBLE)
	if allow, reason := p.AllowRadioReason(selectionDevice(10), RadioBLE); allow || !strings.HasPrefix(reason, "second-radio-fresh-candidate(") {
		t.Fatalf("hint veto: allow=%v reason=%q", allow, reason)
	}

	// Failover kept once the hint expires.
	now = now.Add(61 * time.Second)
	if allow, reason := p.AllowRadioReason(selectionDevice(10), RadioBLE); !allow || reason != "second-radio-failover-kept" {
		t.Fatalf("failover: allow=%v reason=%q", allow, reason)
	}

	// Convergence: four concurrent assets, lowest-numbered radio loses.
	snapshot.Links = []PeerLink{{Device: selectionDevice(1), Cost: 512}, {Device: selectionDevice(2), Cost: 512}, {Device: selectionDevice(3), Cost: 512}, {Device: selectionDevice(4), Cost: 512}}
	if allow, reason := p.AllowRadioReason(selectionDevice(4), RadioBLE); allow || !strings.HasPrefix(reason, "convergence-not-selected") {
		t.Fatalf("convergence veto: allow=%v reason=%q", allow, reason)
	}

	// AllowRadio stays consistent with the reasoned decision everywhere above.
	for _, tc := range [][2]int32{{10, 1}, {10, 2}, {1, 2}, {4, 2}} {
		allow, _ := p.AllowRadioReason(selectionDevice(int(tc[0])), RadioMedium(tc[1]))
		if allow != p.AllowRadio(selectionDevice(int(tc[0])), RadioMedium(tc[1])) {
			t.Fatalf("AllowRadio disagrees with AllowRadioReason for asset=%d medium=%d", tc[0], tc[1])
		}
	}
}

func selectionDevice(number int) meshid.Device {
	tenant, _ := meshid.ParseTenant(testTenantUUID)
	device, _ := meshid.ParseDevice(tenant, fmt.Sprintf("00000000-0000-4000-8000-%012x", number))
	return device
}
