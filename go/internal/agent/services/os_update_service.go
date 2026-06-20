package services

import (
	"bufio"
	"fmt"
	"os/exec"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type OSUpdateService struct {
	agentpbv2.UnimplementedWendyOSUpdateServiceServer
	logger        *zap.Logger
	isWendyOSHost func() bool
}

func NewOSUpdateService(logger *zap.Logger) *OSUpdateService {
	return &OSUpdateService{logger: logger, isWendyOSHost: defaultIsWendyOSHost}
}

func (s *OSUpdateService) UpdateOS(req *agentpbv2.UpdateOSRequest, stream grpc.ServerStreamingServer[agentpbv2.UpdateOSResponse]) error {
	s.logger.Info("UpdateOS started", zap.String("artifact_url", req.GetArtifactUrl()))

	if !s.isWendyOSHost() {
		s.logger.Warn("UpdateOS rejected: host is not a WendyOS OTA target", zap.String("artifact_url", req.GetArtifactUrl()))
		return stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_Failed_{
				Failed: &agentpbv2.UpdateOSResponse_Failed{
					ErrorMessage: osUpdateUnsupportedForHostMessage,
				},
			},
		})
	}

	// Stop the auto-updater so it can't SIGTERM in-flight mender mid-OTA; see
	// inhibitAutoUpdater. Restored on return.
	restoreUpdater := inhibitAutoUpdater(s.logger)
	defer restoreUpdater()

	sendProgress := func(phase string, percent int32) {
		_ = stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_Progress_{
				Progress: &agentpbv2.UpdateOSResponse_Progress{Phase: phase, Percent: percent},
			},
		})
	}

	if err := enableJetsonRootfsAB(s.logger); err != nil {
		return stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_Failed_{
				Failed: &agentpbv2.UpdateOSResponse_Failed{
					ErrorMessage: fmt.Sprintf("Jetson A/B setup failed: %v", err),
				},
			},
		})
	}

	sendProgress("downloading", 0)
	cmdName, found := resolveMenderBinary()
	if !found {
		return stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_Failed_{
				Failed: &agentpbv2.UpdateOSResponse_Failed{ErrorMessage: "mender-update binary not found"},
			},
		})
	}

	cmd := exec.CommandContext(stream.Context(), cmdName, "install", req.GetArtifactUrl())
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_Failed_{
				Failed: &agentpbv2.UpdateOSResponse_Failed{
					ErrorMessage: fmt.Sprintf("failed to create stderr pipe: %v", err),
				},
			},
		})
	}

	if err := cmd.Start(); err != nil {
		return stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_Failed_{
				Failed: &agentpbv2.UpdateOSResponse_Failed{
					ErrorMessage: fmt.Sprintf("failed to start mender: %v", err),
				},
			},
		})
	}

	// Retain the tail of mender's output so a non-zero exit can report the
	// real cause (e.g. an incompatible device type) instead of a bare
	// "exit status 1".
	outputTail := newLineRing(menderErrorTailLines)
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		outputTail.push(line)
		if m := menderProgressRe.FindStringSubmatch(line); len(m) > 1 {
			if pct := parseInt32(m[1]); pct >= 0 {
				sendProgress("installing", pct)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		s.logger.Warn("mender output scan error", zap.Error(err))
	}

	if err := cmd.Wait(); err != nil {
		msg := formatMenderFailure(err, outputTail.tail())
		s.logger.Error("mender install failed", zap.Error(err), zap.Strings("output_tail", outputTail.tail()))
		return stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_Failed_{
				Failed: &agentpbv2.UpdateOSResponse_Failed{
					ErrorMessage: msg,
				},
			},
		})
	}

	rebootRequired := true
	return stream.Send(&agentpbv2.UpdateOSResponse{
		ResponseType: &agentpbv2.UpdateOSResponse_Completed_{
			Completed: &agentpbv2.UpdateOSResponse_Completed{RebootRequired: rebootRequired},
		},
	})
}

func parseInt32(s string) int32 {
	var n int32
	fmt.Sscanf(s, "%d", &n)
	return n
}
