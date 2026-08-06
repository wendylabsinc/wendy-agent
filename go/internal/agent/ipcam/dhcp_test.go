package ipcam

import (
	"errors"
	"net"
	"testing"
	"time"
)

func testMAC(t *testing.T) net.HardwareAddr {
	t.Helper()
	mac, err := net.ParseMAC("ec:71:db:2a:ae:7e")
	if err != nil {
		t.Fatalf("parse mac: %v", err)
	}
	return mac
}

// A DISCOVER we build must parse back to the same thing, which exercises both
// sides of the wire format.
func TestDiscoverRoundTrip(t *testing.T) {
	mac := testMAC(t)
	got, err := ParsePacket(BuildDiscover(0x2419d68a, mac, "RLC-520A"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Type != Discover {
		t.Fatalf("type = %v, want DISCOVER", got.Type)
	}
	if got.XID != 0x2419d68a {
		t.Fatalf("xid = %#x", got.XID)
	}
	if got.CHAddr.String() != mac.String() {
		t.Fatalf("chaddr = %s, want %s", got.CHAddr, mac)
	}
	// The camera announcing its model is how the registry learns a name before
	// any ONVIF probe succeeds.
	if got.Hostname != "RLC-520A" {
		t.Fatalf("hostname = %q, want RLC-520A", got.Hostname)
	}
	if !got.Broadcast() {
		t.Fatal("broadcast flag lost; a client with no address cannot be unicast to")
	}
}

// Port 67 carries more than DHCP, and a truncated or hostile packet must not be
// able to drive a read past the buffer.
func TestParsePacketRejectsNonDHCP(t *testing.T) {
	mac := testMAC(t)
	valid := BuildDiscover(1, mac, "cam")

	cases := map[string][]byte{
		"empty":      {},
		"short":      make([]byte, 100),
		"no cookie":  make([]byte, dhcpMinLen+4),
		"truncated":  valid[:dhcpMinLen],
		"bad hwtype": corrupt(valid, 1, 99),
		"bad hwlen":  corrupt(valid, 2, 99),
		"no message type": append(append([]byte(nil), valid[:dhcpMinLen]...),
			optHostname, 3, 'c', 'a', 'm', optEnd),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePacket(payload); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func corrupt(src []byte, index int, value byte) []byte {
	out := append([]byte(nil), src...)
	out[index] = value
	return out
}

// An option whose length runs past the end of the datagram must stop parsing
// rather than panic.
func TestParsePacketTruncatedOptionDoesNotPanic(t *testing.T) {
	mac := testMAC(t)
	valid := BuildDiscover(1, mac, "cam")
	// Keep the message type, then append an option claiming more bytes than remain.
	truncated := append(append([]byte(nil), valid[:dhcpMinLen]...),
		optMessageType, 1, byte(Discover), optRequestedIP, 4, 10, 98)
	got, err := ParsePacket(truncated)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.RequestedIP != nil {
		t.Fatalf("RequestedIP = %v, want nil for a truncated option", got.RequestedIP)
	}
}

func TestBuildReplyOffer(t *testing.T) {
	mac := testMAC(t)
	req, err := ParsePacket(BuildDiscover(0xdeadbeef, mac, "RLC-520A"))
	if err != nil {
		t.Fatalf("parse discover: %v", err)
	}
	reply := BuildReply(req, Offer, ReplyConfig{
		ServerIP:  net.IPv4(10, 98, 0, 1),
		ClientIP:  net.IPv4(10, 98, 0, 50),
		Mask:      net.CIDRMask(24, 32),
		Broadcast: net.IPv4(10, 98, 0, 255),
		Lease:     12 * time.Hour,
	})

	got, err := ParsePacket(reply)
	if err != nil {
		t.Fatalf("parse reply: %v", err)
	}
	if got.Op != bootReply {
		t.Fatalf("op = %d, want a reply", got.Op)
	}
	if got.Type != Offer {
		t.Fatalf("type = %v, want OFFER", got.Type)
	}
	if got.XID != 0xdeadbeef {
		t.Fatalf("xid = %#x, want the request's", got.XID)
	}
	if !got.YIAddr.Equal(net.IPv4(10, 98, 0, 50)) {
		t.Fatalf("yiaddr = %v, want the offered address", got.YIAddr)
	}
	if !got.ServerID.Equal(net.IPv4(10, 98, 0, 1)) {
		t.Fatalf("server id = %v", got.ServerID)
	}
	if got.CHAddr.String() != mac.String() {
		t.Fatalf("chaddr = %s, want the client's", got.CHAddr)
	}
	if !got.Broadcast() {
		t.Fatal("reply dropped the broadcast flag")
	}
}

func TestBuildReplyAck(t *testing.T) {
	mac := testMAC(t)
	req, err := ParsePacket(BuildDiscover(1, mac, "cam"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	reply := BuildReply(req, Ack, ReplyConfig{
		ServerIP: net.IPv4(10, 98, 0, 1),
		ClientIP: net.IPv4(10, 98, 0, 50),
		Mask:     net.CIDRMask(24, 32),
		Lease:    time.Hour,
	})
	got, err := ParsePacket(reply)
	if err != nil {
		t.Fatalf("parse reply: %v", err)
	}
	if got.Type != Ack {
		t.Fatalf("type = %v, want ACK", got.Type)
	}
}

func TestMessageTypeString(t *testing.T) {
	if got := Discover.String(); got != "DISCOVER" {
		t.Fatalf("Discover.String() = %q", got)
	}
	if got := MessageType(99).String(); got != "type(99)" {
		t.Fatalf("unknown type = %q", got)
	}
}

// A camera must keep its address across renewals, or its address changes under
// anything holding a stream open.
func TestLeasePoolIsStableForAMAC(t *testing.T) {
	pool := NewLeasePool(net.IPv4(10, 98, 0, 50), 100)
	mac := testMAC(t)
	first, err := pool.Lease(mac, nil)
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	second, err := pool.Lease(mac, nil)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if !first.Equal(second) {
		t.Fatalf("address changed across leases: %v then %v", first, second)
	}
	if !first.Equal(net.IPv4(10, 98, 0, 50)) {
		t.Fatalf("first address = %v, want the base", first)
	}
}

func TestLeasePoolDistinctMACs(t *testing.T) {
	pool := NewLeasePool(net.IPv4(10, 98, 0, 50), 100)
	a, _ := pool.Lease(net.HardwareAddr{1, 1, 1, 1, 1, 1}, nil)
	b, _ := pool.Lease(net.HardwareAddr{2, 2, 2, 2, 2, 2}, nil)
	if a.Equal(b) {
		t.Fatalf("two MACs got the same address %v", a)
	}
	if !b.Equal(net.IPv4(10, 98, 0, 51)) {
		t.Fatalf("second address = %v, want the next in the pool", b)
	}
}

// Option 50 is how a camera asks to keep the address it had before a reboot.
func TestLeasePoolHonoursPreferredAddress(t *testing.T) {
	pool := NewLeasePool(net.IPv4(10, 98, 0, 50), 100)
	got, err := pool.Lease(testMAC(t), net.IPv4(10, 98, 0, 77))
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if !got.Equal(net.IPv4(10, 98, 0, 77)) {
		t.Fatalf("address = %v, want the requested one", got)
	}
}

// A preferred address already held by someone else, or outside the pool, falls
// back to a fresh allocation instead of colliding.
func TestLeasePoolRejectsUnusablePreference(t *testing.T) {
	pool := NewLeasePool(net.IPv4(10, 98, 0, 50), 10)
	other := net.HardwareAddr{9, 9, 9, 9, 9, 9}
	if _, err := pool.Lease(other, net.IPv4(10, 98, 0, 55)); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	taken, err := pool.Lease(testMAC(t), net.IPv4(10, 98, 0, 55))
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if taken.Equal(net.IPv4(10, 98, 0, 55)) {
		t.Fatal("handed out an address another MAC already holds")
	}

	outside, err := pool.Lease(net.HardwareAddr{3, 3, 3, 3, 3, 3}, net.IPv4(192, 168, 0, 9))
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if outside.Equal(net.IPv4(192, 168, 0, 9)) {
		t.Fatal("handed out an address from outside the pool")
	}
}

func TestLeasePoolExhaustion(t *testing.T) {
	pool := NewLeasePool(net.IPv4(10, 98, 0, 50), 2)
	if _, err := pool.Lease(net.HardwareAddr{1, 1, 1, 1, 1, 1}, nil); err != nil {
		t.Fatalf("lease 1: %v", err)
	}
	if _, err := pool.Lease(net.HardwareAddr{2, 2, 2, 2, 2, 2}, nil); err != nil {
		t.Fatalf("lease 2: %v", err)
	}
	if _, err := pool.Lease(net.HardwareAddr{3, 3, 3, 3, 3, 3}, nil); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("err = %v, want ErrPoolExhausted", err)
	}
}

func TestLeasePoolReleaseFreesAddress(t *testing.T) {
	pool := NewLeasePool(net.IPv4(10, 98, 0, 50), 1)
	mac := net.HardwareAddr{1, 1, 1, 1, 1, 1}
	if _, err := pool.Lease(mac, nil); err != nil {
		t.Fatalf("lease: %v", err)
	}
	pool.Release(mac)
	if _, err := pool.Lease(net.HardwareAddr{2, 2, 2, 2, 2, 2}, nil); err != nil {
		t.Fatalf("lease after release: %v", err)
	}
}

func TestLeasePoolHolderAndLeases(t *testing.T) {
	pool := NewLeasePool(net.IPv4(10, 98, 0, 50), 10)
	mac := testMAC(t)
	addr, err := pool.Lease(mac, nil)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	holder, ok := pool.Holder(addr)
	if !ok || holder != mac.String() {
		t.Fatalf("Holder(%v) = %q, %v", addr, holder, ok)
	}
	if got := pool.Leases(); len(got) != 1 || !got[mac.String()].Equal(addr) {
		t.Fatalf("Leases() = %v", got)
	}
}
