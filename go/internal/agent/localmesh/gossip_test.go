package localmesh

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/netgossip"
)

func TestManifestHaveWantDiamondTransfersEachRevisionOnce(t *testing.T) {
	source, chain, key, device, now := directoryFixture(t)
	dirs := []*Directory{source}
	pools := []*netgossip.Pool{netgossip.NewPool()}
	for range 3 {
		cache, err := OpenIdentityCache("", DefaultCacheLimits(), source.cache.verify, now)
		if err != nil {
			t.Fatal(err)
		}
		d, _ := NewDirectory(testTenant(t), 10, cache)
		dirs = append(dirs, d)
		pools = append(pools, netgossip.NewPool())
	}
	edges := [][2]int{{0, 1}, {0, 2}, {1, 3}, {2, 3}, {1, 2}}
	sessions := make(map[[2]int]*Synchronizer)
	for _, e := range edges {
		for _, ends := range [][2]int{e, {e[1], e[0]}} {
			s := NewSynchronizer(dirs[ends[0]], dirs[ends[0]].cache, pools[ends[0]])
			sessions[ends] = s
			defer s.Close()
		}
	}
	type delivery struct {
		from, to int
		m        ControlMessage
	}
	for revision := uint64(1); revision <= 3; revision++ {
		m := routeManifest(device, now)
		m.Revision = revision
		m.Withdraw = revision == 3
		w, err := SignManifest(m, chain, key, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = source.Accept(w, now); err != nil {
			t.Fatal(err)
		}
		var queue []delivery
		for edge, s := range sessions {
			if edge[0] == 0 {
				for _, m := range s.Record(w, now) {
					if m.Kind != "gossip" || m.Gossip.Op != "have" {
						t.Fatal("publication pushed a full manifest")
					}
					queue = append(queue, delivery{edge[0], edge[1], m})
				}
			}
		}
		full := make(map[int]int)
		for round := 0; round < 50; round++ {
			for len(queue) > 0 {
				d := queue[0]
				queue = queue[1:]
				var wire bytes.Buffer
				if err := WriteControl(&wire, d.m); err != nil {
					t.Fatal(err)
				}
				decoded, err := ReadControl(&wire)
				if err != nil {
					t.Fatal(err)
				}
				if decoded.Kind == "gossip" && decoded.Gossip.Op == "data" && decoded.Gossip.Offset+len(decoded.Gossip.Data) == decoded.Gossip.Total {
					full[d.to]++
				}
				replies, changed, err := sessions[[2]int{d.to, d.from}].Receive(decoded, now)
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range replies {
					queue = append(queue, delivery{d.to, d.from, m})
				}
				for _, w := range changed {
					for edge, s := range sessions {
						if edge[0] == d.to && edge[1] != d.from {
							for _, m := range s.Record(w, now) {
								queue = append(queue, delivery{edge[0], edge[1], m})
							}
						}
					}
				}
			}
			for edge, s := range sessions {
				replies, changed, err := s.ContinueCertificates(now)
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range replies {
					queue = append(queue, delivery{edge[0], edge[1], m})
				}
				for _, w := range changed {
					for other, forward := range sessions {
						if other[0] == edge[0] && other[1] != edge[1] {
							for _, m := range forward.Record(w, now) {
								queue = append(queue, delivery{other[0], other[1], m})
							}
						}
					}
				}
			}
			if len(queue) == 0 {
				break
			}
			if round == 49 {
				t.Fatal("protocol did not quiesce")
			}
		}
		for node := 1; node < 4; node++ {
			if full[node] != 1 {
				t.Fatalf("revision %d node %d received %d full manifests", revision, node, full[node])
			}
			records := dirs[node].Records(now)
			if len(records) != 1 {
				t.Fatal("manifest missing", node)
			}
			var got Manifest
			json.Unmarshal(records[0].Body, &got)
			if got.Revision != revision || got.Withdraw != m.Withdraw {
				t.Fatal("wrong revision/withdrawal", got)
			}
		}
		// Reconnect/periodic repair sends HAVE again, not duplicate DATA.
		for _, edge := range [][2]int{{1, 3}, {2, 3}} {
			for _, m := range sessions[edge].Reconcile(now) {
				replies, _, err := sessions[[2]int{edge[1], edge[0]}].Receive(m, now)
				if err != nil || len(replies) > 0 {
					t.Fatal("known revision redownloaded", replies, err)
				}
			}
		}
	}
}

func TestRouterGossipRejectsUnsolicitedManifestButLeafRemainsCompatible(t *testing.T) {
	d, chain, key, device, now := directoryFixture(t)
	w, err := SignManifest(routeManifest(device, now), chain, key, now)
	if err != nil {
		t.Fatal(err)
	}
	router := NewSynchronizer(d, d.cache, netgossip.NewPool())
	defer router.Close()
	if _, _, err := router.Receive(ControlMessage{Kind: "manifest", Manifest: &w}, now); err == nil {
		t.Fatal("router accepted full unsolicited manifest")
	}
	leaf := NewSynchronizer(d, d.cache)
	defer leaf.Close()
	if _, _, err := leaf.Receive(ControlMessage{Kind: "manifest", Manifest: &w}, now); err != nil {
		t.Fatal("legacy leaf envelope rejected", err)
	}
}

func TestManifestGossipQueueBudgetDoesNotConsumeAnnouncement(t *testing.T) {
	d, chain, key, device, now := directoryFixture(t)
	w, _ := SignManifest(routeManifest(device, now), chain, key, now)
	d.Accept(w, now)
	s := NewSynchronizer(d, d.cache, netgossip.NewPool())
	defer s.Close()
	if got := s.ReconcileBounded(now, 0); len(got) != 0 {
		t.Fatal(got)
	}
	got := s.ContinueReconcile(now, 1)
	if len(got) != 1 || got[0].Gossip == nil || got[0].Gossip.Op != "have" {
		t.Fatal("dropped unqueued announcement", got)
	}
}
