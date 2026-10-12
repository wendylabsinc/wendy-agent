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
	mu        sync.Mutex
	moves     int
	stops     int
	failMove  bool
	stallMove bool
	failStop  bool
}

func (l *teleopFixtureLink) Move(ctx context.Context, f, y float64) error {
	if f == 0 && y == 0 {
		return l.Stop(ctx)
	}
	l.mu.Lock()
	l.moves++
	fail, stall := l.failMove, l.stallMove
	l.mu.Unlock()
	if stall {
		<-ctx.Done()
		return ctx.Err()
	}
	if fail {
		return context.DeadlineExceeded
	}
	return nil
}
func (l *teleopFixtureLink) Stop(context.Context) error {
	l.mu.Lock()
	l.stops++
	fail := l.failStop
	l.mu.Unlock()
	if fail {
		return context.DeadlineExceeded
	}
	return nil
}
func (l *teleopFixtureLink) counts() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.moves, l.stops
}

type teleopTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s teleopTestStream) Context() context.Context { return s.ctx }
func teleopFixture(t *testing.T, enabled, authenticated bool) (pb.WendyUnitreeTeleopServiceClient, *teleopFixtureLink, *UnitreeService) {
	t.Helper()
	p := testUnitreeService(t)
	l := &teleopFixtureLink{testUnitreeLink: newTestUnitreeLink()}
	p.sessions["GO2TEST1"] = &unitreeSession{link: l, cancel: func() {}, done: make(chan struct{})}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		ctx := ss.Context()
		if authenticated {
			ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.UnixAddr{Name: "fixture", Net: "unix"}})
		}
		return h(srv, teleopTestStream{ss, ctx})
	}))
	pb.RegisterWendyUnitreeTeleopServiceServer(server, NewUnitreeTeleopService(p, enabled))
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); server.Stop(); listener.Close() })
	return pb.NewWendyUnitreeTeleopServiceClient(conn), l, p
}
func beginTeleop(t *testing.T, c pb.WendyUnitreeTeleopServiceClient) (grpc.BidiStreamingClient[pb.UnitreeTeleopFrame, pb.UnitreeTeleopReady], *pb.UnitreeTeleopReady) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	stream, err := c.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(&pb.UnitreeTeleopFrame{Serial: "GO2TEST1"}); err != nil {
		t.Fatal(err)
	}
	ready, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	return stream, ready
}
func TestUnitreeTeleopRequiresExplicitEnableAndTransport(t *testing.T) {
	for _, tc := range []struct {
		enabled, auth bool
		code          codes.Code
	}{{false, true, codes.FailedPrecondition}, {true, false, codes.Unauthenticated}} {
		c, l, _ := teleopFixture(t, tc.enabled, tc.auth)
		s, _ := c.Control(context.Background())
		_ = s.Send(&pb.UnitreeTeleopFrame{Serial: "GO2TEST1"})
		_, err := s.Recv()
		if status.Code(err) != tc.code {
			t.Fatalf("error %v", err)
		}
		if m, n := l.counts(); m != 0 || n != 0 {
			t.Fatal("disabled/unauthenticated sent robot commands")
		}
	}
}
func TestUnitreeTeleopMoveReleaseStopAndSingleController(t *testing.T) {
	c, l, _ := teleopFixture(t, true, true)
	s, r := beginTeleop(t, c)
	second, _ := c.Control(context.Background())
	_ = second.Send(&pb.UnitreeTeleopFrame{Serial: "GO2TEST1"})
	_, err := second.Recv()
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("second controller accepted: %v", err)
	}
	_ = s.Send(&pb.UnitreeTeleopFrame{Serial: "GO2TEST1", Challenge: r.Challenge, Forward: .1})
	r, err = s.Recv()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Send(&pb.UnitreeTeleopFrame{Serial: "GO2TEST1", Challenge: r.Challenge})
	r, err = s.Recv()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Send(&pb.UnitreeTeleopFrame{Serial: "GO2TEST1", Challenge: r.Challenge, Stop: true})
	_, err = s.Recv()
	if err == nil {
		t.Fatal("Stop did not end control")
	}
	if m, n := l.counts(); m != 1 || n != 3 {
		t.Fatalf("moves=%d stops=%d", m, n)
	}
}
func TestUnitreeTeleopFaultsInvalidateAndStop(t *testing.T) {
	for _, name := range []string{"expired", "wrong challenge", "nan", "speed", "robot disconnected", "connection replaced", "client closed", "command failure", "command timeout"} {
		t.Run(name, func(t *testing.T) {
			c, l, p := teleopFixture(t, true, true)
			s, r := beginTeleop(t, c)
			f := &pb.UnitreeTeleopFrame{Serial: "GO2TEST1", Challenge: r.Challenge, Forward: .1}
			switch name {
			case "expired":
			case "wrong challenge":
				f.Challenge = "old"
				_ = s.Send(f)
			case "nan":
				f.Forward = math.NaN()
				_ = s.Send(f)
			case "speed":
				f.Forward = 1
				_ = s.Send(f)
			case "robot disconnected":
				_ = l.Close()
			case "connection replaced":
				p.mu.Lock()
				p.sessions["GO2TEST1"] = &unitreeSession{cancel: func() {}, done: make(chan struct{})}
				p.mu.Unlock()
				_ = s.Send(&pb.UnitreeTeleopFrame{Serial: "GO2TEST1", Challenge: r.Challenge})
			case "client closed":
				_ = s.CloseSend()
			case "command failure":
				l.mu.Lock()
				l.failMove = true
				l.mu.Unlock()
				_ = s.Send(f)
			case "command timeout":
				l.mu.Lock()
				l.stallMove = true
				l.mu.Unlock()
				_ = s.Send(f)
			}
			_, err := s.Recv()
			if err == nil {
				t.Fatal("fault left controls active")
			}
			if _, n := l.counts(); n < 2 {
				t.Fatal("fault did not attempt stop")
			}
		})
	}
}

