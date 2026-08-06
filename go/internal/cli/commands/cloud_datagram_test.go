package commands

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func TestParseTunnelArgUDPSuffix(t *testing.T) {
	cases := []struct {
		arg           string
		local, remote uint32
		udp           bool
		wantErr       bool
	}{
		{"8080:80", 8080, 80, false, false},
		{"5000:5000/udp", 5000, 5000, true, false},
		{"5000/udp", 5000, 5000, true, false},
		{"9000", 9000, 9000, false, false},
		{"5000/tcp", 5000, 5000, false, false},
		{"5000/quic", 0, 0, false, true},
		{"0:80/udp", 0, 0, false, true},
	}
	for _, c := range cases {
		l, r, udp, err := parseTunnelArg(c.arg)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.arg)
			}
			continue
		}
		if err != nil || l != c.local || r != c.remote || udp != c.udp {
			t.Errorf("%q → (%d,%d,%v,%v), want (%d,%d,%v,nil)", c.arg, l, r, udp, err, c.local, c.remote, c.udp)
		}
	}
}

// fakeDatagramSession loops sent datagrams straight back (device echo). It
// selects on ctx everywhere it can block so a cancelled test context always
// unblocks both serveUDPForward's recv() loop and sendDatagram(), instead of
// leaking a goroutine parked on an idle channel past test end (see
// fakeAgentStream in go/internal/agent/services/datagram_relay_test.go for
// the same pattern).
type fakeDatagramSession struct {
	ctx    context.Context
	frames chan *cloudpb.TunnelData
}

func newFakeDatagramSession(ctx context.Context) *fakeDatagramSession {
	return &fakeDatagramSession{ctx: ctx, frames: make(chan *cloudpb.TunnelData, 64)}
}
func (f *fakeDatagramSession) sendDatagram(flowID, port uint32, payload []byte) error {
	select {
	case f.frames <- &cloudpb.TunnelData{Datagram: &cloudpb.TunnelDatagram{
		FlowId: flowID, Port: port, Payload: payload,
	}}:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakeDatagramSession) recv() (*cloudpb.TunnelData, error) {
	select {
	case d, ok := <-f.frames:
		if !ok {
			return nil, io.EOF
		}
		return d, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

func TestServeUDPForwardRoundTrip(t *testing.T) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := newFakeDatagramSession(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- serveUDPForward(ctx, pc, session, 7000, time.Minute) }()

	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Write([]byte("probe")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("no echoed datagram: %v", err)
	}
	if string(buf[:n]) != "probe" {
		t.Fatalf("payload = %q, want probe", buf[:n])
	}

	// Shutdown (Ctrl+C-style ctx cancel) must report nil, not the session's
	// benign context.Canceled recv() error.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serveUDPForward returned %v on ctx shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for serveUDPForward to return after cancel")
	}
}
