package clitimesync

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
	"golang.org/x/net/ipv4"
)

type fakeMulticastConn struct {
	interfaceName string
	ttl           int
	destination   string
	writes        int
	interfaceErr  error
	writeErr      error
}

func (c *fakeMulticastConn) SetMulticastInterface(iface *net.Interface) error {
	c.interfaceName = iface.Name
	return c.interfaceErr
}

func (c *fakeMulticastConn) SetMulticastTTL(ttl int) error {
	c.ttl = ttl
	return nil
}

func (c *fakeMulticastConn) WriteTo(_ []byte, _ *ipv4.ControlMessage, dst net.Addr) (int, error) {
	c.destination = dst.String()
	c.writes++
	return 1, c.writeErr
}

func (c *fakeMulticastConn) Close() error { return nil }

func TestFetchProofPacketMemoizes(t *testing.T) {
	calls := 0
	orig := roughtimeQueryFn
	roughtimeQueryFn = func(_ context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		calls++
		return roughtime.Result{Server: "cloudflare", Nonce: []byte("nonce"), RawResponse: []byte("resp")}, nil
	}
	t.Cleanup(func() { roughtimeQueryFn = orig; resetProofCache() })
	resetProofCache()

	pkt1, _, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("FetchProofPacket: %v", err)
	}
	pkt2, _, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("FetchProofPacket (2): %v", err)
	}
	if calls != 1 {
		t.Fatalf("roughtime query called %d times, want 1 (memoized)", calls)
	}
	if len(pkt1) == 0 || string(pkt1) != string(pkt2) {
		t.Fatal("expected identical non-empty packets across calls")
	}
}

func TestSendMulticastSelectsEveryActiveInterface(t *testing.T) {
	origInterfaces := listMulticastInterfaces
	origNewConn := newMulticastPacketConn
	t.Cleanup(func() {
		listMulticastInterfaces = origInterfaces
		newMulticastPacketConn = origNewConn
	})

	listMulticastInterfaces = func() ([]net.Interface, error) {
		return []net.Interface{
			{Name: "en0", Flags: net.FlagUp | net.FlagMulticast},
			{Name: "en60", Flags: net.FlagUp | net.FlagMulticast},
			{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback | net.FlagMulticast},
			{Name: "down0", Flags: net.FlagMulticast},
			{Name: "point-to-point", Flags: net.FlagUp},
		}, nil
	}

	var conns []*fakeMulticastConn
	newMulticastPacketConn = func() (multicastPacketConn, error) {
		conn := &fakeMulticastConn{}
		conns = append(conns, conn)
		return conn, nil
	}

	if err := sendMulticast([]byte("proof")); err != nil {
		t.Fatalf("sendMulticast: %v", err)
	}

	var gotInterfaces []string
	for _, conn := range conns {
		gotInterfaces = append(gotInterfaces, conn.interfaceName)
		if conn.ttl != multicastTTL {
			t.Errorf("%s TTL = %d, want %d", conn.interfaceName, conn.ttl, multicastTTL)
		}
		if conn.destination != multicastAddr {
			t.Errorf("%s destination = %q, want %q", conn.interfaceName, conn.destination, multicastAddr)
		}
		if conn.writes != 1 {
			t.Errorf("%s writes = %d, want 1", conn.interfaceName, conn.writes)
		}
	}
	if want := []string{"en0", "en60"}; !reflect.DeepEqual(gotInterfaces, want) {
		t.Fatalf("selected interfaces = %v, want %v", gotInterfaces, want)
	}
}

func TestSendMulticastRequiresOneSuccessfulWrite(t *testing.T) {
	origInterfaces := listMulticastInterfaces
	origNewConn := newMulticastPacketConn
	t.Cleanup(func() {
		listMulticastInterfaces = origInterfaces
		newMulticastPacketConn = origNewConn
	})

	listMulticastInterfaces = func() ([]net.Interface, error) {
		return []net.Interface{{Name: "en60", Flags: net.FlagUp | net.FlagMulticast}}, nil
	}
	newMulticastPacketConn = func() (multicastPacketConn, error) {
		return &fakeMulticastConn{writeErr: errors.New("network unreachable")}, nil
	}

	err := sendMulticast([]byte("proof"))
	if err == nil {
		t.Fatal("sendMulticast succeeded without a successful write")
	}
	if got := err.Error(); got != "broadcasting time proof: en60: sending multicast packet: network unreachable" {
		t.Fatalf("sendMulticast error = %q", got)
	}
}

func TestSendMulticastContinuesAfterInterfaceFailure(t *testing.T) {
	origInterfaces := listMulticastInterfaces
	origNewConn := newMulticastPacketConn
	t.Cleanup(func() {
		listMulticastInterfaces = origInterfaces
		newMulticastPacketConn = origNewConn
	})

	listMulticastInterfaces = func() ([]net.Interface, error) {
		return []net.Interface{
			{Name: "en0", Flags: net.FlagUp | net.FlagMulticast},
			{Name: "en60", Flags: net.FlagUp | net.FlagMulticast},
		}, nil
	}

	var conns []*fakeMulticastConn
	newMulticastPacketConn = func() (multicastPacketConn, error) {
		conn := &fakeMulticastConn{}
		if len(conns) == 0 {
			conn.interfaceErr = errors.New("unsupported interface")
		}
		conns = append(conns, conn)
		return conn, nil
	}

	if err := sendMulticast([]byte("proof")); err != nil {
		t.Fatalf("sendMulticast failed despite en60 succeeding: %v", err)
	}
	if len(conns) != 2 {
		t.Fatalf("opened %d connections, want 2", len(conns))
	}
	if conns[0].writes != 0 {
		t.Errorf("failed en0 writes = %d, want 0", conns[0].writes)
	}
	if conns[1].interfaceName != "en60" || conns[1].writes != 1 {
		t.Errorf("en60 connection = {interface: %q, writes: %d}, want {interface: en60, writes: 1}", conns[1].interfaceName, conns[1].writes)
	}
}