func TestUnitreeTeleopUnacknowledgedStopDoesNotClaimSuccess(t *testing.T) {
	c, l, _ := teleopFixture(t, true, true)
	s, r := beginTeleop(t, c)
	l.mu.Lock()
	l.failStop = true
	l.mu.Unlock()
	_ = s.Send(&pb.UnitreeTeleopFrame{Serial: "GO2TEST1", Challenge: r.Challenge, Stop: true})
	_, err := s.Recv()
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("unacknowledged Stop reported success: %v", err)
	}
}

type blockedTeleopReply struct {
	grpc.ServerStream
	ctx   context.Context
	first bool
}

func (s *blockedTeleopReply) Context() context.Context { return s.ctx }
func (s *blockedTeleopReply) Recv() (*pb.UnitreeTeleopFrame, error) {
	if !s.first {
		s.first = true
		return &pb.UnitreeTeleopFrame{Serial: "GO2TEST1"}, nil
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}
func (s *blockedTeleopReply) Send(*pb.UnitreeTeleopReady) error { <-s.ctx.Done(); return s.ctx.Err() }
func TestUnitreeTeleopBlockedReplyExpiresAndStops(t *testing.T) {
	_, l, p := teleopFixture(t, true, true)
	ctx, cancel := context.WithCancel(unitreeTestContext())
	defer cancel()
	started := time.Now()
	err := NewUnitreeTeleopService(p, true).Control(&blockedTeleopReply{ctx: ctx})
	if status.Code(err) != codes.DeadlineExceeded || time.Since(started) > time.Second {
		t.Fatalf("blocked reply retained control: %v", err)
	}
	if _, stops := l.counts(); stops != 2 {
		t.Fatal("blocked reply did not stop")
	}
}

func TestUnitreeTeleopRejectsForeignTLSOwnerBeforeCommands(t *testing.T) {
	_, l, p := teleopFixture(t, true, true)
	p.opMu.Lock()
	p.owner = "org:42"
	p.opMu.Unlock()
	ctx, cancel := context.WithCancel(unitreeTLSContext(t, "urn:wendy:org:43:user:fixture"))
	defer cancel()
	err := NewUnitreeTeleopService(p, true).Control(&blockedTeleopReply{ctx: ctx})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign TLS owner accepted: %v", err)
	}
	if moves, stops := l.counts(); moves != 0 || stops != 0 {
		t.Fatal("foreign owner sent robot commands")
	}
}
