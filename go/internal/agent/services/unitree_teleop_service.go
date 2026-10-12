package services

import (
	"context"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/unitree"
	pb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const teleopRobotTimeout = 200 * time.Millisecond
const teleopMoveLease = 500 * time.Millisecond

type unitreeMotionLink interface {
	unitreeLink
	Move(context.Context, float64, float64) error
	Stop(context.Context) error
	StandUp(context.Context) error
	LieDown(context.Context) error
	Hello(context.Context) error
}

// UnitreeTeleopService never connects a robot or sees its key. It sends a
// small fixed set of commands over the retained paired connection. A move
// expires unless refreshed, so a crashed caller cannot leave the robot walking.
type UnitreeTeleopService struct {
	pb.UnimplementedWendyUnitreeTeleopServiceServer
	pairing *UnitreeService
	lease   time.Duration
	mu      sync.Mutex
	gen     map[string]uint64
	timers  map[string]*time.Timer
}

func NewUnitreeTeleopService(pairing *UnitreeService) *UnitreeTeleopService {
	return &UnitreeTeleopService{pairing: pairing, lease: teleopMoveLease,
		gen: make(map[string]uint64), timers: make(map[string]*time.Timer)}
}

func (s *UnitreeTeleopService) Command(ctx context.Context, req *pb.UnitreeCommandRequest) (*pb.UnitreeCommandResponse, error) {
	if err := requireUnitreeTransport(ctx); err != nil {
		return nil, err
	}
	if !unitree.ValidSerial(req.GetSerial()) {
		return nil, status.Error(codes.InvalidArgument, "a paired Go2 serial is required")
	}
	if req.GetCommand() == pb.UnitreeCommand_UNITREE_COMMAND_MOVE && !unitree.ValidMotion(req.GetForward(), req.GetYaw()) {
		return nil, status.Error(codes.InvalidArgument, "Go2 velocity outside limits")
	}
	link, err := s.link(ctx, req.GetSerial())
	if err != nil {
		return nil, err
	}

	// Cancel any pending move expiry first, so it cannot fire after this command.
	gen := s.cancelLease(req.GetSerial())
	commandCtx, cancel := context.WithTimeout(ctx, teleopRobotTimeout)
	defer cancel()
	switch req.GetCommand() {
	case pb.UnitreeCommand_UNITREE_COMMAND_MOVE:
		err = link.Move(commandCtx, req.GetForward(), req.GetYaw())
		if err == nil && (req.GetForward() != 0 || req.GetYaw() != 0) {
			s.armLease(req.GetSerial(), gen, link)
		}
	case pb.UnitreeCommand_UNITREE_COMMAND_STOP:
		err = link.Stop(commandCtx)
	case pb.UnitreeCommand_UNITREE_COMMAND_STAND_UP:
		err = link.StandUp(commandCtx)
	case pb.UnitreeCommand_UNITREE_COMMAND_LIE_DOWN:
		err = link.LieDown(commandCtx)
	case pb.UnitreeCommand_UNITREE_COMMAND_HELLO:
		err = link.Hello(commandCtx)
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown Go2 command")
	}
	if err != nil {
		if req.GetCommand() == pb.UnitreeCommand_UNITREE_COMMAND_MOVE {
			stopRobot(link)
		}
		return nil, status.Error(codes.Unavailable, "robot did not acknowledge the command")
	}
	return &pb.UnitreeCommandResponse{}, nil
}

// link returns the retained connection after the pairing-owner check, which
// applies even when the common listener's scope enforcement is in grace/off mode.
func (s *UnitreeTeleopService) link(ctx context.Context, serial string) (unitreeMotionLink, error) {
	if !s.pairing.opMu.TryLock() {
		return nil, status.Error(codes.Unavailable, "pairing is changing; try again")
	}
	defer s.pairing.opMu.Unlock()
	if err := s.pairing.authorize(ctx); err != nil {
		return nil, err
	}
	s.pairing.mu.RLock()
	defer s.pairing.mu.RUnlock()
	var link unitreeMotionLink
	if session := s.pairing.sessions[serial]; session != nil {
		link, _ = session.link.(unitreeMotionLink)
	}
	if link == nil || !link.Connected() {
		return nil, status.Error(codes.Unavailable, "paired Go2 is not connected")
	}
	return link, nil
}

func (s *UnitreeTeleopService) cancelLease(serial string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen[serial]++
	if t := s.timers[serial]; t != nil {
		t.Stop()
		delete(s.timers, serial)
	}
	return s.gen[serial]
}

func (s *UnitreeTeleopService) armLease(serial string, gen uint64, link unitreeMotionLink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen[serial] != gen {
		return // a newer command already arrived
	}
	s.timers[serial] = time.AfterFunc(s.lease, func() {
		s.mu.Lock()
		current := s.gen[serial] == gen
		if current {
			delete(s.timers, serial)
		}
		s.mu.Unlock()
		if current {
			stopRobot(link)
		}
	})
}

func stopRobot(link unitreeMotionLink) {
	ctx, cancel := context.WithTimeout(context.Background(), teleopRobotTimeout)
	defer cancel()
	_ = link.Stop(ctx)
}
