//go:build linux

package localmesh

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	quic "github.com/quic-go/quic-go"
	"golang.org/x/sys/unix"
)

func TestLANUDPBindUsesSelectedInterface(t *testing.T) {
	selected := lanInterface{iface: net.Interface{Name: "lo"}, ip: net.IPv4(127, 0, 0, 1)}
	socket, err := listenLANQUIC(context.Background(), selected, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	udp, ok := socket.(*net.UDPConn)
	if !ok {
		t.Fatalf("production listener is not real UDP: %T", socket)
	}
	raw, err := udp.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var bound string
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		bound, sockErr = unix.GetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE)
	}); err != nil {
		t.Fatal(err)
	}
	if sockErr != nil || bound != "lo" {
		t.Fatalf("wrong interface binding: %q %v", bound, sockErr)
	}
	selected.iface.Name = "not-a-real-lan"
	if bad, err := listenLANQUIC(context.Background(), selected, "127.0.0.1:0"); err == nil {
		bad.Close()
		t.Fatal("invalid interface fell back to default routing")
	}
}

func TestLANUDPRejectedPeerRetainsWorkerSocket(t *testing.T) {
	a, b := testCredentials(t)
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	endpoint := socket.LocalAddr()
	mdnsConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer mdnsConn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	selected := lanInterface{iface: net.Interface{Name: "not-a-real-lan"}, ip: net.IPv4(127, 0, 0, 1), net: &net.IPNet{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)}, cost: EthernetLinkCost}
	claims := &lanClaims{active: make(map[lanClaim]bool)}
	done := make(chan error, 1)
	ready := make(chan CarrierStatus, 1)
	go func() {
		done <- serveLANInterface(ctx, LANConfig{Credentials: b, Node: &Node{Credentials: b, ctx: ctx}, Status: func(status CarrierStatus) { ready <- status }}, selected, claims, socket, mdnsConn, nil)
	}()
	select {
	case status := <-ready:
		if !status.Ready || status.Err != nil {
			t.Fatalf("native UDP setup readiness: %+v", status)
		}
	case <-ctx.Done():
		t.Fatal("native UDP setup did not report readiness")
	}
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	clientSocket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer clientSocket.Close()
	clientTransport := &quic.Transport{Conn: clientSocket}
	defer clientTransport.Close()
	// An already claimed identity/cost must be rejected after real mTLS,
	// without closing the endpoint used by the following independent attempts.
	if !claims.claim(a.Device, EthernetLinkCost) {
		t.Fatal("initial claim failed")
	}
	config, err := a.PeerTLS(b.Device)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := clientTransport.Dial(ctx, endpoint, config, QUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenControl(ctx, duplicate, a.Device, b.Device); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("duplicate not promptly rejected: %v", err)
	}
	duplicate.CloseWithError(0, "duplicate done")
	claims.release(a.Device, EthernetLinkCost)
	for trial := 0; trial < 2; trial++ {
		config, err := a.PeerTLS(b.Device)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := clientTransport.Dial(ctx, endpoint, config, QUICConfig())
		if err != nil {
			t.Fatalf("shared UDP endpoint unavailable on trial%d: %v", trial, err)
		}
		if _, err := OpenControl(ctx, conn, lanDevice(t, 444), b.Device); err == nil {
			t.Fatal("certificate445 admitted as hello444")
		}
		conn.CloseWithError(0, "rejected peer finished")
		deadline := time.Now().Add(time.Second)
		for {
			claims.mu.Lock()
			count := len(claims.active)
			claims.mu.Unlock()
			if count == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("rejected peer retained its duplicate claim")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestLANUDPRejectsCrossedInitiator(t *testing.T) {
	a, b := testCredentials(t)
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	mdnsConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer mdnsConn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	selected := lanInterface{iface: net.Interface{Name: "not-a-real-lan"}, ip: net.IPv4(127, 0, 0, 1), net: &net.IPNet{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)}, cost: EthernetLinkCost}
	claims := &lanClaims{active: make(map[lanClaim]bool)}
	done := make(chan error, 1)
	go func() {
		done <- serveLANInterface(ctx, LANConfig{Credentials: a, Node: &Node{Credentials: a, ctx: ctx}}, selected, claims, socket, mdnsConn, nil)
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client, err := b.PeerTLS(a.Device)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := quic.DialAddr(ctx, socket.LocalAddr().String(), client, QUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "crossed attempt done")
	if _, err := OpenControl(ctx, conn, b.Device, a.Device); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("higher identity initiated inbound link: %v", err)
	}
	claims.mu.Lock()
	defer claims.mu.Unlock()
	if len(claims.active) != 0 {
		t.Fatal("crossed initiator consumed active claim")
	}
}
