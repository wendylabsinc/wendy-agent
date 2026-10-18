package localmesh

import "testing"

func TestRouteProofProtocolRejectsLegacyHello(t *testing.T) {
	self, peer := testDevices(t)
	hello := LinkHello{Version: LinkProtocolVersion, Device: peer.UUID(), Peer: self.UUID(), MTU: TunnelMTU, Datagram: DatagramLimit}
	if !validLinkHello(&hello, self, peer) {
		t.Fatal("current route-proof peer rejected")
	}
	hello.Version = 1
	if validLinkHello(&hello, self, peer) {
		t.Fatal("legacy peer without route-proof policy admitted")
	}
	if validLinkHello(nil, self, peer) {
		t.Fatal("missing hello admitted")
	}
}
