package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/unitree"
	pb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const teleopFrameTimeout = 350 * time.Millisecond
const teleopRobotTimeout = 200 * time.Millisecond

type unitreeMotionLink interface {
	unitreeLink
	Move(context.Context, float64, float64) error
	Stop(context.Context) error
}

// UnitreeTeleopService never connects a robot. It borrows one exact retained
// connection for an explicit control stream, and never follows its reconnect.
// Disabled by default; configuration and live acceptance require operator gates.
type UnitreeTeleopService struct {
	pb.UnimplementedWendyUnitreeTeleopServiceServer
	pairing *UnitreeService
	enabled bool
	mu      sync.Mutex
	active  map[string]bool
}

func NewUnitreeTeleopService(pairing *UnitreeService, enabled bool) *UnitreeTeleopService {
	return &UnitreeTeleopService{pairing: pairing, enabled: enabled, active: make(map[string]bool)}
}

type teleopReceived struct {
	frame *pb.UnitreeTeleopFrame
	err   error
}

func (s *UnitreeTeleopService) Control(stream grpc.BidiStreamingServer[pb.UnitreeTeleopFrame, pb.UnitreeTeleopReady]) (result error) {
	if err := requireUnitreeTransport(stream.Context()); err != nil {
		return err
	}
	if !s.enabled {
		return status.Error(codes.FailedPrecondition, "Go2 teleop is disabled by the agent operator")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	frames := make(chan teleopReceived, 1)
	go func() {
		for {
			frame, err := stream.Recv()
			select {
			case frames <- teleopReceived{frame, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-time.After(5 * time.Second):
		return status.Error(codes.DeadlineExceeded, "select a robot to begin")
	case first := <-frames:
		f := first.frame
		if first.err != nil {
			return first.err
		}
		if f == nil || !unitree.ValidSerial(f.Serial) || f.Challenge != "" || f.Forward != 0 || f.Yaw != 0 || f.Stop {
			return status.Error(codes.InvalidArgument, "first frame must select an exact paired serial with zero velocity")
		}
		// Reuse pairing-owner authorization even when the common listener's
		// scope enforcement is in grace/off mode. Never wait on a rebind.
		if !s.pairing.opMu.TryLock() {
			return status.Error(codes.Unavailable, "pairing is changing; control disabled")
		}
		if err := s.pairing.authorize(ctx); err != nil {
			s.pairing.opMu.Unlock()
			return err
		}
		s.pairing.mu.RLock()
		session := s.pairing.sessions[f.Serial]
		var link unitreeMotionLink
		if session != nil {
			link, _ = session.link.(unitreeMotionLink)
		}
		s.pairing.mu.RUnlock()
		s.pairing.opMu.Unlock()
		s.mu.Lock()
		if s.active[f.Serial] {
			s.mu.Unlock()
			return status.Error(codes.AlreadyExists, "another controller owns this robot")
		}
		s.active[f.Serial] = true
		s.mu.Unlock()
		defer func() { s.mu.Lock(); delete(s.active, f.Serial); s.mu.Unlock() }()
		if link == nil || !link.Connected() {
			return status.Error(codes.Unavailable, "selected paired robot is not connected")
		}
		// Ending this stream ALWAYS invalidates control. A final best-effort Stop
		// is attempted even after a command timeout. Failure must not claim stopped.
		defer func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), teleopRobotTimeout)
			defer stopCancel()
			if err := link.Stop(stopCtx); err != nil {
				result = status.Error(codes.Unavailable, "control ended; robot stop acknowledgement unavailable; use physical stop")
			}
		}()
		commandCtx, commandCancel := context.WithTimeout(ctx, teleopRobotTimeout)
		err := link.Stop(commandCtx)
		commandCancel()
		if err != nil {
			return status.Error(codes.Unavailable, "robot did not acknowledge initial stop; controls remain disabled")
		}
		for {
			if ctx.Err() != nil || !link.Connected() {
				return status.Error(codes.Unavailable, "control connection ended")
			}
			// Bind all subsequent frames to this same connection, not a replacement.
			if !s.pairing.mu.TryRLock() {
				return status.Error(codes.Unavailable, "pairing is changing; control disabled")
			}
			unchanged := s.pairing.sessions[f.Serial] == session && session.link == link
			s.pairing.mu.RUnlock()
			if !unchanged {
				return status.Error(codes.Unavailable, "paired connection changed; enable a new supervised session")
			}
			challengeBytes := make([]byte, 16)
			if _, err := rand.Read(challengeBytes); err != nil {
				return status.Error(codes.Internal, "cannot create control challenge")
			}
			challenge := hex.EncodeToString(challengeBytes)
			ready := &pb.UnitreeTeleopReady{Challenge: challenge, TimeoutMs: uint32(teleopFrameTimeout / time.Millisecond)}
			// Budget includes sending the acknowledgement: a blocked client cannot
			// keep control alive just because grpc Send is waiting for flow control.
			deadline := time.Now().Add(teleopFrameTimeout)
			timer := time.NewTimer(time.Until(deadline))
			sendResult := make(chan error, 1)
			go func() { sendResult <- stream.Send(ready) }()
			select {
			case <-ctx.Done():
				timer.Stop()
				return status.FromContextError(ctx.Err()).Err()
			case <-link.Done():
				timer.Stop()
				return status.Error(codes.Unavailable, "robot disconnected")
			case <-timer.C:
				return status.Error(codes.DeadlineExceeded, "control reply timed out; enable again")
			case err := <-sendResult:
				if err != nil {
					timer.Stop()
					return err
				}
			}
			var next teleopReceived
			select {
			case <-ctx.Done():
				timer.Stop()
				return status.FromContextError(ctx.Err()).Err()
			case <-link.Done():
				timer.Stop()
				return status.Error(codes.Unavailable, "robot disconnected")
			case <-timer.C:
				return status.Error(codes.DeadlineExceeded, "control input expired; enable again")
			case next = <-frames:
				timer.Stop()
			}
			if !time.Now().Before(deadline) {
				return status.Error(codes.DeadlineExceeded, "control input expired; enable again")
			}
			if next.err != nil {
				return next.err
			}
			n := next.frame
			if n == nil || n.Serial != f.Serial || n.Challenge != challenge || !unitree.ValidMotion(n.Forward, n.Yaw) {
				return status.Error(codes.InvalidArgument, "invalid, stale or out-of-order control frame")
			}
			if n.Stop {
				return nil
			}
			if !s.pairing.mu.TryRLock() {
				return status.Error(codes.Unavailable, "pairing is changing; control disabled")
			}
			unchanged = s.pairing.sessions[f.Serial] == session && session.link == link
			s.pairing.mu.RUnlock()
			if !unchanged {
				return status.Error(codes.Unavailable, "paired connection changed; enable a new supervised session")
			}
			if !time.Now().Before(deadline) {
				return status.Error(codes.DeadlineExceeded, "control input expired; enable again")
			}
			commandCtx, commandCancel = context.WithTimeout(ctx, teleopRobotTimeout)
			err = link.Move(commandCtx, n.Forward, n.Yaw)
			commandCancel()
			if err != nil {
				return status.Error(codes.Unavailable, "robot command was not acknowledged; control disabled")
			}
		}
	}
}
