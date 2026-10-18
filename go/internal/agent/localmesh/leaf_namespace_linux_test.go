//go:build linux

package localmesh

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/WendyOS/go/babel"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func dialTestLeaf(ctx context.Context, address string, leaf, router *Credentials) (*tls.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if err := writeTestLeafPreface(conn, leaf.Device); err != nil {
		return nil, err
	}
	config, err := leaf.PeerTLS(router.Device)
	if err != nil {
		return nil, err
	}
	config.NextProtos = []string{LeafALPN}
	secure := tls.Client(conn, config)
	if err := secure.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	if err := OpenStreamHelloWithALPN(ctx, secure, leaf.Device, router.Device, LeafALPN); err != nil {
		return nil, err
	}
	ok = true
	return secure, nil
}

// This endpoint uses the same Babel engine and stream framing as a leaf, with
// no kernel interface or forwarding capability. Only the Linux router creates
// TUNs/FIB entries; replies must cross its production kernel route to this link.
func runTestLeafEndpoint(ctx context.Context, link *streamNodeLink, leaf, router *Credentials, packets chan<- []byte) error {
	const routerID babel.RouterID = 0x1000000000042
	engine, err := babel.New(babel.Config{RouterID: routerID, HelloInterval: time.Second, UpdateInterval: 4 * time.Second,
		AcceptRoute: func(babel.RouterID, netip.Prefix) bool { return false }})
	if err != nil {
		return err
	}
	if err := link.WriteControl(ControlMessage{Kind: "bundle", Bundle: leaf.Certificate.Certificate}); err != nil {
		return err
	}
	var revision uint64
	lastSeq := uint16(0)
	started := time.Now()
	step := func(event babel.Event) error {
		effects, err := engine.Step(time.Since(started), event)
		if err != nil {
			return err
		}
		if effects.Revision != 0 {
			committed, err := engine.Commit(effects.Revision, true)
			if err != nil {
				return err
			}
			effects.Datagrams = append(effects.Datagrams, committed.Datagrams...)
		}
		seq := engine.Snapshot().Seqno
		if revision == 0 || seq != lastSeq {
			revision++
			now := time.Now()
			proof, err := SignManifest(Manifest{Version: ManifestVersionRoute, Device: leaf.Device.UUID(), Revision: revision,
				Issued: now.UnixMilli(), Expires: now.Add(time.Minute).UnixMilli(), AgentPort: 50052,
				Route: &RouteOrigin{Prefix: netip.PrefixFrom(leaf.Device.MeshAddress(), 128), RouterID: routerID, Seqno: seq}}, leaf.Certificate.Certificate, leaf.Signer, now)
			if err != nil {
				return err
			}
			if err := link.WriteControl(ControlMessage{Kind: "manifest", Manifest: &proof}); err != nil {
				return err
			}
			lastSeq = seq
		}
		for _, packet := range effects.Datagrams {
			if err := link.SendDatagram(append([]byte{PacketBabel}, packet.Payload...)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := step(babel.Originate{Prefix: netip.PrefixFrom(leaf.Device.MeshAddress(), 128)}); err != nil {
		return err
	}
	local, peer, err := meshid.LinkLocalAddresses(leaf.Device, router.Device)
	if err != nil {
		return err
	}
	if err := step(babel.AddLink{Link: babel.Link{ID: 1, Local: local, Peer: peer, Cost: 256, MaxPacket: DatagramLimit - 1}}); err != nil {
		return err
	}
	var assembler IPAssembler
	for ctx.Err() == nil {
		receive, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		packet, err := link.ReceiveDatagram(receive)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			if err := step(babel.Tick{}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if len(packet) == 0 {
			return errors.New("empty leaf packet")
		}
		if packet[0] == PacketBabel {
			if err := step(babel.Receive{ID: 1, Packet: packet[1:]}); err != nil {
				return err
			}
		} else if packet[0] == PacketIP {
			ip, err := assembler.Receive(packet, time.Now())
			if err != nil {
				return err
			}
			if ip != nil {
				select {
				case packets <- ip:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
	return ctx.Err()
}

func checksumLeafPacket(packet []byte, offset int) {
	// IPv6 pseudo-header plus the complete upper-layer body.
	pseudo := append([]byte(nil), packet[8:40]...)
	length := uint32(len(packet) - 40)
	pseudo = binary.BigEndian.AppendUint32(pseudo, length)
	pseudo = append(pseudo, 0, 0, 0, packet[6])
	pseudo = append(pseudo, packet[40:]...)
	if len(pseudo)%2 != 0 {
		pseudo = append(pseudo, 0)
	}
	var sum uint32
	for i := 0; i < len(pseudo); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(pseudo[i:]))
	}
	for sum>>16 != 0 {
		sum = (sum & 65535) + (sum >> 16)
	}
	checksum := ^uint16(sum)
	if checksum == 0 {
		checksum = 65535
	}
	binary.BigEndian.PutUint16(packet[40+offset:], checksum)
}

func TestLeafRoutedEndpointIsolatedNamespace(t *testing.T) {
	if os.Getenv("WENDY_LOCALMESH_ISOLATED_TEST") != "1" {
		t.Skip("disposable Linux network namespace with NET_ADMIN and TUN")
	}
	var identity TCPIdentity
	router, leaf := testCredentialsWithPEM(t, func(device meshid.Device, cert, chain, key string) {
		if device.UUID().String() == testDeviceAID {
			identity = TCPIdentity{Name: "leaf-router", AgentPort: 50052, Certificate: cert, Chain: chain, Key: key}
		}
	})
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	_ = reserved.Close()
	dir := t.TempDir()
	data, err := json.Marshal(TCPConfig{LeafListen: address})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tcpConfigName), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ready := make(chan *Node, 1)
	runtimeDone := make(chan error, 1)
	go func() {
		runtimeDone <- RunConfiguredWithNode(ctx, dir, identity, func(node *Node, _ *TCPConfig) {
			if node != nil {
				ready <- node
			}
		})
	}()
	stopped := false
	defer func() {
		cancel()
		if !stopped {
			<-runtimeDone
		}
	}()
	var node *Node
	select {
	case node = <-ready:
	case err := <-runtimeDone:
		stopped = true
		t.Fatalf("leaf runtime startup: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	conn, err := dialTestLeaf(ctx, address, leaf, router)
	if err != nil {
		t.Fatal(err)
	}
	link := newStreamNodeLink(conn)
	defer link.Close()
	controlDone := make(chan struct{})
	go func() {
		defer close(controlDone)
		for {
			if _, err := link.ReadControl(); err != nil {
				return
			}
		}
	}()
	packets := make(chan []byte, 8)
	endpointDone := make(chan error, 1)
	go func() { endpointDone <- runTestLeafEndpoint(ctx, link, leaf, router, packets) }()
	defer func() { _ = link.Close(); <-controlDone; <-endpointDone }()
	leafPrefix := netip.PrefixFrom(leaf.Device.MeshAddress(), 128)
	waitFor := func(description string, condition func() bool) {
		t.Helper()
		for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); {
			if condition() {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		t.Fatal(description)
	}
	waitFor("signed leaf route never installed", func() bool {
		for _, route := range node.Snapshot().Routes {
			if route.Prefix == leafPrefix && !route.Local && !route.Unreachable {
				return true
			}
		}
		return false
	})
	if len(node.Snapshot().Links) != 1 {
		t.Fatal("expected one admitted leaf link")
	}
	if peers := node.ControlHub().Peers(); len(peers) != 0 {
		t.Fatal("endpoint-only leaf registered as a catalogue peer")
	}
	// A second authenticated connection cannot replace the working leaf.
	duplicate, err := dialTestLeaf(ctx, address, leaf, router)
	if duplicate != nil {
		_ = duplicate.Close()
	}
	if err == nil || len(node.Snapshot().Links) != 1 {
		t.Fatal("duplicate leaf replaced or multiplied the active link")
	}
	udp, err := net.ListenUDP("udp6", &net.UDPAddr{IP: router.Device.MeshAddress().AsSlice(), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	request := leafTestPacket(leaf.Device.MeshAddress(), router.Device.MeshAddress(), 17, TunnelMTU)
	binary.BigEndian.PutUint16(request[40:42], 18089)
	binary.BigEndian.PutUint16(request[42:44], uint16(udp.LocalAddr().(*net.UDPAddr).Port))
	binary.BigEndian.PutUint16(request[44:46], uint16(len(request)-40))
	copy(request[48:], bytes.Repeat([]byte("leaf-mtu"), (TunnelMTU-48)/8))
	checksumLeafPacket(request, 6)
	if err := link.SendIP(1, request); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, TunnelMTU)
	size, source, err := udp.ReadFromUDP(buffer)
	if err != nil || !bytes.Equal(buffer[:size], request[48:]) || !source.IP.Equal(leaf.Device.MeshAddress().AsSlice()) {
		t.Fatalf("routed UDP input: size=%d source=%v err=%v", size, source, err)
	}
	if _, err := udp.WriteToUDP(buffer[:size], source); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-packets:
		if len(reply) != TunnelMTU || reply[6] != 17 || !bytes.Equal(reply[48:], request[48:]) || !bytes.Equal(reply[24:40], leaf.Device.MeshAddress().AsSlice()) {
			t.Fatal("kernel UDP reply did not follow leaf route")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no routed UDP reply")
	}
	echo := leafTestPacket(leaf.Device.MeshAddress(), router.Device.MeshAddress(), 58, 56)
	echo[40], echo[44], echo[46] = 128, 1, 1
	copy(echo[48:], []byte("leafping"))
	checksumLeafPacket(echo, 2)
	if err := link.SendIP(2, echo); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-packets:
		if reply[6] != 58 || reply[40] != 129 || !bytes.Equal(reply[48:], echo[48:]) {
			t.Fatal("invalid kernel ICMPv6 echo reply")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no routed ICMPv6 echo reply")
	}
	// A source spoof passes framing/reassembly but must close this leaf before
	// reaching the kernel. Its selected route then becomes unreachable.
	spoof := leafTestPacket(router.Device.MeshAddress(), router.Device.MeshAddress(), 17, 48)
	if err := link.SendIP(3, spoof); err != nil {
		t.Fatal(err)
	}
	waitFor("source spoof did not remove leaf", func() bool { return len(node.Snapshot().Links) == 0 })
	for _, route := range node.Snapshot().Routes {
		if route.Prefix == leafPrefix && !route.Unreachable {
			t.Fatal("disconnected leaf kept a usable route")
		}
	}
	cancel()
	if err := <-runtimeDone; err != nil {
		t.Fatal(err)
	}
	stopped = true
	if _, err := netlink.LinkByName(InterfaceName); err == nil {
		t.Fatal("shutdown retained mesh host interface")
	}
	links, err := netlink.LinkList()
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range links {
		if ownedInterface(link.Attrs().Name) {
			t.Fatalf("shutdown retained %s", link.Attrs().Name)
		}
	}
	if conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("shutdown retained leaf listener")
	}
	t.Log(fmt.Sprintf("signed leaf /128, UDP %d-byte MTU, ICMPv6, duplicate/source rejection and owned cleanup passed", TunnelMTU))
}
