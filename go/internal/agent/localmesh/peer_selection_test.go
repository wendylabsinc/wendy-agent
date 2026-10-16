package localmesh

import (
	"fmt"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"strings"
	"testing"
	"time"
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

func TestPeerSelectionCapsDistinctPeers(t *testing.T) {
	snapshot := NodeSnapshot{Links: []PeerLink{
		{Device: selectionDevice(10), Cost: 512}, {Device: selectionDevice(11), Cost: 4096}, {Device: selectionDevice(12), Cost: 128},
	}}
	p := NewPeerSelection(func() NodeSnapshot { return snapshot })
	if p.AllowRadio(selectionDevice(13), RadioBLE) || p.AllowRadio(selectionDevice(11), RadioNAN) {
		t.Fatal("radio selection exceeded the three-distinct-peer target")
	}
	if !p.AllowRadio(selectionDevice(10), RadioNAN) {
		t.Fatal("an existing sole radio link must be retained")
	}
	// If simultaneous handshakes temporarily add a fourth asset, the
	// periodic radio watchers shed the excess radio peer. LAN peers win.
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(13), Cost: 4096})
	if p.AllowRadio(selectionDevice(13), RadioBLE) || !p.AllowRadio(selectionDevice(10), RadioNAN) {
		t.Fatal("four-peer overshoot did not converge toward three")
	}
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(13), Cost: 64})
	if p.AllowRadio(selectionDevice(11), RadioBLE) {
		t.Fatal("new LAN neighbor did not displace an excess radio peer")
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
		t.Fatal("wired neighbor must displace a Wi-Fi link when LAN slots are full")
	}
	snapshot.Links = append(snapshot.Links, PeerLink{Device: selectionDevice(13), Cost: 64})
	if p.AllowLAN(selectionDevice(12), 128) || !p.AllowLAN(selectionDevice(13), 64) {
		t.Fatal("LAN overshoot did not converge to three best neighbors")
	}
	if p.AllowLAN(selectionDevice(10), 256) {
		t.Fatal("higher-cost LAN duplicate should be rejected")
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
