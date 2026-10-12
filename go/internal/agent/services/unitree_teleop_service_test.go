package services

import (
	"context"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type teleopFixtureLink struct {
	*testUnitreeLink
	mu       sync.Mutex
	calls    []string
	failMove bool
}

func (l *teleopFixtureLink) record(name string) {
	l.mu.Lock()
	l.calls = append(l.calls, name)
	l.mu.Unlock()
}
func (l *teleopFixtureLink) Move(ctx context.Context, f, y float64) error {
	if f == 0 && y == 0 {
		return l.Stop(ctx)
	}
	l.record("move")
	l.mu.Lock()
	fail := l.failMove
	l.mu.Unlock()
	if fail {
		return context.DeadlineExceeded
	}
	return nil
}
func (l *teleopFixtureLink) Stop(context.Context) error    { l.record("stop"); return nil }
func (l *teleopFixtureLink) StandUp(context.Context) error { l.record("stand"); return nil }
func (l *teleopFixtureLink) LieDown(context.Context) error { l.record("lie"); return nil }
func (l *teleopFixtureLink) Hello(context.Context) error   { l.record("hello"); return nil }
func (l *teleopFixtureLink) log() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func teleopFixture(t *testing.T, authenticated bool, lease time.Duration) (pb.WendyUnitreeTeleopServiceClient, *teleopFixtureLink, *UnitreeService) {
	t.Helper()
	p := testUnitreeService(t)
	l := &teleopFixtureLink{testUnitreeLink: newTestUnitreeLink()}
	p.sessions["GO2TEST1"] = &unitreeSession{link: l, cancel: func() {}, done: make(chan struct{})}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if authenticated {
			ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.UnixAddr{Name: "fixture", Net: "unix"}})
		}
		return h(ctx, req)
	}))
	svc := NewUnitreeTeleopService(p)
	svc.lease = lease
	pb.RegisterWendyUnitreeTeleopServiceServer(server, svc)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); server.Stop(); listener.Close() })
	return pb.NewWendyUnitreeTeleopServiceClient(conn), l, p
}

func teleopSend(c pb.WendyUnitreeTeleopServiceClient, cmd pb.UnitreeCommand, f, y float64) error {
	_, err := c.Command(context.Background(), &pb.UnitreeCommandRequest{Serial: "GO2TEST1", Command: cmd, Forward: f, Yaw: y})
	return err
}

func sameCalls(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUnitreeTeleopRequiresTransport(t *testing.T) {
	c, l, _ := teleopFixture(t, false, time.Second)
	if err := teleopSend(c, pb.UnitreeCommand_UNITREE_COMMAND_HELLO, 0, 0); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("error %v", err)
	}
	if len(l.log()) != 0 {
		t.Fatal("unauthenticated caller sent robot commands")
	}
}

func TestUnitreeTeleopCommandsMapToRobot(t *testing.T) {
	c, l, _ := teleopFixture(t, true, time.Second)
	for _, cmd := range []pb.UnitreeCommand{
		pb.UnitreeCommand_UNITREE_COMMAND_STAND_UP,
		pb.UnitreeCommand_UNITREE_COMMAND_HELLO,
		pb.UnitreeCommand_UNITREE_COMMAND_LIE_DOWN,
		pb.UnitreeCommand_UNITREE_COMMAND_STOP,
	} {
		if err := teleopSend(c, cmd, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got := l.log(); !sameCalls(got, []string{"stand", "hello", "lie", "stop"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestUnitreeTeleopMoveExpiresUnlessRefreshed(t *testing.T) {
	c, l, _ := teleopFixture(t, true, 150*time.Millisecond)
	for range 4 {
		if err := teleopSend(c, pb.UnitreeCommand_UNITREE_COMMAND_MOVE, .2, 0); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := l.log(); !sameCalls(got, []string{"move", "move", "move", "move"}) {
		t.Fatalf("refreshed move stopped early: %v", got)
	}
	time.Sleep(300 * time.Millisecond)
	if got := l.log(); !sameCalls(got, []string{"move", "move", "move", "move", "stop"}) {
		t.Fatalf("move did not expire into one stop: %v", got)
	}
}

func TestUnitreeTeleopStopCancelsPendingExpiry(t *testing.T) {
	c, l, _ := teleopFixture(t, true, 100*time.Millisecond)
	if err := teleopSend(c, pb.UnitreeCommand_UNITREE_COMMAND_MOVE, 0, .5); err != nil {
		t.Fatal(err)
	}
	if err := teleopSend(c, pb.UnitreeCommand_UNITREE_COMMAND_STOP, 0, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if got := l.log(); !sameCalls(got, []string{"move", "stop"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestUnitreeTeleopRejectsInvalidInput(t *testing.T) {
	c, l, _ := teleopFixture(t, true, time.Second)
	for _, v := range [][2]float64{{.31, 0}, {0, -.61}, {math.NaN(), 0}, {math.Inf(1), 0}} {
		if err := teleopSend(c, pb.UnitreeCommand_UNITREE_COMMAND_MOVE, v[0], v[1]); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("velocity %v accepted: %v", v, err)
		}
	}
	if err := teleopSend(c, pb.UnitreeCommand_UNITREE_COMMAND_UNSPECIFIED, 0, 0); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unspecified command accepted: %v", err)
	}
	_, err := c.Command(context.Background(), &pb.UnitreeCommandRequest{Serial: "OTHER123", Command: pb.UnitreeCommand_UNITREE_COMMAND_HELLO})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("unpaired serial accepted: %v", err)
	}
	if got := l.log(); len(got) != 0 {
		t.Fatalf("invalid input sent robot commands: %v", got)
	}
}

func TestUnitreeTeleopFailedMoveStops(t *testing.T) {
	c, l, _ := teleopFixture(t, true, time.Second)
	l.failMove = true
	if err := teleopSend(c, pb.UnitreeCommand_UNITREE_COMMAND_MOVE, .1, 0); status.Code(err) != codes.Unavailable {
		t.Fatalf("error %v", err)
	}
	if got := l.log(); !sameCalls(got, []string{"move", "stop"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestUnitreeTeleopRejectsForeignTLSOwner(t *testing.T) {
	_, l, p := teleopFixture(t, true, time.Second)
	p.opMu.Lock()
	p.owner = "org:42"
	p.opMu.Unlock()
	ctx := unitreeTLSContext(t, "urn:wendy:org:43:user:fixture")
	_, err := NewUnitreeTeleopService(p).Command(ctx, &pb.UnitreeCommandRequest{Serial: "GO2TEST1", Command: pb.UnitreeCommand_UNITREE_COMMAND_HELLO})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign TLS owner accepted: %v", err)
	}
	if len(l.log()) != 0 {
		t.Fatal("foreign owner sent robot commands")
	}
}
