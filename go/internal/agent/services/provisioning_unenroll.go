package services

import (
	"context"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The prerelease ACME revocation RPCs are retired, not alternate orchestrators.
func (s *ProvisioningServiceV2) RevokeACMECertificate(context.Context, *agentpbv2.RevokeACMECertificateRequest) (*agentpbv2.RevokeACMECertificateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "Cloud owns device unenrollment and certificate revocation")
}
func (s *ProvisioningServiceV2) CheckACMERevocation(context.Context, *agentpbv2.RevokeACMECertificateRequest) (*agentpbv2.RevokeACMECertificateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "use Cloud binding preflight; no Agent ACME revocation path")
}
