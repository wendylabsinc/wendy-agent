//go:build linux

package localmesh

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/WendyOS/babel"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// TestKernelIsolatedNamespace exercises the real FIB: a device's own ULA address
// on the host link, a peer TUN addressed with the complementary link-local pair
// derived from the two device UUIDs, and a Babel route for the peer's ULA /128
// with a link-local next hop.
func TestKernelIsolatedNamespace(t *testing.T) {
	if os.Getenv("WENDY_LOCALMESH_ISOLATED_TEST") != "1" {
		t.Skip("requires disposable privileged container network namespace")
	}
	self, peer := testDevices(t)
	k, err := NewKernel(self)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := k.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err = k.AddPeer(1, self); err == nil {
		t.Fatal("kernel accepted a link to our own device principal")
	}
	tun, err := k.AddPeer(1, peer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = k.AddPeer(1, peer); err == nil {
		t.Fatal("kernel accepted a duplicate link incarnation")
	}
	selfLink, peerLink, err := meshid.LinkLocalAddresses(self, peer)
	if err != nil {
		t.Fatal(err)
	}
	if selfLink.String() != "fe80::1" || peerLink.String() != "fe80::2" {
		t.Fatalf("TUN addressing is not the UUID-ordered pair: %s / %s", selfLink, peerLink)
	}
	dst := netip.PrefixFrom(peer.MeshAddress(), 128)
	route := babel.Route{Prefix: dst, RouterID: babel.RouterIDFromRandom([6]byte{0x2b, 0x1c, 0, 0, 0x46, 0}), Link: 1, NextHop: peerLink, Metric: 256}
	if err = k.Apply([]babel.Route{route}); err != nil {
		t.Fatal(err)
	}
	got, err := netlink.RouteGet(peer.MeshAddress().AsSlice())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LinkIndex != k.peers[1].link.Index {
		t.Fatalf("wrong FIB: %v", got)
	}
	conn, err := net.DialUDP("udp6", nil, &net.UDPAddr{IP: peer.MeshAddress().AsSlice(), Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, TunnelMTU)
		for {
			n, err := tun.Read(buf)
			if err != nil {
				done <- nil
				return
			}
			// Only the test's IPv6 payload is application data.
			if n > 0 && buf[0]>>4 == 6 {
				done <- buf[:n]
				return
			}
		}
	}()
	if _, err = conn.Write([]byte("local-mesh-kernel-test")); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-done:
		if !validIP(packet) || packet[0]>>4 != 6 {
			t.Fatalf("invalid TUN output: %x", packet)
		}
		src, dstAddr, err := ipv6Addresses(packet)
		if err != nil {
			t.Fatal(err)
		}
		if src != self.MeshAddress() || dstAddr != peer.MeshAddress() {
			t.Fatalf("payload left with %s -> %s, want %s -> %s", src, dstAddr, self.MeshAddress(), peer.MeshAddress())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no IP packet delivered through TUN")
	}
	if err = k.Apply([]babel.Route{{Prefix: dst, Unreachable: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err = netlink.RouteGet(peer.MeshAddress().AsSlice()); err == nil {
		t.Fatal("unreachable FIB fell through")
	}
	if err = k.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if _, err = netlink.RouteGet(peer.MeshAddress().AsSlice()); err == nil {
		t.Fatal("empty table fell through to default")
	}
}

// TestKernelRefusesForeignTableOwnership runs in the same disposable namespace:
// a route already occupying the reserved table is never adopted.
func TestKernelRefusesForeignTableOwnership(t *testing.T) {
	if os.Getenv("WENDY_LOCALMESH_ISOLATED_TEST") != "1" {
		t.Skip("requires disposable privileged container network namespace")
	}
	self, _ := testDevices(t)
	foreign := netlink.Route{Table: RouteTable, Protocol: routeProtocol, Family: netlink.FAMILY_V6, Dst: ipNet(netip.MustParsePrefix("fd00::445:1/128")), LinkIndex: 1}
	if err := netlink.RouteReplace(&foreign); err != nil {
		t.Skipf("cannot plant a foreign route: %v", err)
	}
	defer netlink.RouteDel(&foreign)
	if _, err := NewKernel(self); err == nil {
		t.Fatal("kernel adopted a foreign route in the reserved table")
	}
}

// ipv6Addresses returns the source and destination of an IPv6 packet.
func ipv6Addresses(p []byte) (netip.Addr, netip.Addr, error) {
	if len(p) < 40 || p[0]>>4 != 6 {
		return netip.Addr{}, netip.Addr{}, errors.New("not an IPv6 packet")
	}
	src, ok := netip.AddrFromSlice(p[8:24])
	if !ok {
		return netip.Addr{}, netip.Addr{}, errors.New("invalid IPv6 source")
	}
	dst, ok := netip.AddrFromSlice(p[24:40])
	if !ok {
		return netip.Addr{}, netip.Addr{}, errors.New("invalid IPv6 destination")
	}
	return src.Unmap(), dst.Unmap(), nil
}
