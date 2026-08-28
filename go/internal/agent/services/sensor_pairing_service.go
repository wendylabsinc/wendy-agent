package services

import (
	"context"

	"github.com/wendylabsinc/wendy/go/internal/agent/mcusource"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
)

// StartPairingFunc launches (or restarts) a supervisor goroutine for a pairing.
type StartPairingFunc func(p mcusource.SensorPairing, addr string)

// StopPairingFunc cancels a running supervisor.
type StopPairingFunc func(sourceAssetID int32)

type SensorPairingService struct {
	agentpbv2.UnimplementedWendySensorPairingServiceServer
	logger *zap.Logger
	store  *mcusource.PairingStore
	start  StartPairingFunc
	stop   StopPairingFunc
}

func NewSensorPairingService(logger *zap.Logger, store *mcusource.PairingStore, start StartPairingFunc, stop ...StopPairingFunc) *SensorPairingService {
	s := &SensorPairingService{logger: logger, store: store, start: start}
	if len(stop) > 0 {
		s.stop = stop[0]
	}
	return s
}

func (s *SensorPairingService) AddSensorPairing(_ context.Context, req *agentpbv2.AddSensorPairingRequest) (*agentpbv2.AddSensorPairingResponse, error) {
	p := mcusource.SensorPairing{
		SourceAssetID:   req.SourceAssetId,
		Name:            req.Name,
		SensorAllowlist: req.SensorAllowlist,
	}
	if err := s.store.Add(p); err != nil {
		return nil, err
	}
	if s.start != nil {
		s.start(p, req.SourceAddress)
	}
	return &agentpbv2.AddSensorPairingResponse{Pairing: toProto(p, true)}, nil
}

func (s *SensorPairingService) RemoveSensorPairing(_ context.Context, req *agentpbv2.RemoveSensorPairingRequest) (*agentpbv2.RemoveSensorPairingResponse, error) {
	if s.stop != nil {
		s.stop(req.SourceAssetId)
	}
	if err := s.store.Remove(req.SourceAssetId); err != nil {
		return nil, err
	}
	return &agentpbv2.RemoveSensorPairingResponse{}, nil
}

func (s *SensorPairingService) ListSensorPairings(_ context.Context, _ *agentpbv2.ListSensorPairingsRequest) (*agentpbv2.ListSensorPairingsResponse, error) {
	list := s.store.List()
	out := make([]*agentpbv2.SensorPairing, 0, len(list))
	for _, p := range list {
		out = append(out, toProto(p, false))
	}
	return &agentpbv2.ListSensorPairingsResponse{Pairings: out}, nil
}

func toProto(p mcusource.SensorPairing, connected bool) *agentpbv2.SensorPairing {
	return &agentpbv2.SensorPairing{
		SourceAssetId:   p.SourceAssetID,
		OrgId:           p.OrgID,
		Name:            p.Name,
		SensorAllowlist: p.SensorAllowlist,
		Connected:       connected,
	}
}
