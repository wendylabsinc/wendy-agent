package meshcatalog

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"testing"
	"time"
)

func TestInventoryFramingAndBounds(t *testing.T) {
	fps := make([]string, maxInventoryEntries)
	for i := range fps {
		fps[i] = fmt.Sprintf("%064x", i)
	}
	var received receivedInventory
	var wire bytes.Buffer
	for _, m := range inventoryMessages(fps) {
		if err := writeInventoryMessage(&wire, m); err != nil {
			t.Fatal(err)
		}
		got, err := readInventoryMessage(&wire)
		if err != nil {
			t.Fatal(err)
		}
		if err = received.receive(got); err != nil {
			t.Fatal(err)
		}
	}
	if !received.done || len(received.fingerprints) != maxInventoryEntries {
		t.Fatal("inventory incomplete")
	}
	if err := received.receive(inventoryMessage{Message: Message{Kind: "identity-cache-done"}}); err == nil {
		t.Fatal("duplicate completion accepted")
	}
	var duplicate receivedInventory
	m := inventoryMessages(fps[:1])[0]
	if duplicate.receive(m) != nil || duplicate.receive(m) == nil {
		t.Fatal("duplicate inventory accepted")
	}
	var oversized receivedInventory
	for _, m := range inventoryMessages(append(fps, fmt.Sprintf("%064x", 1024)))[:8] {
		if err := oversized.receive(m); err != nil {
			t.Fatal(err)
		}
	}
	if oversized.receive(inventoryMessages([]string{fmt.Sprintf("%064x", 1024)})[0]) == nil {
		t.Fatal("oversized inventory accepted")
	}
	invalid := []inventoryMessage{
		{Message: Message{Kind: "identity-cache"}},
		{Message: Message{Kind: "identity-cache"}, Fingerprints: make([]byte, 33)},
		{Message: Message{Kind: "identity-cache"}, Fingerprints: make([]byte, 32*(inventoryChunkEntries+1))},
		{Message: Message{Kind: "identity-cache-done"}, Fingerprints: make([]byte, 32)},
		{Message: Message{Kind: "snapshot-done"}, Fingerprints: make([]byte, 32)},
	}
	for _, m := range invalid {
		if writeInventoryMessage(new(bytes.Buffer), m) == nil {
			t.Fatalf("invalid frame accepted: %s", m.Kind)
		}
	}
	var early receivedInventory
	if early.receive(inventoryMessage{Message: Message{Kind: "snapshot-done"}}) == nil {
		t.Fatal("data before inventory accepted")
	}
	var old bytes.Buffer
	if err := writeInventoryMessage(&old, inventoryMessages(fps[:1])[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMessage(&old); err == nil {
		t.Fatal("legacy parser accepted inventory")
	}
	for _, raw := range []string{`{"kind":"identity-cache-done","unknown":1}`, `{"kind":"identity-cache-done"} {}`, `{"kind":"identity-cache","fingerprints":"%%%"}`} {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, uint32(len(raw)))
		b.WriteString(raw)
		if _, err := readInventoryMessage(&b); err == nil {
			t.Fatalf("malformed wire accepted: %s", raw)
		}
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(MaxMessageBytes+1))
	if _, err := readInventoryMessage(&b); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestInventoryDoesNotGrantTrustOrBypassSignatures(t *testing.T) {
	f := newFixture(t)
	origin, oc := f.newCatalog(t, p535, "default", nil, nil)
	w, err := origin.Publish(testSpec(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	allowed := true
	cache, err := localmesh.OpenIdentityCache("", localmesh.DefaultCacheLimits(), func(chain [][]byte, now time.Time) (localmesh.Identity, error) {
		if !allowed {
			return localmesh.Identity{}, errors.New("revoked")
		}
		return f.creds[p533].Verify(chain, now)
	}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := NewCatalog("default", f.creds[p533].Device, 32, f.creds[p533], cache, func(string, string, uint16) error { return nil }, nil, func([]Receipt) error { return nil }, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cache.Put(f.creds[p535].Certificate.Certificate, f.now); err != nil {
		t.Fatal(err)
	}
	fps := cache.Fingerprints(f.now, 1024)
	if len(fps) != 2 {
		t.Fatal("valid identity enumeration")
	}
	var inv receivedInventory
	for _, m := range inventoryMessages(fps) {
		if err = inv.receive(m); err != nil {
			t.Fatal(err)
		}
	}
	sender := NewSynchronizer(origin, oc)
	sender.useCurrentInventory(inv)
	frames := sender.Record(w, f.now)
	if len(frames) != 1 || frames[0].Kind != "record" {
		t.Fatal("inventory did not omit bundle")
	}
	receiver := NewSynchronizer(viewer, cache)
	tampered := cloneWire(w)
	tampered.Signature[0] ^= 0xff
	if _, _, err = receiver.Receive(Message{Kind: "record", Record: &tampered}, f.now); err == nil {
		t.Fatal("cached inventory bypassed signature")
	}
	allowed = false
	if got := cache.Fingerprints(f.now, 1024); len(got) != 0 {
		t.Fatal("revoked identities advertised")
	}
	if _, changed, err := receiver.Receive(frames[0], f.now); err != nil || len(changed) != 0 || !receiver.PendingIdentity() {
		t.Fatalf("revoked identity admitted: %v", err)
	}
	if _, _, err = receiver.Receive(Message{Kind: "bundle", Bundle: f.creds[p535].Certificate.Certificate}, f.now); err == nil {
		t.Fatal("repair bypassed revoked trust")
	}
	if len(viewer.Snapshot(f.now)) != 0 {
		t.Fatal("untrusted service visible")
	}
	allowed = true
	if len(cache.Fingerprints(f.now.Add(25*time.Hour), 1024)) != 0 {
		t.Fatal("expired identities advertised")
	}
}

func TestInventoryStaleAdvertisementCannotExceedPendingBound(t *testing.T) {
	f := newFixture(t)
	origin, oc := f.newCatalog(t, p535, "default", nil, nil)
	viewer, cache := f.newCatalog(t, p533, "default", nil, nil)
	fp := localmesh.Fingerprint(f.creds[p535].Certificate.Certificate)
	sender := NewSynchronizer(origin, oc)
	sender.useCurrentInventory(receivedInventory{fingerprints: map[string]bool{fp: true}, done: true})
	receiver := NewSynchronizer(viewer, cache)
	for i := range 9 {
		spec := testSpec()
		spec.ServiceID = fmt.Sprintf("http-%d", i)
		w, err := origin.Publish(spec, f.now)
		if err != nil {
			t.Fatal(err)
		}
		frames := sender.Record(w, f.now)
		if len(frames) != 1 {
			t.Fatal("current inventory unexpectedly reverted to four hints")
		}
		_, _, err = receiver.Receive(frames[0], f.now)
		if i < 8 && err != nil {
			t.Fatal(err)
		}
		if i == 8 && !errors.Is(err, errPendingIdentities) {
			t.Fatal("ninth pending identity did not fail closed")
		}
	}
	if n := len(receiver.pending[p535]); n != 8 {
		t.Fatalf("pending=%d", n)
	}
	if len(viewer.Snapshot(f.now)) != 0 {
		t.Fatal("unverified snapshot projected")
	}
	// Empty inventory cold retry carries its bundle first and admits all records.
	retry := NewSynchronizer(origin, oc)
	fresh := NewSynchronizer(viewer, cache)
	for _, m := range retry.Reconcile(f.now) {
		if _, _, err := fresh.Receive(m, f.now); err != nil {
			t.Fatal(err)
		}
	}
	if fresh.PendingIdentity() || len(viewer.Snapshot(f.now)) != 9 {
		t.Fatal("cold retry failed")
	}
}

func TestInventoryFingerprintPackingMatchesLeafDigest(t *testing.T) {
	f := newFixture(t)
	fp := localmesh.Fingerprint(f.creds[p535].Certificate.Certificate)
	m := inventoryMessages([]string{fp})[0]
	if len(m.Fingerprints) != 32 || hex.EncodeToString(m.Fingerprints) != fp {
		t.Fatal("fingerprint encoding changed")
	}
}
