package meshcatalog

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/netgossip"
)

func (s *Synchronizer) offerService(w SignedRecord, now time.Time) []Message {
	var record Record
	if json.Unmarshal(w.Body, &record) != nil {
		return nil
	}
	if _, _, ok := s.cache.Get(record.Key.Device, now); !ok {
		return nil
	}
	data, err := json.Marshal(w)
	if err != nil {
		return nil
	}
	m := s.gossip.Offer(data, time.UnixMilli(record.Expires), now)
	if m == nil {
		return nil
	}
	return []Message{{Kind: "gossip", Gossip: m}}
}
func (s *Synchronizer) resolveService(w SignedRecord, now time.Time, accepted bool) {
	var record Record
	_ = json.Unmarshal(w.Body, &record)
	data, _ := json.Marshal(w)
	s.gossip.Resolve(data, time.UnixMilli(record.Expires), now, accepted)
}
func (s *Synchronizer) Receive(m Message, now time.Time, slots ...int) ([]Message, []SignedRecord, error) {
	if m.Kind != "gossip" {
		if s.gossip != nil && m.Kind == "record" {
			return nil, nil, errors.New("unsolicited service record")
		}
		return s.receive(m, now)
	}
	if s.gossip == nil || m.Gossip == nil {
		return nil, nil, errors.New("service gossip not negotiated")
	}
	if err := m.validate(); err != nil {
		return nil, nil, err
	}
	if m.Gossip.Op == "want" {
		payload := s.gossip.Lookup(m.Gossip.ID(), now)
		if len(payload) == 0 {
			// The bounded byte cache may have evicted this payload, while the
			// authoritative store still has it. A receipt alone is never a body.
			for _, candidate := range s.catalog.Records(now) {
				b, _ := json.Marshal(candidate)
				if netgossip.Identify(netgossip.Service, b) == m.Gossip.ID() {
					s.offerService(candidate, now)
					payload = b
					break
				}
			}
		}
		if len(payload) > 0 {
			var w SignedRecord
			var r Record
			if json.Unmarshal(payload, &w) != nil || json.Unmarshal(w.Body, &r) != nil {
				return nil, nil, errors.New("invalid cached service artifact")
			}
			if _, _, ok := s.cache.Get(r.Key.Device, now); !ok {
				return []Message{{Kind: "gossip", Gossip: netgossip.Unavailable(*m.Gossip)}}, nil, nil
			}
		}
	}
	reply, data, err := s.gossip.Receive(*m.Gossip, now)
	var replies []Message
	if reply != nil {
		replies = append(replies, Message{Kind: "gossip", Gossip: reply})
	}
	if err == nil && m.Gossip.Op == "want" && len(slots) > 0 {
		for _, chunk := range s.gossip.Poll(now, max(0, slots[0]-len(replies))) {
			replies = append(replies, Message{Kind: "gossip", Gossip: chunk})
		}
	}
	if err != nil || len(data) == 0 {
		return replies, nil, err
	}
	var w SignedRecord
	if err = json.Unmarshal(data, &w); err != nil {
		return nil, nil, err
	}
	canonical, _ := json.Marshal(w)
	if string(canonical) != string(data) {
		return nil, nil, errors.New("noncanonical service artifact")
	}
	out, changed, err := s.receive(Message{Kind: "record", Record: &w}, now)
	return append(replies, out...), changed, err
}
