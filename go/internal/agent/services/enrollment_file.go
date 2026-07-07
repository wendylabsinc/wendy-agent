package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/enrolltoken"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// enrollmentFileName is the handoff file agent.sh writes with a short-lived
// enrollment token for a Linux Desktop install.
const enrollmentFileName = "enrollment.json"

type stagedEnrollment struct {
	Token     string `json:"token"`
	CloudHost string `json:"cloudHost"`
}

// ApplyEnrollmentFile self-enrolls the agent from a token staged by agent.sh at
// <configPath>/enrollment.json, then deletes the file. It is best-effort and
// safe to call unconditionally at startup: absent file is a no-op; an
// already-enrolled agent just clears the file; a malformed or failed token is
// logged and the file removed (the 1h TTL self-limits it anyway).
func (s *ProvisioningService) ApplyEnrollmentFile(ctx context.Context) {
	path := filepath.Join(s.configPath, enrollmentFileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		s.logger.Error("Failed to read enrollment file", zap.String("path", path), zap.Error(err))
		s.removeEnrollmentFile(path)
		return
	}

	if _, _, _, enrolled := s.ProvisioningInfo(); enrolled {
		s.logger.Info("Agent already enrolled; discarding staged enrollment token")
		s.removeEnrollmentFile(path)
		return
	}

	var req stagedEnrollment
	if err := json.Unmarshal(data, &req); err != nil {
		s.logger.Error("Failed to parse enrollment file, removing", zap.Error(err))
		s.removeEnrollmentFile(path)
		return
	}
	if req.Token == "" || req.CloudHost == "" {
		s.logger.Error("Enrollment file is incomplete, removing")
		s.removeEnrollmentFile(path)
		return
	}

	orgID, assetID, err := enrolltoken.ParseAsset(req.Token)
	if err != nil {
		s.logger.Error("Enrollment token is not a valid asset token, removing", zap.Error(err))
		s.removeEnrollmentFile(path)
		return
	}

	// Bounded retry to tolerate a slow boot-time network. The token's short TTL
	// caps how long a failing token stays useful, so we do not retry forever.
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		_, lastErr = s.StartProvisioning(ctx, &agentpb.StartProvisioningRequest{
			EnrollmentToken: req.Token,
			CloudHost:       req.CloudHost,
			OrganizationId:  orgID,
			AssetId:         assetID,
		})
		if lastErr == nil {
			break
		}
		s.logger.Warn("Self-enrollment attempt failed",
			zap.Int("attempt", attempt), zap.Error(lastErr))
		if attempt < 3 {
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}
	if lastErr != nil {
		s.logger.Error("Self-enrollment from staged token failed; run 'wendy device enroll' to retry",
			zap.Error(lastErr))
	} else {
		s.logger.Info("Self-enrolled from staged token",
			zap.Int32("org_id", orgID), zap.Int32("asset_id", assetID))
	}
	s.removeEnrollmentFile(path)
}

func (s *ProvisioningService) removeEnrollmentFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.logger.Warn("Failed to remove enrollment file", zap.String("path", path), zap.Error(err))
	}
}
