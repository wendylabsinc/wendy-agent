package localmesh

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestControlBoundAndFraming(t *testing.T) {
	var b bytes.Buffer
	m := ControlMessage{Kind: "identity-request", Principal: testPrincipal(testTenantUUID, testDeviceAID)}
	if err := WriteControl(&b, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadControl(&b)
	if err != nil || got.Kind != m.Kind || got.Principal != m.Principal {
		t.Fatal(got, err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxControlMessage+1)
	if _, err = ReadControl(bytes.NewReader(header[:])); err == nil {
		t.Fatal("oversize allocated")
	}
}

func TestDirectorySyncBundleOnceAndMissingBundleRecovery(t *testing.T) {
	source, chain, key, device, now := directoryFixture(t)
	m := Manifest{Version: 2, Device: device.UUID(), Revision: 1, Issued: now.UnixMilli(), Expires: now.Add(time.Minute).UnixMilli(), AgentPort: 50052}
	w, err := SignManifest(m, chain, key, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Accept(w, now); err != nil {
		t.Fatal(err)
	}
	cache, err := OpenIdentityCache("", DefaultCacheLimits(), source.cache.verify, now)
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := NewDirectory(testTenant(t), 10, cache)
	sender, receiver := NewSynchronizer(source, source.cache), NewSynchronizer(dest, cache)
	msgs := sender.Reconcile(now)
	if len(msgs) != 2 || msgs[0].Kind != "bundle" || msgs[1].Kind != "manifest" {
		t.Fatal(msgs)
	}
	// Deliberately omit the proactively sent bundle.
	replies, changed, err := receiver.Receive(msgs[1], now)
	if err != nil || len(changed) != 0 || len(replies) != 1 || replies[0].Kind != "identity-request" {
		t.Fatal(replies, changed, err)
	}
	bundles, _, err := sender.Receive(replies[0], now)
	if err != nil || len(bundles) != 1 {
		t.Fatal(err)
	}
	_, changed, err = receiver.Receive(bundles[0], now)
	if err != nil || len(changed) != 1 || len(dest.Snapshot(now)) != 1 {
		t.Fatal("bundle recovery failed", err)
	}
	if len(sender.Reconcile(now)) != 1 {
		t.Fatal("certificate repeated unnecessarily")
	}
	_, changed, err = receiver.Receive(msgs[1], now)
	if err != nil || len(changed) != 0 {
		t.Fatal("duplicate re-flooded", err)
	}
}

// TestSyncIdentityRequestIsPrincipalScoped covers the two ways a neighbour can
// ask for a certificate: a principal this node holds is answered, anything
// else - an unknown device, or the same device UUID under another tenant - is
// not, and a manifest from another tenant ends the link rather than being
// relayed as a hint.
func TestSyncIdentityRequestIsPrincipalScoped(t *testing.T) {
	source, chain, key, device, now := directoryFixture(t)
	w, err := SignManifest(Manifest{Version: 2, Device: device.UUID(), Revision: 1, Issued: now.UnixMilli(), Expires: now.Add(time.Minute).UnixMilli(), AgentPort: 50052}, chain, key, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Accept(w, now); err != nil {
		t.Fatal(err)
	}
	cache, err := OpenIdentityCache("", DefaultCacheLimits(), source.cache.verify, now)
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := NewDirectory(testTenant(t), 10, cache)
	sender, receiver := NewSynchronizer(source, source.cache), NewSynchronizer(dest, cache)
	replies, _, err := receiver.Receive(ControlMessage{Kind: "identity-request", Principal: device.Principal()}, now)
	if err != nil || len(replies) != 0 {
		t.Fatal("receiver answered a bundle it does not hold", err)
	}
	replies, _, err = sender.Receive(ControlMessage{Kind: "identity-request", Principal: device.Principal()}, now)
	if err != nil || len(replies) != 1 || replies[0].Kind != "bundle" {
		t.Fatal("known principal unanswered", err)
	}
	replies, _, err = sender.Receive(ControlMessage{Kind: "identity-request", Principal: testPrincipal(testOtherTenant, testDeviceAID)}, now)
	if err != nil || len(replies) != 0 {
		t.Fatal("principal outside the tenant was answered", err)
	}
	foreign := w
	foreign.Principal = testPrincipal(testOtherTenant, testDeviceAID)
	if _, _, err = receiver.Receive(ControlMessage{Kind: "manifest", Manifest: &foreign}, now); err == nil {
		t.Fatal("manifest from another tenant relayed without error")
	}
}
