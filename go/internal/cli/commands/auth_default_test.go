//go:build darwin || linux || windows

package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func selectorConfig() *config.Config {
	return &config.Config{Auth: []config.AuthConfig{
		{CloudDashboard: "https://cloud.wendy.sh", CloudGRPC: "prod.example.com:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}},
		{CloudDashboard: "http://localhost:3000", CloudGRPC: "localhost:50051", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
	}}
}

func TestMatchAuthSelectorByOrgID(t *testing.T) {
	a, err := matchAuthSelector(selectorConfig(), "7")
	if err != nil || a.CloudGRPC != "prod.example.com:443" {
		t.Fatalf("org match failed: %v / %v", a, err)
	}
}

func TestMatchAuthSelectorByEndpointSubstring(t *testing.T) {
	a, err := matchAuthSelector(selectorConfig(), "localhost")
	if err != nil || a.CloudGRPC != "localhost:50051" {
		t.Fatalf("substring match failed: %v / %v", a, err)
	}
}

func TestMatchAuthSelectorNoMatch(t *testing.T) {
	if _, err := matchAuthSelector(selectorConfig(), "nope"); err == nil || !strings.Contains(err.Error(), "no auth session matches") {
		t.Fatalf("want no-match error, got %v", err)
	}
}

func TestMatchAuthSelectorAmbiguous(t *testing.T) {
	cfg := selectorConfig()
	cfg.Auth[1].Certificates[0].OrganizationID = 7 // two sessions in org 7
	_, err := matchAuthSelector(cfg, "7")
	if err == nil || !strings.Contains(err.Error(), "matches multiple sessions") {
		t.Fatalf("want ambiguous error, got %v", err)
	}
	// The error lists each candidate so the user can disambiguate.
	if !strings.Contains(err.Error(), "prod.example.com:443") || !strings.Contains(err.Error(), "localhost:50051") {
		t.Fatalf("ambiguous error should list candidates, got %v", err)
	}
}
