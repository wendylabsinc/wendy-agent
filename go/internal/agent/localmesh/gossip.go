package localmesh

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/netgossip"
)

func (s *Synchronizer) offerManifest(w SignedManifest, now time.Time) []ControlMessage {
	if _, _, ok := s.cache.Get(w.Principal, now); !ok {
		return nil
	}
	var manifest Manifest
	if json.Unmarshal(w.Body, &manifest) != nil {
		return nil
	}
	data, err := json.Marshal(w)
	if err != nil {
		return nil
	}
	m := s.gossip.Offer(data, time.UnixMilli(manifest.Expires), now)
	if m == nil {
		return nil
	}
	return []ControlMessage{{Kind: "gossip", Gossip: m}}
}
func (s *Synchronizer) resolveManifest(w SignedManifest, now time.Time, accepted bool) {
	var manifest Manifest
	_ = json.Unmarshal(w.Body, &manifest)
	data, _ := json.Marshal(w)
	s.gossip.Resolve(data, time.UnixMilli(manifest.Expires), now, accepted)
}

// Router links require HAVE/WANT; the legacy constructor is retained only for
// the separately negotiated endpoint-only leaf protocol and protocol fixtures.
func (s *Synchronizer) Receive(m ControlMessage, now time.Time, slots ...int) ([]ControlMessage, []SignedManifest, error) {
	if m.Kind != "gossip" {
		if s.gossip != nil && m.Kind == "manifest" {
			return nil, nil, errors.New("unsolicited router manifest")
		}
		return s.receive(m, now)
	}
	if s.gossip == nil || m.Gossip == nil {
		return nil, nil, errors.New("manifest gossip not negotiated")
	}
	if err := m.Gossip.Validate(); err != nil {
		return nil, nil, err
	}
	if m.Gossip.Op == "want" {
		payload := s.gossip.Lookup(m.Gossip.ID(), now)
		if len(payload) == 0 {
			// The bounded byte cache may have evicted this payload, while the
			// authoritative store still has it. A receipt alone is never a body.
			for _, candidate := range s.directory.Records(now) {
				b, _ := json.Marshal(candidate)
				if netgossip.Identify(netgossip.Manifest, b) == m.Gossip.ID() {
					s.offerManifest(candidate, now)
					payload = b
					break
				}
			}
		}
		if len(payload) > 0 {
			var w SignedManifest
			if err := json.Unmarshal(payload, &w); err != nil {
				return nil, nil, err
			}
			if _, _, ok := s.cache.Get(w.Principal, now); !ok {
				return []ControlMessage{{Kind: "gossip", Gossip: netgossip.Unavailable(*m.Gossip)}}, nil, nil
			}
		}
	}
	reply, data, err := s.gossip.Receive(*m.Gossip, now)
	var replies []ControlMessage
	if reply != nil {
		replies = append(replies, ControlMessage{Kind: "gossip", Gossip: reply})
	}
	if err == nil && m.Gossip.Op == "want" && len(slots) > 0 {
		for _, chunk := range s.gossip.Poll(now, max(0, slots[0]-len(replies))) {
			replies = append(replies, ControlMessage{Kind: "gossip", Gossip: chunk})
		}
	}
	if err != nil || len(data) == 0 {
		return replies, nil, err
	}
	var w SignedManifest
	if err = json.Unmarshal(data, &w); err != nil {
		return nil, nil, err
	}
	// Require the exact deterministic envelope used for hashing, including no
	// unknown fields. Otherwise re-marshalling could leave a claim unresolved.
	canonical, _ := json.Marshal(w)
	if string(canonical) != string(data) {
		return nil, nil, errors.New("noncanonical manifest artifact")
	}
	out, changed, err := s.receive(ControlMessage{Kind: "manifest", Manifest: &w}, now)
	return append(replies, out...), changed, err
}
