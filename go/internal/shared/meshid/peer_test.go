package meshid

import (
	"strings"
	"testing"
)

func TestOperatorPeerKeepsPrincipalSeparate(t *testing.T) {
	principal := "spiffe://wendy.sh/tenant/11111111-1111-4111-8111-111111111111/operator/auth0|alice"
	peer, err := PeerFromPrincipal(principal)
	if err != nil {
		t.Fatal(err)
	}
	if !peer.IsOperator() || peer.Principal() != principal || !InMesh(peer.MeshAddress()) {
		t.Fatalf("invalid peer: %+v", peer)
	}
	again, _ := PeerFromPrincipal(principal)
	if peer != again {
		t.Fatal("unstable address")
	}
	if _, err = DeviceFromPrincipal(principal); err == nil {
		t.Fatal("operator accepted as device")
	}
	device := NewDevice(peer.Tenant(), peer.UUID())
	if device == peer || device.Principal() == peer.Principal() {
		t.Fatal("operator aliases device identity")
	}
	other, _ := PeerFromPrincipal(strings.Replace(principal, "alice", "bob", 1))
	if other.MeshAddress() == peer.MeshAddress() {
		t.Fatal("operator addresses collide")
	}
	for _, bad := range []string{strings.Replace(principal, "operator/", "service/", 1), "urn:wendy:org:1:user:alice", strings.Replace(principal, "11111111-1111-4111-8111-111111111111", "not-a-tenant", 1)} {
		if _, err := PeerFromPrincipal(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
