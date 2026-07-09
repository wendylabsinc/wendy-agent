package services

import (
	"go.uber.org/zap"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// SimService fronts a simulation backend (wendy.sim.v1.RobotBackendService,
// running as a containerized service — MuJoCo first) toward the CLI/MCP. It
// will own backend selection (WENDY_SIM_BACKEND) and per-session ControlLevel
// gating before forwarding calls.
//
// P0 skeleton: every RPC returns Unimplemented via the embedded server; the
// MuJoCo backend wiring lands with WendySim P1.
type SimService struct {
	agentpbv2.UnimplementedWendySimServiceServer
	logger *zap.Logger
}

// NewSimService builds the skeleton sim service.
func NewSimService(logger *zap.Logger) *SimService {
	return &SimService{logger: logger}
}
