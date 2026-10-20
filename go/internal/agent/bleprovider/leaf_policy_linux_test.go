//go:build linux

package bleprovider

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestBLELeafAndRouterSharePhysicalAdapterBudget(t *testing.T) {
	policy := localmesh.NewConnectionPolicy(selfFixture, false, localmesh.ConnectionLimits{Outgoing: 2, Attempts: 1, Total: 3})
	queue := &localmesh.CandidateQueue{}
	router := &runtime{cfg: Config{Policy: policy, Candidates: queue}}
	peer := leafCandidate{config: localmesh.LeafBLEPeer{Device: lowFixture.UUID().String(), Address: "02:00:00:00:00:01", AddressType: "public"}, peer: lowFixture}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dialed := make(chan struct{}, 1)
	allow := make(chan struct{})
	ready := make(chan struct{}, 1)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	leaf := &leafRuntime{policy: policy, candidates: queue, peers: []leafCandidate{peer}, dial: func(ctx context.Context, _ localmesh.LeafBLEPeer) (net.Conn, error) {
		dialed <- struct{}{}
		select {
		case <-allow:
			return left, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, serve: func(ctx context.Context, device meshid.Device, conn net.Conn) error {
		if !localmesh.ConnectionReady(ctx, device, func() { _ = conn.Close() }) {
			return errors.New("leaf admission rejected")
		}
		ready <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}
	defer func() { cancel(); leaf.workers.Wait() }()
	incoming := router.connectionPolicy().Reserve("incoming", peerFixture, false)
	if incoming == nil {
		t.Fatal("incoming reservation")
	}
	defer incoming.Release()
	pending := router.connectionPolicy().Reserve("router", testPeerDevice(), true)
	if pending == nil {
		t.Fatal("router setup")
	}
	now := time.Now()
	leaf.startCandidates(ctx, now)
	if len(dialed) != 0 || policy.Counts().Attempts != 1 {
		t.Fatal("leaf bypassed router's pending setup")
	}
	pending.Release()
	leaf.startCandidates(ctx, now)
	select {
	case <-dialed:
	case <-ctx.Done():
		t.Fatal("leaf dial did not start")
	}
	radio := candidate{address: "02:00:00:00:00:02", addressType: "public"}
	if lease := router.reserveCandidate(radio); lease != nil {
		lease.Release()
		t.Fatal("router bypassed leaf's pending setup")
	}
	close(allow)
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("leaf never became ready")
	}
	lease := router.reserveCandidate(radio)
	if lease == nil {
		t.Fatal("ready leaf retained setup capacity")
	}
	defer lease.Release()
	if !lease.Ready(testPeerDevice(), func() {}) {
		t.Fatal("router admission")
	}
	c := policy.Counts()
	if c.Outgoing != 2 || c.Attempts != 0 || c.Total != 3 {
		t.Fatalf("combined counts: %+v", c)
	}
	if extra := policy.Reserve("fourth", meshid.Device{}, false); extra != nil {
		extra.Release()
		t.Fatal("adapter total exceeded")
	}
	if extra := policy.Reserve("third-outgoing", meshid.Device{}, true); extra != nil {
		extra.Release()
		t.Fatal("adapter outgoing exceeded")
	}
	cancel()
	leaf.workers.Wait()
	if c := policy.Counts(); c.Total != 2 || c.Outgoing != 1 {
		t.Fatalf("leaf cancellation leaked reservation: %+v", c)
	}
}

func testPeerDevice() meshid.Device { return fixtureDevice("12345678-1234-4000-8000-000000000099") }

func TestBLELeafFailureReleasesBudgetAndSharesPenalty(t *testing.T) {
	policy := localmesh.NewConnectionPolicy(selfFixture, false, localmesh.ConnectionLimits{Outgoing: 2, Attempts: 1, Total: 3})
	queue := &localmesh.CandidateQueue{}
	peer := leafCandidate{config: localmesh.LeafBLEPeer{Device: lowFixture.UUID().String(), Address: "02:00:00:00:00:01", AddressType: "public"}, peer: lowFixture}
	leaf := &leafRuntime{policy: policy, candidates: queue, peers: []leafCandidate{peer}, dial: func(context.Context, localmesh.LeafBLEPeer) (net.Conn, error) {
		return nil, errors.New("time/bootstrap/dial failed")
	}}
	leaf.startCandidates(context.Background(), time.Now())
	leaf.workers.Wait()
	if c := policy.Counts(); c.Total != 0 {
		t.Fatalf("failed leaf leaked budget: %+v", c)
	}
	router := &runtime{cfg: Config{Policy: policy, Candidates: queue}}
	healthy := "public:02:00:00:00:00:02"
	for range 20 {
		if keys := router.candidateQueue().Order([]string{peer.key(), healthy}, time.Now()); keys[0] != healthy {
			t.Fatal("router ignored leaf failure history")
		}
	}
	// Address formatting must not create a second reservation for the same radio.
	duplicate := candidate{address: "02:00:00:00:00:01", addressType: " PUBLIC "}
	if duplicate.key() != peer.key() {
		t.Fatal("router/leaf radio keys differ")
	}
}

func TestBLELeafAndRouterShareDialPacing(t *testing.T) {
	policy := localmesh.NewConnectionPolicy(selfFixture, false, localmesh.ConnectionLimits{Outgoing: 2, Attempts: 1, Total: 3})
	pacer := &DialPacer{}
	router := &runtime{cfg: Config{Policy: policy, Pacer: pacer}}
	now := time.Now()
	if !router.admitDial(now) {
		t.Fatal("initial router start denied")
	}
	attempted := 0
	leaf := &leafRuntime{policy: policy, candidates: &localmesh.CandidateQueue{}, pacer: pacer, peers: []leafCandidate{{config: localmesh.LeafBLEPeer{Address: "02:00:00:00:00:01", AddressType: "public"}, peer: lowFixture}}, dial: func(context.Context, localmesh.LeafBLEPeer) (net.Conn, error) {
		attempted++
		return nil, errors.New("fixture dial")
	}}
	leaf.startCandidates(context.Background(), now.Add(time.Second))
	leaf.workers.Wait()
	if attempted != 0 || policy.Counts().Total != 0 {
		t.Fatal("leaf bypassed pacing or retained skipped reservation")
	}
	leaf.startCandidates(context.Background(), now.Add(globalDialPace))
	leaf.workers.Wait()
	if attempted != 1 || router.admitDial(now.Add(globalDialPace)) {
		t.Fatal("consumers did not share adapter pacing")
	}
}
