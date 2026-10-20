//go:build linux

package localmesh

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestTCPAcceptPreservesHandshakeFailure(t *testing.T) {
	a, b := testCredentials(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	inbound, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer inbound.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// The connected peer sends no QUIC handshake. A nil Node must never be
	// reached, and the original accept failure must remain available to callers.
	err = attachTCP(ctx, nil, a, b.Device, inbound, true)
	if err == nil || errors.Unwrap(err) == nil {
		t.Fatalf("accept failure lost its cause: %v", err)
	}
}
