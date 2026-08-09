package services

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// stubPeerDialer stands in for MeshDialer so the proxy can be tested without a
// broker or a real peer.
type stubPeerDialer struct {
	addr string // when set, dial this instead of a mesh peer
	err  error
}

func (d stubPeerDialer) DialDevice(_ context.Context, _ int32, _ uint16) (net.Conn, string, error) {
	if d.err != nil {
		return nil, "", d.err
	}
	c, err := net.Dial("tcp", d.addr)
	if err != nil {
		return nil, "", err
	}
	return c, "lan-direct", nil
}

func testTarget() *agentpbv2.PushTarget {
	return &agentpbv2.PushTarget{AssetId: 214, RegistryPort: 5000, Repository: "myapp:latest"}
}

// A push that cannot reach the target must surface WHY. Without this the
// failure reaches buildkit as a bare "connection reset by peer" on loopback,
// which says nothing about whether the mesh is unreachable or the registry
// rejected our certificate — the two causes with completely different fixes.
func TestPushProxy_SurfacesDialFailure(t *testing.T) {
	dialer := stubPeerDialer{err: errors.New("no route to peer")}

	proxy, err := startPushProxy(context.Background(), dialer, testTarget(), &tls.Config{MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("startPushProxy: %v", err)
	}
	defer proxy.stop()

	conn, err := net.Dial("tcp", proxy.addr)
	if err != nil {
		t.Fatalf("dialing the proxy: %v", err)
	}
	// Drive one request through so the proxy attempts its outbound dial.
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write([]byte("HEAD /v2/ HTTP/1.1\r\nHost: x\r\n\r\n"))
	io.Copy(io.Discard, conn)
	conn.Close()

	if got := proxy.firstError(); got == nil {
		t.Fatal("the proxy must record why the outbound dial failed, not discard it")
	}
}

// A client that finishes sending and half-closes must still receive the whole
// response. Tearing both connections down as soon as ONE direction finishes
// truncates the reply, which reaches the client as "connection reset by peer" —
// the failure buildkit hit on every concurrent push connection.
func TestPushProxy_DeliversResponseAfterClientHalfClose(t *testing.T) {
	const reply = "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer backend.Close()
	go func() {
		c, aerr := backend.Accept()
		if aerr != nil {
			return
		}
		defer c.Close()
		io.Copy(io.Discard, c) // read until the client half-closes
		c.Write([]byte(reply))
	}()

	proxy, err := newPushProxy(stubPeerDialer{addr: backend.Addr().String()}, testTarget(), nil)
	if err != nil {
		t.Fatalf("newPushProxy: %v", err)
	}
	defer proxy.stop()
	// Plain TCP for the backend hop; the TLS wrapping is orthogonal to relaying.
	// Set before serve, so no accepting goroutine can be reading dial yet.
	proxy.dial = func(ctx context.Context) (net.Conn, error) {
		return net.Dial("tcp", backend.Addr().String())
	}
	proxy.serve(context.Background())

	conn, err := net.Dial("tcp", proxy.addr)
	if err != nil {
		t.Fatalf("dialing proxy: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	conn.Write([]byte("HEAD /v2/x/blobs/sha256:deadbeef HTTP/1.1\r\nHost: x\r\n\r\n"))
	conn.(*net.TCPConn).CloseWrite()

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("reading response through the proxy: %v", err)
	}
	if string(got) != reply {
		t.Fatalf("response truncated through the proxy:\n got: %q\nwant: %q", got, reply)
	}
}

func TestPushProxy_NoErrorWhenNothingConnects(t *testing.T) {
	proxy, err := startPushProxy(context.Background(),
		stubPeerDialer{addr: "127.0.0.1:1"}, testTarget(), &tls.Config{MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("startPushProxy: %v", err)
	}
	defer proxy.stop()

	if got := proxy.firstError(); got != nil {
		t.Fatalf("a proxy nothing dialed must report no error, got: %v", got)
	}
}

func TestValidatePushTarget_AcceptsMeshPeer(t *testing.T) {
	if err := validatePushTarget(&agentpbv2.PushTarget{
		AssetId: 214, RegistryPort: 5000, Repository: "myapp:latest",
	}); err != nil {
		t.Fatalf("validatePushTarget: %v", err)
	}
}

func TestValidatePushTarget_RejectsMissingTarget(t *testing.T) {
	if err := validatePushTarget(nil); err == nil {
		t.Fatal("a spec with no push target must be rejected")
	}
}

func TestValidatePushTarget_RejectsBadAssetID(t *testing.T) {
	if err := validatePushTarget(&agentpbv2.PushTarget{
		AssetId: 0, RegistryPort: 5000, Repository: "a:latest",
	}); err == nil {
		t.Fatal("a non-positive asset id must be rejected")
	}
}

func TestValidatePushTarget_RejectsBadPort(t *testing.T) {
	if err := validatePushTarget(&agentpbv2.PushTarget{
		AssetId: 214, RegistryPort: 0, Repository: "a:latest",
	}); err == nil {
		t.Fatal("a zero registry port must be rejected")
	}
}

// A slash would make the first element a registry host once joined to the proxy
// address, quietly redirecting the push somewhere else.
func TestValidatePushTarget_RejectsHostInRepository(t *testing.T) {
	if err := validatePushTarget(&agentpbv2.PushTarget{
		AssetId: 214, RegistryPort: 5000, Repository: "evil.example.com/a:latest",
	}); err == nil {
		t.Fatal("a repository containing a host component must be rejected")
	}
}
