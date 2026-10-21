//go:build js && wasm

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/wendylabsinc/wendy/go/internal/cli/browserauth"
	"io"
	"net"
	"strings"
	"testing"
)

func TestRejectsUICertificateConnections(t *testing.T) {
	for _, params := range []json.RawMessage{
		nil,
		json.RawMessage(`{"relay":"wss://untrusted.example/tunnel","certificate":{"pemPrivateKey":"test-only-key","organizationId":1},"assetId":1}`),
	} {
		b := &bridge{}
		value, err := b.handle(request{Method: "connect", Params: params})
		if value != nil || err == nil || !strings.Contains(err.Error(), "Direct certificate connections are disabled") {
			t.Fatalf("legacy connection must be rejected: value=%v, err=%v", value, err)
		}
		if b.conn != nil || b.life != nil {
			t.Fatal("legacy connection created a device session")
		}
	}
}

type recordingConn struct {
	net.Conn
	writes [][]byte
	short  bool
}

func (c *recordingConn) Write(p []byte) (int, error) {
	if c.short {
		return 1, nil
	}
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}
func TestHostedTransportBoundsMessages(t *testing.T) {
	wire := &recordingConn{}
	conn := &browserTransportConn{Conn: wire}
	payload := bytes.Repeat([]byte("w"), (2<<20)+11)
	n, err := conn.Write(payload)
	if err != nil || n != len(payload) || len(wire.writes) != 3 {
		t.Fatalf("write: %d %v, frames %d", n, err, len(wire.writes))
	}
	for _, frame := range wire.writes {
		if len(frame) > 1<<20 {
			t.Fatal("oversized frame")
		}
	}
	if !bytes.Equal(bytes.Join(wire.writes, nil), payload) {
		t.Fatal("byte stream changed")
	}
	wire.short = true
	if n, err := conn.Write(payload); n != 1 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %d %v", n, err)
	}
}
func TestHostedDialRejectsUnexpectedTargetBeforeNetwork(t *testing.T) {
	settings := browserauth.Settings{
		AuthBase: "https://auth.example", ClientID: "cloud-login",
		IdentityResource: "https://pki.example/identity", IdentityEndpoint: "https://identity.example/v1/identity/certificate",
		CloudResource: "https://cloud.example/api", CloudGRPC: "api.example:443", RelayIssuer: "https://api.example",
	}
	b := &bridge{auth: &browserauth.Session{Settings: &settings}}
	if conn, err := b.browserCloudDial(context.Background(), "localhost:443"); err == nil || conn != nil {
		t.Fatal("unexpected target accepted")
	}
}
