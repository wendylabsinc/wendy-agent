package meshcatalog

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestBroadcastFullPeerQueuesDoNotCopyDroppedRecords(t *testing.T) {
	r := &Runtime{peers: make(map[meshid.Device]*catalogPeer)}
	for i := 1; i <= 100; i++ {
		p := &catalogPeer{out: make(chan SignedRecord, 128)}
		for range cap(p.out) {
			p.out <- SignedRecord{}
		}
		r.peers[deviceForIndex(i)] = p
	}
	w := SignedRecord{Fingerprint: "fixture", Body: bytes.Repeat([]byte("x"), MaxRecordBytes), Signature: make([]byte, 64)}
	allocations := testing.AllocsPerRun(20, func() { r.Broadcast(w) })
	t.Logf("100 full peer queues: allocations per dropped broadcast=%.0f", allocations)
	if allocations != 0 {
		t.Fatalf("dropped record copied: %.0f allocations", allocations)
	}
	for _, p := range r.peers {
		if len(p.out) != cap(p.out) {
			t.Fatal("changed full queue")
		}
	}
}

func TestBroadcastAfterQueueDrainCopiesForEachPeer(t *testing.T) {
	a, b := &catalogPeer{out: make(chan SignedRecord, 1)}, &catalogPeer{out: make(chan SignedRecord, 1)}
	r := &Runtime{peers: map[meshid.Device]*catalogPeer{deviceForIndex(1): a, deviceForIndex(2): b}}
	a.out <- SignedRecord{}
	b.out <- SignedRecord{}
	r.Broadcast(SignedRecord{Body: []byte("dropped")})
	<-a.out
	<-b.out
	w := SignedRecord{Body: []byte("withdrawal"), Signature: []byte("signature")}
	r.Broadcast(w)
	x, y := <-a.out, <-b.out
	w.Body[0], w.Signature[0] = 'X', 'X'
	x.Body[1], x.Signature[1] = 'Y', 'Y'
	if string(y.Body) != "withdrawal" || string(y.Signature) != "signature" {
		t.Fatal("queued peers alias caller or one another")
	}
}

// deviceForIndex builds a deterministic in-tenant device so peer maps key on
// real identities instead of numeric stand-ins.
func deviceForIndex(i int) meshid.Device {
	principal := fmt.Sprintf("spiffe://wendy.sh/tenant/%s/device/%08x-0000-4000-8000-00000000%04x", testTenantID, i, i)
	device, _ := meshid.DeviceFromPrincipal(principal)
	return device
}
