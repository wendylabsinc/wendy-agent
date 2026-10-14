package liteclient

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"net"
	"testing"
	"time"
)

func TestBLEDialCancellationClosesLateConnection(t *testing.T) {
	previous := openBLEStreamFn
	t.Cleanup(func() { openBLEStreamFn = previous })
	started, release := make(chan struct{}), make(chan struct{})
	local, remote := net.Pipe()
	defer remote.Close()
	openBLEStreamFn = func(context.Context, string, uint16) (net.Conn, error) {
		close(started)
		<-release
		return local, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		_, err := dialBLEStream(ctx, "board", DefaultL2CAPPSM)
		completed <- err
	}()
	<-started
	cancel()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dial error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BLE setup ignored cancellation")
	}
	close(release)
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remote.Read(make([]byte, 1)); err == nil {
		t.Fatal("late connection remained open")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("late connection was not closed")
	}
}

func TestBLEHandshakeCancellation(t *testing.T) {
	previous := openBLEStreamFn
	t.Cleanup(func() { openBLEStreamFn = previous })
	local, remote := net.Pipe()
	defer remote.Close()
	openBLEStreamFn = func(context.Context, string, uint16) (net.Conn, error) {
		return local, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		completed <- NewWendyLiteClient().ConnectViaBLEContext(ctx, "board", DefaultL2CAPPSM, nil)
	}()
	// Receive the ClientHello, then leave the TLS server silent.
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remote.Read(make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-completed:
		if err == nil {
			t.Fatal("cancelled BLE handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("BLE TLS handshake ignored cancellation")
	}
}

func TestPKILANHandshakeCancellation(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111111"
	root, rootKey := issueRelayCertificate(t, 1, "root", "", true, nil, nil)
	operator, operatorKey := issueRelayCertificate(t, 2, "operator", "spiffe://wendy.sh/tenant/"+tenant+"/operator/22222222-2222-4222-8222-222222222222", false, root, rootKey)
	cert := tls.Certificate{Certificate: [][]byte{operator.Raw}, PrivateKey: operatorKey}
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		completed <- NewWendyLiteClient().ConnectWithPKIAuthenticationContext(ctx, listener.Addr().String(), cert, chain, "441bf6804ff8")
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handshake error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("LAN TLS handshake ignored cancellation")
	}
}
