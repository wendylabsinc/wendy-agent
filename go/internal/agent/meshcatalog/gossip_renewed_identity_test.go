package meshcatalog

import (
	"errors"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/agent/netgossip"
)

func TestServiceGossipRenewalRejectsPreviousCertificateOffersAndCachedBodies(t *testing.T) {
	old, renewed := newFixture(t), newFixture(t)
	old.now = renewed.now
	allowOld := true
	verifyCurrent := func(chain [][]byte, now time.Time) (localmesh.Identity, error) {
		if id, err := renewed.creds[p534].Verify(chain, now); err == nil {
			return id, nil
		}
		if allowOld {
			return old.creds[p534].Verify(chain, now)
		}
		return localmesh.Identity{}, errors.New("old issuer retired")
	}
	cache, err := localmesh.OpenIdentityCache("", localmesh.DefaultCacheLimits(), verifyCurrent, old.now)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewCatalog("default", old.creds[p534].Device, 32, old.creds[p534], cache,
		func(string, string, uint16) error { return nil }, nil, func([]Receipt) error { return nil }, old.now)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := old.newCatalog(t, p533, "default", nil, nil)
	wire, err := origin.Publish(testSpec(), old.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Put(old.creds[p533].Certificate.Certificate, old.now); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Accept(wire, old.now); err != nil {
		t.Fatal(err)
	}
	sender := NewSynchronizer(relay, cache, netgossip.NewPool())
	defer sender.Close()
	have := sender.Record(wire, old.now)
	if len(have) != 1 || have[0].Gossip == nil || have[0].Gossip.Op != "have" {
		t.Fatal("initial current artifact was not advertised")
	}
	requester := netgossip.NewPool().NewSession(netgossip.Service)
	defer requester.Close()
	want, _, err := requester.Receive(*have[0].Gossip, old.now)
	if err != nil || want == nil {
		t.Fatal(want, err)
	}

	// HAVE may already be queued when the principal receives a new trusted leaf.
	// Neither an old Broadcast nor a delayed WANT may borrow that new identity's
	// eligibility to advertise or serve bytes signed by the retired certificate.
	allowOld = false
	if _, err := cache.Put(renewed.creds[p533].Certificate.Certificate, old.now); err != nil {
		t.Fatal(err)
	}
	if got := sender.Record(wire, old.now); len(got) != 0 {
		t.Error("queued old artifact was advertised under renewed trust")
	}
	replies, _, err := sender.Receive(Message{Kind: "gossip", Gossip: want}, old.now)
	if err != nil || len(replies) != 1 || replies[0].Gossip == nil || replies[0].Gossip.Op != "unavailable" {
		t.Errorf("cached old body remained downloadable after renewal: replies=%v err=%v", replies, err)
	}
	key := Key{"default", p533, testSpec().AppID, testSpec().ServiceID}
	if got := relay.servicePayload(key, old.now); len(got) != 0 {
		t.Error("authoritative payload loader recovered an old-certificate artifact")
	}

	currentOrigin, _ := renewed.newCatalog(t, p533, "default", []Receipt{relay.history[key]}, nil)
	current, err := currentOrigin.Publish(testSpec(), old.now)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := relay.Accept(current, old.now); err != nil || !changed {
		t.Fatalf("renewed generation did not recover: changed=%v err=%v", changed, err)
	}
	cold, coldCache := renewed.newCatalog(t, p535, "default", nil, nil)
	receiver := NewSynchronizer(cold, coldCache, netgossip.NewPool())
	defer receiver.Close()
	changed := exchangeServiceMessages(t, sender, receiver, sender.Record(current, old.now), old.now)
	if len(changed) != 1 || len(cold.Snapshot(old.now)) != 1 || cold.Snapshot(old.now)[0].Generation != 2 {
		t.Fatal("renewed artifact failed pooled download and cold certificate repair")
	}
	if _, err := relay.Accept(wire, old.now); err == nil {
		t.Fatal("renewed cache accepted an old leaf fingerprint")
	}
	expired := old.now.Add(testSpec().Lease + time.Second)
	if got := sender.Record(current, expired); len(got) != 0 || relay.history[key].Generation != 2 {
		t.Fatal("gossip renewed a signed lease or lost its generation floor")
	}
}
