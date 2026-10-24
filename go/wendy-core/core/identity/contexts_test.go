package identity

import "testing"

func certOrg(org int) []Certificate { return []Certificate{{OrganizationID: org}} }

func TestEnsureContextsFirstIsDefault(t *testing.T) {
	cfg := &SessionSet{Auth: []Session{
		{CloudGRPC: "prod:443", Certificates: certOrg(7)},
		{CloudGRPC: "dev:50051", Certificates: certOrg(1)},
	}}
	ensureContexts(cfg)
	if cfg.Auth[0].Name != "default" {
		t.Fatalf("first context must be default, got %q", cfg.Auth[0].Name)
	}
	if cfg.Auth[1].Name != "org-1" {
		t.Fatalf("second context name = %q, want org-1", cfg.Auth[1].Name)
	}
	// Several contexts, none chosen -> CurrentContext stays empty.
	if cfg.CurrentContext != "" {
		t.Fatalf("CurrentContext should be empty with several unselected contexts, got %q", cfg.CurrentContext)
	}
}

func TestEnsureContextsSingleSessionBecomesCurrent(t *testing.T) {
	cfg := &SessionSet{Auth: []Session{{CloudGRPC: "prod:443", Certificates: certOrg(7)}}}
	ensureContexts(cfg)
	if cfg.Auth[0].Name != "default" || cfg.CurrentContext != "default" {
		t.Fatalf("single session should be the current default, got name=%q current=%q", cfg.Auth[0].Name, cfg.CurrentContext)
	}
}

func TestEnsureContextsMigratesLegacyDefault(t *testing.T) {
	cfg := &SessionSet{
		Auth: []Session{
			{CloudGRPC: "prod:443", Certificates: certOrg(7)},
			{CloudGRPC: "dev:50051", Certificates: certOrg(1)},
		},
		DefaultCloudGRPC: "dev:50051",
	}
	ensureContexts(cfg)
	// The legacy default pointed at the dev session (org 1), which is now "org-1".
	if cfg.CurrentContext != "org-1" {
		t.Fatalf("CurrentContext = %q, want org-1 (migrated from DefaultCloudGRPC)", cfg.CurrentContext)
	}
}

func TestEnsureContextsMigratesLegacyDefaultOrgOnSharedEndpoint(t *testing.T) {
	cfg := &SessionSet{
		Auth: []Session{
			{CloudGRPC: "prod:443", Certificates: certOrg(9)},
			{CloudGRPC: "prod:443", Certificates: certOrg(75)},
		},
		DefaultOrgID: 75,
	}
	ensureContexts(cfg)
	if cfg.CurrentContext != "org-75" {
		t.Fatalf("CurrentContext = %q, want org-75 (migrated from DefaultOrgID)", cfg.CurrentContext)
	}
}

func TestEnsureContextsDedupesDerivedNames(t *testing.T) {
	issuer := "https://auth.wendy.sh/realms/acme"
	cfg := &SessionSet{Auth: []Session{
		{CloudGRPC: "prod:443", Certificates: certOrg(7)},
		{CloudGRPC: "prod:443", OAuthIssuer: issuer},
		{CloudGRPC: "prod:443", OAuthIssuer: issuer},
	}}
	ensureContexts(cfg)
	if cfg.Auth[1].Name != "acme" || cfg.Auth[2].Name != "acme-2" {
		t.Fatalf("dedup failed: %q, %q", cfg.Auth[1].Name, cfg.Auth[2].Name)
	}
}

func TestEnsureContextsIdempotent(t *testing.T) {
	cfg := &SessionSet{
		Auth: []Session{
			{CloudGRPC: "prod:443", Certificates: certOrg(7)},
			{CloudGRPC: "dev:50051", Certificates: certOrg(1)},
		},
		DefaultCloudGRPC: "dev:50051",
	}
	ensureContexts(cfg)
	name0, name1, cur := cfg.Auth[0].Name, cfg.Auth[1].Name, cfg.CurrentContext
	ensureContexts(cfg)
	if cfg.Auth[0].Name != name0 || cfg.Auth[1].Name != name1 || cfg.CurrentContext != cur {
		t.Fatalf("ensureContexts not idempotent: %q/%q/%q -> %q/%q/%q",
			name0, name1, cur, cfg.Auth[0].Name, cfg.Auth[1].Name, cfg.CurrentContext)
	}
}

func TestResolveAuthByCurrentContext(t *testing.T) {
	cfg := &SessionSet{
		Auth: []Session{
			{CloudGRPC: "prod:443", Certificates: certOrg(7)},
			{CloudGRPC: "dev:50051", Certificates: certOrg(1)},
		},
		CurrentContext: "org-1",
	}
	// Names not yet assigned; ResolveSession's ensureContexts must assign them so
	// "org-1" resolves.
	auth, err := ResolveSession(cfg, "", nil)
	if err != nil || auth.CloudGRPC != "dev:50051" {
		t.Fatalf("current context should resolve to dev session, got %v / %v", auth, err)
	}
}

func TestContextByName(t *testing.T) {
	cfg := &SessionSet{Auth: []Session{
		{Name: "default", CloudGRPC: "prod:443"},
		{Name: "acme", CloudGRPC: "dev:50051"},
	}}
	if a, ok := cfg.ContextByName("acme"); !ok || a.CloudGRPC != "dev:50051" {
		t.Fatalf("ContextByName(acme) = %v / %v", a, ok)
	}
	if _, ok := cfg.ContextByName("missing"); ok {
		t.Fatal("ContextByName(missing) should be false")
	}
}
