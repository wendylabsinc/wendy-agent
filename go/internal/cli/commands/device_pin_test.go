package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// TestCloudGRPCForOrg maps the org carried by a verifying mTLS cert back to the
// cloud host of the auth session that issued it — the cloud half of the
// WDY-1149 (org, cloud) pin.
func TestCloudGRPCForOrg(t *testing.T) {
	cfg := &config.Config{Auth: []config.AuthConfig{
		{CloudGRPC: "grpc.a.sh:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}},
		{CloudGRPC: "grpc.b.sh:443", Certificates: []config.CertificateInfo{{OrganizationID: 9}}},
	}}

	if got := cloudGRPCForOrg(cfg, 9); got != "grpc.b.sh:443" {
		t.Errorf("org 9: got %q, want grpc.b.sh:443", got)
	}
	if got := cloudGRPCForOrg(cfg, 7); got != "grpc.a.sh:443" {
		t.Errorf("org 7: got %q, want grpc.a.sh:443", got)
	}
	if got := cloudGRPCForOrg(cfg, 42); got != "" {
		t.Errorf("unknown org: got %q, want empty", got)
	}
}
