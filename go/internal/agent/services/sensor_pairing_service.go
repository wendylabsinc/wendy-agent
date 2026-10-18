package services

import (
	"context"

	"github.com/wendylabsinc/wendy/go/internal/agent/mcusource"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StartPairingFunc launches (or restarts) a supervisor goroutine for a pairing.
type StartPairingFunc func(p mcusource.SensorPairing, addr string)

// StopPairingFunc cancels a running supervisor.
type StopPairingFunc func(mcusource.SensorPairing)

// IsConnectedFunc reports whether a pairing is delivering sensor frames.
type IsConnectedFunc func(mcusource.SensorPairing) bool

type SensorPairingService struct {
	agentpbv2.UnimplementedWendySensorPairingServiceServer
	logger      *zap.Logger
	store       *mcusource.PairingStore
	agentScope  func() certs.Scope
	start       StartPairingFunc
	stop        StopPairingFunc
	isConnected IsConnectedFunc
}

// agentScope reads this agent's current identity on every Add so pairing
// follows provisioning changes without a restart. The dialer independently
// verifies the source certificate against the current consumer identity.
func NewSensorPairingService(logger *zap.Logger, store *mcusource.PairingStore, agentScope func() certs.Scope, start StartPairingFunc, stop StopPairingFunc, isConnected IsConnectedFunc) *SensorPairingService {
	return &SensorPairingService{logger: logger, store: store, agentScope: agentScope, start: start, stop: stop, isConnected: isConnected}
}

func (s *SensorPairingService) AddSensorPairing(_ context.Context, req *agentpbv2.AddSensorPairingRequest) (*agentpbv2.AddSensorPairingResponse, error) {
	scope := s.agentScope()
	p := mcusource.SensorPairing{
		SourceAssetID:      req.SourceAssetId,
		SourcePrincipalURI: req.SourcePrincipalUri,
		OrgID:              scope.OrgID,
		Name:               req.Name,
		SensorAllowlist:    req.SensorAllowlist,
		Transport:          req.Transport,
	}
	if err := p.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if p.SourcePrincipalURI != "" {
		identity, _ := certs.ParsePrincipal(p.SourcePrincipalURI)
		if !identity.Scope().Matches(scope) {
			return nil, status.Error(codes.PermissionDenied, "sensor source must belong to this device's tenant")
		}
	}
	if err := s.store.Add(p); err != nil {
		return nil, err
	}
	if s.start != nil {
		s.start(p, req.SourceAddress)
	}
	// The supervisor has not delivered a frame yet.
	return &agentpbv2.AddSensorPairingResponse{Pairing: toProto(p, false)}, nil
}

func (s *SensorPairingService) RemoveSensorPairing(_ context.Context, req *agentpbv2.RemoveSensorPairingRequest) (*agentpbv2.RemoveSensorPairingResponse, error) {
	p := mcusource.SensorPairing{SourceAssetID: req.SourceAssetId, SourcePrincipalURI: req.SourcePrincipalUri, Transport: "wendycom"}
	if err := p.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if s.stop != nil {
		s.stop(p)
	}
	if err := s.store.RemovePairing(p); err != nil {
		return nil, err
	}
	return &agentpbv2.RemoveSensorPairingResponse{}, nil
}

func (s *SensorPairingService) ListSensorPairings(_ context.Context, _ *agentpbv2.ListSensorPairingsRequest) (*agentpbv2.ListSensorPairingsResponse, error) {
	list := s.store.List()
	out := make([]*agentpbv2.SensorPairing, 0, len(list))
	for _, p := range list {
		connected := s.isConnected != nil && s.isConnected(p)
		out = append(out, toProto(p, connected))
	}
	return &agentpbv2.ListSensorPairingsResponse{Pairings: out, SupportsPrincipalPairing: true}, nil
}

func toProto(p mcusource.SensorPairing, connected bool) *agentpbv2.SensorPairing {
	return &agentpbv2.SensorPairing{
		SourceAssetId:      p.SourceAssetID,
		SourcePrincipalUri: p.SourcePrincipalURI,
		OrgId:              p.OrgID,
		Name:               p.Name,
		SensorAllowlist:    p.SensorAllowlist,
		Connected:          connected,
		Transport:          p.Transport,
	}
}
