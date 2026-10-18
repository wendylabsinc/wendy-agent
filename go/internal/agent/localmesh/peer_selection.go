package localmesh

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// Only automatically discovered Ethernet and infrastructure Wi-Fi links
// suppress radio discovery. Configured TCP peers have cost 256, and may need
// NAN or BLE as a second path when that TCP link fails.
const MaxLANCost uint16 = 128

type RadioMedium uint8

const (
	RadioNAN RadioMedium = iota + 1
	RadioBLE
)

type radioCandidate struct {
	seen        time.Time
	failedUntil time.Time
}

type radioCandidateKey struct {
	asset  meshid.Device
	medium RadioMedium
}

// PeerSelection shares one diversity decision across the NAN and BLE
// providers. Discovery is only a hint; the snapshot contains only peers that
// passed device mTLS. Candidate hints expire quickly so stale advertisements
// cannot suppress a useful fallback indefinitely.
type PeerSelection struct {
	snapshot func() NodeSnapshot
	mu       sync.Mutex
	seen     map[radioCandidateKey]radioCandidate
	now      func() time.Time
}

func NewPeerSelection(snapshot func() NodeSnapshot) *PeerSelection {
	return &PeerSelection{snapshot: snapshot, seen: make(map[radioCandidateKey]radioCandidate), now: time.Now}
}

func (p *PeerSelection) Seen(asset meshid.Device, medium RadioMedium) {
	if p == nil || asset == (meshid.Device{}) || (medium != RadioNAN && medium != RadioBLE) {
		return
	}
	p.mu.Lock()
	key := radioCandidateKey{asset, medium}
	hint := p.seen[key]
	hint.seen = p.now()
	p.seen[key] = hint
	p.mu.Unlock()
}

// Failed marks a discovery hint temporarily unavailable after a failed radio
// setup. Repeated advertisements do not reset this cooldown. The carrier may
// still retry it; the cooldown only stops a dead candidate from suppressing a
// useful second radio path to an already authenticated peer.
func (p *PeerSelection) Failed(asset meshid.Device, medium RadioMedium) {
	if p == nil || asset == (meshid.Device{}) || (medium != RadioNAN && medium != RadioBLE) {
		return
	}
	p.mu.Lock()
	key := radioCandidateKey{asset, medium}
	hint := p.seen[key]
	hint.failedUntil = p.now().Add(30 * time.Second)
	p.seen[key] = hint
	p.mu.Unlock()
}

func (p *PeerSelection) Connected(asset meshid.Device, medium RadioMedium) {
	if p == nil || asset == (meshid.Device{}) || (medium != RadioNAN && medium != RadioBLE) {
		return
	}
	p.mu.Lock()
	key := radioCandidateKey{asset, medium}
	hint := p.seen[key]
	hint.seen = p.now()
	hint.failedUntil = time.Time{}
	p.seen[key] = hint
	p.mu.Unlock()
}

func (p *PeerSelection) HasLAN(asset meshid.Device) bool {
	if p == nil || p.snapshot == nil {
		return false
	}
	for _, link := range p.snapshot().Links {
		if link.Device == asset && link.Cost <= MaxLANCost {
			return true
		}
	}
	return false
}

// AllowLAN keeps every discovered LAN neighbor, preferring a cheaper carrier
// only for the same principal. Ranking unrelated peers at both ends can form
// a closed clique that permanently excludes another reachable device.
// The LAN carrier separately bounds concurrent connections and handshakes.
func (p *PeerSelection) AllowLAN(asset meshid.Device, cost uint16) bool {
	if p == nil || p.snapshot == nil {
		return true
	}
	if asset == (meshid.Device{}) || cost == 0 || cost > MaxLANCost {
		return false
	}
	for _, link := range p.snapshot().Links {
		if link.Device == asset && link.Cost < cost {
			return false
		}
	}
	return true
}

// AllowRadio limits radio-only neighbors to three distinct principals. LAN
// and configured TCP neighbors do not consume radio slots, so a populated
// LAN can still connect to a different LAN through a radio-only neighbor.
// A spare BLE link can provide failover while no other candidate needs it.
func (p *PeerSelection) AllowRadio(asset meshid.Device, medium RadioMedium) bool {
	allow, _ := p.AllowRadioReason(asset, medium)
	return allow
}

// AllowRadioReason reports the same decision as AllowRadio plus a stable
// machine-readable reason naming the branch that allowed or vetoed the link.
// Veto reasons exist so a torn-down established link can be attributed to the
// exact clause that fired instead of hypothesizing after the fact.
func (p *PeerSelection) AllowRadioReason(asset meshid.Device, medium RadioMedium) (bool, string) {
	if p == nil || p.snapshot == nil {
		return true, "no-policy"
	}
	if asset == (meshid.Device{}) || (medium != RadioNAN && medium != RadioBLE) {
		return false, "invalid-asset-or-medium"
	}
	s := p.snapshot()
	connected := make(map[meshid.Device]bool, len(s.Links))
	lan := make(map[meshid.Device]bool)
	radio := make(map[meshid.Device]bool)
	otherRadio := false
	for _, link := range s.Links {
		connected[link.Device] = true
		if link.Cost <= MaxLANCost {
			lan[link.Device] = true
		} else if link.Cost >= 512 {
			radio[link.Device] = true
		}
		if link.Device == asset && link.Cost <= MaxLANCost {
			return false, fmt.Sprintf("lan-supersedes(cost=%d)", link.Cost)
		}
		if link.Device == asset && ((medium == RadioNAN && link.Cost > 512) || (medium == RadioBLE && link.Cost == 512)) {
			otherRadio = true
		}
	}
	for device := range lan {
		delete(radio, device)
	}
	if !radio[asset] && len(radio) >= 3 {
		return false, fmt.Sprintf("peer-cap-no-slot(connected=%d)", len(radio))
	}
	if len(radio) > 3 {
		// Concurrent radio handshakes can briefly exceed the hardware budget.
		// Shed only excess radio-only neighbors, independently of LAN degree.
		keep := make([]meshid.Device, 0, len(radio))
		for candidate := range radio {
			keep = append(keep, candidate)
		}
		sort.Slice(keep, func(i, j int) bool { return meshid.Compare(keep[i], keep[j]) < 0 })
		selected := false
		for _, candidate := range keep[:3] {
			selected = selected || candidate == asset
		}
		if !selected {
			return false, fmt.Sprintf("convergence-not-selected(connected=%d)", len(radio))
		}
	}
	if !otherRadio {
		return true, "first-radio"
	}
	// Prefer NAN over BLE for the same principal. Both providers recheck this
	// policy periodically: vetoing both duplicates would tear down every path
	// to a retained neighbor. BLE is shed only after NAN is authenticated.
	if medium == RadioNAN {
		return true, "preferred-radio"
	}
	if len(radio) >= 3 {
		return false, fmt.Sprintf("second-radio-slots-full(connected=%d)", len(radio))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for key, hint := range p.seen {
		if now.Sub(hint.seen) > 60*time.Second {
			delete(p.seen, key)
			continue
		}
		if key.asset != asset && !connected[key.asset] && !now.Before(hint.failedUntil) {
			return false, fmt.Sprintf("second-radio-fresh-candidate(principal=%s)", key.asset.Principal())
		}
	}
	return true, "second-radio-failover-kept"
}
