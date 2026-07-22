package mesh

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestAddrPortFromSockaddrIn(t *testing.T) {
	// struct sockaddr_in: [0:2] family, [2:4] port (network order), [4:8] IPv4.
	var b [16]byte
	binary.LittleEndian.PutUint16(b[0:2], 2) // AF_INET
	binary.BigEndian.PutUint16(b[2:4], 8080)
	copy(b[4:8], []byte{10, 99, 0, 215})
	got := addrPortFromSockaddrIn(b[:])
	if got.String() != "10.99.0.215:8080" {
		t.Fatalf("got %s, want 10.99.0.215:8080", got)
	}
}

func TestAddrPortFromSockaddrInShortInput(t *testing.T) {
	for _, n := range []int{0, 1, 4, 7} {
		got := addrPortFromSockaddrIn(make([]byte, n))
		if got != (netip.AddrPort{}) {
			t.Fatalf("len(b)=%d: got %v, want zero netip.AddrPort", n, got)
		}
	}
}
