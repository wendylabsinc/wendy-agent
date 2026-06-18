//go:build darwin || linux || windows

package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestAuthSessionLabel(t *testing.T) {
	withOrg := &config.AuthConfig{CloudGRPC: "prod:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}}
	if got := authSessionLabel(withOrg); got != "org 7 — prod:443" {
		t.Fatalf("got %q", got)
	}
	noCerts := &config.AuthConfig{CloudGRPC: "local:50051"}
	if got := authSessionLabel(noCerts); got != "local:50051" {
		t.Fatalf("got %q", got)
	}
}

func TestAuthPickerItems(t *testing.T) {
	cfg := &config.Config{Auth: []config.AuthConfig{
		{CloudDashboard: "https://cloud.wendy.sh", CloudGRPC: "prod:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}},
		{CloudGRPC: "local:50051", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
	}}
	items := authPickerItems(cfg)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Name != "https://cloud.wendy.sh" {
		t.Errorf("item 0 name = %q", items[0].Name)
	}
	if !strings.Contains(items[0].Description, "org 7") {
		t.Errorf("item 0 desc = %q", items[0].Description)
	}
	if items[0].Value.(string) != "prod:443" || items[0].DedupKey != "prod:443" {
		t.Errorf("item 0 value/dedup wrong: %+v", items[0])
	}
	// Session with no dashboard falls back to its endpoint for the Name column.
	if items[1].Name != "local:50051" {
		t.Errorf("item 1 name = %q", items[1].Name)
	}
}
