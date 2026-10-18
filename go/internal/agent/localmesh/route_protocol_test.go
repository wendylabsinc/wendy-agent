package localmesh

import "testing"

func TestRouteProofProtocolRejectsLegacyHello(t *testing.T) {
	self, peer := testDevices(t)
	hello := LinkHello{Version: LinkProtocolVersion, Device: peer.UUID(), Peer: self.UUID(), MTU: TunnelMTU, Datagram: DatagramLimit, IPDatagram: WholeIPDatagramLimit}
	if !validRouterHello(&hello, self, peer) {
		t.Fatal("current route-proof peer rejected")
	}
	hello.Version = 1
	if validRouterHello(&hello, self, peer) {
		t.Fatal("legacy peer without route-proof policy admitted")
	}
	if validRouterHello(nil, self, peer) {
		t.Fatal("missing hello admitted")
	}
}

func TestRouteProofStreamRejectsLegacyHello(t *testing.T) {
	self, peer := testDevices(t)
	hello := LinkHello{Version: StreamProtocolVersion, Device: peer.UUID(), Peer: self.UUID(), MTU: TunnelMTU, Datagram: DatagramLimit}
	if !validStreamHelloVersion(&hello, self, peer, StreamProtocolVersion) {
		t.Fatal("current route-proof stream rejected")
	}
	for _, version := range []int{1, 2} {
		hello.Version = version
		if validStreamHelloVersion(&hello, self, peer, StreamProtocolVersion) {
			t.Fatalf("legacy full-principal stream version %d admitted", version)
		}
	}
}
