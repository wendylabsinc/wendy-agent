package meshcatalog

import (
	"encoding/binary"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/meshgossip"
)

func TestServiceHaveWantDiamondOneFullTransferIncludingWithdrawal(t *testing.T) {
	n := newNeighbourNetwork(t, 4)
	var mu sync.Mutex
	partial := map[[2]int][]byte{}
	full := map[int]map[meshgossip.ID]int{}
	legacy := 0
	n.observe = func(from, to int, fragment []byte) {
		mu.Lock()
		defer mu.Unlock()
		pair := [2]int{from, to}
		total, offset := int(binary.BigEndian.Uint32(fragment)), int(binary.BigEndian.Uint32(fragment[4:]))
		if offset == 0 {
			partial[pair] = nil
		}
		partial[pair] = append(partial[pair], fragment[8:]...)
		if len(partial[pair]) != total {
			return
		}
		var m inventoryMessage
		if err := json.Unmarshal(partial[pair], &m); err != nil {
			t.Error(err)
			return
		}
		delete(partial, pair)
		if m.Kind == "record" {
			legacy++
		}
		if m.Gossip != nil && m.Gossip.Op == "data" && m.Gossip.Offset+len(m.Gossip.Data) == m.Gossip.Total {
			if full[to] == nil {
				full[to] = make(map[meshgossip.ID]int)
			}
			full[to][m.Gossip.ID()]++
		}
	}
	n.connect(0, 1)
	n.connect(0, 2)
	n.connect(1, 3)
	n.connect(2, 3)
	n.connect(1, 2)
	for step := 0; step < 3; step++ {
		var w SignedRecord
		var err error
		if step == 2 {
			w, err = n.catalogs[0].Remove(testSpec().AppID, testSpec().ServiceID, time.Now())
		} else {
			w, err = n.catalogs[0].Publish(testSpec(), time.Now())
		}
		if err != nil {
			t.Fatal(err)
		}
		n.runtimes[0].Broadcast(w)
		body, _ := json.Marshal(w)
		id := meshgossip.Identify(meshgossip.Service, body)
		eventually(t, func() bool {
			for node := 1; node < 4; node++ {
				found := false
				for _, record := range n.catalogs[node].Records(time.Now()) {
					b, _ := json.Marshal(record)
					if meshgossip.Identify(meshgossip.Service, b) == id {
						found = true
					}
				}
				if !found {
					return false
				}
			}
			return true
		})
		// Force an explicit duplicate advertisement from every origin/relay;
		// the periodic repair path uses this same HAVE-only send operation.
		for node := 0; node < 4; node++ {
			n.runtimes[node].Broadcast(w)
		}
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		for node := 1; node < 4; node++ {
			if full[node][id] != 1 {
				t.Errorf("step %d node %d received %d full objects", step, node, full[node][id])
			}
		}
		if legacy != 0 {
			t.Error("unsolicited full service records", legacy)
		}
		mu.Unlock()
	}
	if len(n.catalogs[3].Snapshot(time.Now())) != 0 {
		t.Fatal("withdrawal did not remove service")
	}
}

func TestServiceSnapshotBarrierWaitsForSharedVerification(t *testing.T) {
	f := newFixture(t)
	source, cache := f.newCatalog(t, p533, "default", nil, nil)
	dest, destCache := f.newCatalog(t, p534, "default", nil, nil)
	w, err := source.Publish(testSpec(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	sender := NewSynchronizer(source, cache, meshgossip.NewPool())
	pool := meshgossip.NewPool()
	a, b := NewSynchronizer(dest, destCache, pool), NewSynchronizer(dest, destCache, pool)
	defer sender.Close()
	defer a.Close()
	defer b.Close()
	have := sender.Record(w, f.now)[0]
	request, _, err := a.Receive(have, f.now)
	if err != nil || len(request) != 1 {
		t.Fatal(request, err)
	}
	if duplicate, _, err := b.Receive(have, f.now); err != nil || len(duplicate) != 0 {
		t.Fatal("duplicate service fetch", duplicate, err)
	}
	if !b.PendingAnnouncements(f.now) {
		t.Fatal("snapshot complete before DATA")
	}
	data, _, err := sender.Receive(request[0], f.now)
	if err != nil {
		t.Fatal(err)
	}
	identity, changed, err := a.Receive(data[0], f.now)
	if err != nil || len(changed) != 0 || len(identity) != 1 {
		t.Fatal(identity, changed, err)
	}
	if !b.PendingAnnouncements(f.now) {
		t.Fatal("snapshot complete before signature verification")
	}
	description, _, err := sender.Receive(identity[0], f.now)
	if err != nil {
		t.Fatal(err)
	}
	changed = exchangeServiceMessages(t, sender, a, description, f.now)
	if len(changed) != 1 || b.PendingAnnouncements(f.now) {
		t.Fatal("shared verification did not finish alternate snapshot")
	}
	if _, _, err := b.Receive(Message{Kind: "record", Record: &w}, f.now); err == nil {
		t.Fatal("accepted unsolicited full service record")
	}
}
