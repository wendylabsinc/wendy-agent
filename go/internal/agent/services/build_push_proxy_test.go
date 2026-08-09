package services

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// A push that cannot reach the target must surface WHY. Without this the
// failure reaches buildkit as a bare "connection reset by peer" on loopback,
// which says nothing about whether the mesh is unreachable or the registry
// rejected our certificate — the two causes with completely different fixes.
func TestPushProxy_SurfacesDialFailure(t *testing.T) {
	// A port with nothing on it: the outbound dial must fail.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadAddr := closed.Addr().String()
	closed.Close()

	proxy, err := startPushProxy(context.Background(), deadAddr, &tls.Config{MinVersion: tls.VersionTLS12})
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

	proxy, err := startPushProxy(context.Background(), backend.Addr().String(), nil)
	if err != nil {
		t.Fatalf("startPushProxy: %v", err)
	}
	defer proxy.stop()
	// Plain TCP for the backend hop; the TLS wrapping is orthogonal to relaying.
	proxy.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		return net.Dial("tcp", addr)
	}

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
	proxy, err := startPushProxy(context.Background(), "127.0.0.1:1", &tls.Config{MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("startPushProxy: %v", err)
	}
	defer proxy.stop()

	if got := proxy.firstError(); got != nil {
		t.Fatalf("a proxy nothing dialed must report no error, got: %v", got)
	}
}

func TestValidatePushReference_AcceptsMeshRegistryForm(t *testing.T) {
	host, port, repoTag, err := validatePushReference("robot-01.acme.cloud.wendy.dev:5000/myapp:latest")
	if err != nil {
		t.Fatalf("validatePushReference: %v", err)
	}
	if host != "robot-01.acme.cloud.wendy.dev" {
		t.Errorf("host = %q", host)
	}
	if port != 5000 {
		t.Errorf("port = %d", port)
	}
	if repoTag != "myapp:latest" {
		t.Errorf("repoTag = %q", repoTag)
	}
}

// Without this, BuildImage doubles as "push an image anywhere", authenticated
// by the build host's own credentials.
func TestValidatePushReference_RejectsArbitraryRegistry(t *testing.T) {
	_, _, _, err := validatePushReference("evil.example.com:443/exfil:latest")
	if err == nil {
		t.Fatal("an arbitrary registry must be rejected")
	}
	if !strings.Contains(err.Error(), "evil.example.com") {
		t.Fatalf("error should name the rejected host, got: %v", err)
	}
}

// A suffix check that matched substrings would accept this.
func TestValidatePushReference_RejectsSuffixLookalike(t *testing.T) {
	if _, _, _, err := validatePushReference("evil-cloud.wendy.dev.attacker.com:5000/a:latest"); err == nil {
		t.Fatal("a host merely containing the mesh domain must be rejected")
	}
}

func TestValidatePushReference_RejectsMissingPort(t *testing.T) {
	if _, _, _, err := validatePushReference("robot-01.acme.cloud.wendy.dev/myapp:latest"); err == nil {
		t.Fatal("a reference without an explicit registry port must be rejected")
	}
}

func TestValidatePushReference_RejectsMissingRepo(t *testing.T) {
	if _, _, _, err := validatePushReference("robot-01.acme.cloud.wendy.dev:5000"); err == nil {
		t.Fatal("a reference with no repository must be rejected")
	}
}

func TestValidatePushReference_RejectsBadPort(t *testing.T) {
	if _, _, _, err := validatePushReference("robot-01.acme.cloud.wendy.dev:99999/a:latest"); err == nil {
		t.Fatal("an out-of-range port must be rejected")
	}
}
