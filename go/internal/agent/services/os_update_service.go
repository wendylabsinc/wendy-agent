package services

import (
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type OSUpdateService struct {
	agentpbv2.UnimplementedWendyOSUpdateServiceServer
	logger        *zap.Logger
	isWendyOSHost func() bool
	stateDir      string
}

func NewOSUpdateService(logger *zap.Logger) *OSUpdateService {
	return &OSUpdateService{
		logger:        logger,
		isWendyOSHost: defaultIsWendyOSHost,
		stateDir:      oshealth.DefaultStateDir,
	}
}

func (s *OSUpdateService) UpdateOS(req *agentpbv2.UpdateOSRequest, stream grpc.ServerStreamingServer[agentpbv2.UpdateOSResponse]) error {
	s.logger.Info("UpdateOS started",
		zap.String("artifact_url", req.GetArtifactUrl()), zap.String("updater", req.GetUpdaterBackend()))

	if !s.isWendyOSHost() {
		s.logger.Warn("UpdateOS rejected: host is not a WendyOS OTA target", zap.String("artifact_url", req.GetArtifactUrl()))
		return sendOSUpdateFailureV2(stream, osUpdateUnsupportedForHostMessage)
	}

	// Stop the auto-updater so it can't SIGTERM the in-flight install mid-OTA;
	// see inhibitAutoUpdater. Restored on return.
	restoreUpdater := inhibitAutoUpdater(s.logger)
	defer restoreUpdater()

	updater, err := selectUpdater(s.logger, req.GetUpdaterBackend(), req.GetArtifactUrl())
	if err != nil {
		s.logger.Warn("UpdateOS rejected: no usable updater backend", zap.Error(err))
		return sendOSUpdateFailureV2(stream, err.Error())
	}
	s.logger.Info("UpdateOS using backend", zap.String("backend", updater.name()))

	if handled, err := armRedundancyPreflightV2(s.logger, stream); handled {
		return err
	}

	sendProgress := func(phase string, percent int32) {
		_ = stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_Progress_{
				Progress: &agentpbv2.UpdateOSResponse_Progress{Phase: phase, Percent: percent},
			},
		})
	}

	if err := updater.install(stream.Context(), req.GetArtifactUrl(), sendProgress); err != nil {
		return sendOSUpdateFailureV2(stream, err.Error())
	}

	recordPendingOSUpdate(s.logger, s.stateDir, req.GetArtifactUrl(), updater.name())

	return stream.Send(&agentpbv2.UpdateOSResponse{
		ResponseType: &agentpbv2.UpdateOSResponse_Completed_{
			Completed: &agentpbv2.UpdateOSResponse_Completed{RebootRequired: true},
		},
	})
}

// sendOSUpdateFailureV2 sends a terminal Failed response on the v2 UpdateOS stream.
func sendOSUpdateFailureV2(stream grpc.ServerStreamingServer[agentpbv2.UpdateOSResponse], msg string) error {
	return stream.Send(&agentpbv2.UpdateOSResponse{
		ResponseType: &agentpbv2.UpdateOSResponse_Failed_{
			Failed: &agentpbv2.UpdateOSResponse_Failed{ErrorMessage: msg},
		},
	})
}

// armRedundancyPreflightV2 arms Jetson A/B rootfs redundancy before an OTA when
// required. It returns handled=true when it has produced a terminal outcome on
// the stream (a Failed message, or an ArmingRedundancy+reboot); the caller then
// returns err. handled=false means the install should proceed normally.
func armRedundancyPreflightV2(logger *zap.Logger, stream grpc.ServerStreamingServer[agentpbv2.UpdateOSResponse]) (bool, error) {
	armer := makeRedundancyArmer(logger)
	switch armer.decide() {
	case armImpossibleNoSlot:
		return true, sendOSUpdateFailureV2(stream, redundancyNoSlotMessage)
	case armFailedPreviously:
		return true, sendOSUpdateFailureV2(stream, redundancyArmFailedMessage)
	case armPossible:
		if err := stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_ArmingRedundancy_{
				ArmingRedundancy: &agentpbv2.UpdateOSResponse_ArmingRedundancy{
					Message: redundancyArmingMessage, WillReboot: true,
				},
			},
		}); err != nil {
			return true, err
		}
		if err := armer.arm(); err != nil {
			return true, sendOSUpdateFailureV2(stream, "arming rootfs A/B redundancy failed: "+err.Error())
		}
		return true, nil // device is rebooting; the CLI reconnects and retries
	default: // armNotNeeded
		return false, nil
	}
}
