package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestFindCertByOrgID(t *testing.T) {
	auth := []config.AuthConfig{
		{Certificates: []config.CertificateInfo{
			{OrganizationID: 5, PemCertificate: "cert5"},
		}},
		{Certificates: []config.CertificateInfo{
			{OrganizationID: 7, PemCertificate: "cert7a"},
			{OrganizationID: 7, PemCertificate: "cert7b"},
		}},
	}

	got := findCertByOrgID(auth, 7)
	if got == nil {
		t.Fatal("findCertByOrgID(7) = nil, want non-nil")
	}
	if got.PemCertificate != "cert7a" {
		t.Errorf("PemCertificate = %q, want %q", got.PemCertificate, "cert7a")
	}

	if got := findCertByOrgID(auth, 99); got != nil {
		t.Errorf("findCertByOrgID(99) = %v, want nil", got)
	}
}
