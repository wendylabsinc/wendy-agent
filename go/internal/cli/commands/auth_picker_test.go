//go:build darwin || linux || windows

package commands

import (
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
	// Name now shows the org label ("org 7 — prod:443") so each row is
	// identified by org, not dashboard URL.
	if items[0].Name != "org 7 — prod:443" {
		t.Errorf("item 0 name = %q", items[0].Name)
	}
	// Description shows the dashboard URL for environment context.
	if items[0].Description != "https://cloud.wendy.sh" {
		t.Errorf("item 0 desc = %q", items[0].Description)
	}
	// DedupKey and Value include the org ID so two orgs on the same endpoint
	// are represented as separate rows.
	if items[0].Value.(string) != "prod:443::7" || items[0].DedupKey != "prod:443::7" {
		t.Errorf("item 0 value/dedup wrong: %+v", items[0])
	}
	// Session with no dashboard: Name is the session label, Description is endpoint.
	if items[1].Name != "org 1 — local:50051" {
		t.Errorf("item 1 name = %q", items[1].Name)
	}
}
