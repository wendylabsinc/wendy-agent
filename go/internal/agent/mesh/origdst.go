package mesh

import (
	"encoding/binary"
	"net/netip"
)

// addrPortFromSockaddrIn decodes a raw struct sockaddr_in as returned by
// getsockopt(SO_ORIGINAL_DST): bytes [2:4] are the port in network order,
// [4:8] the IPv4 address. Callers today always pass a 16-byte array, but a
// length guard is cheap insurance against a short/malformed buffer; it
// returns the zero netip.AddrPort in that case rather than panicking.
func addrPortFromSockaddrIn(b []byte) netip.AddrPort {
	if len(b) < 8 {
		return netip.AddrPort{}
	}
	port := binary.BigEndian.Uint16(b[2:4])
	var ip [4]byte
	copy(ip[:], b[4:8])
	return netip.AddrPortFrom(netip.AddrFrom4(ip), port)
}
