package config

import (
	"errors"
	"strings"
	"testing"
)

func twoSessions() *Config {
	return &Config{Auth: []AuthConfig{
		{CloudDashboard: "https://cloud.wendy.sh", CloudGRPC: "prod:443", Certificates: []CertificateInfo{{OrganizationID: 7}}},
		{CloudDashboard: "http://localhost:3000", CloudGRPC: "localhost:50051", Certificates: []CertificateInfo{{OrganizationID: 1}}},
	}}
}

func TestResolveAuthNotLoggedIn(t *testing.T) {
	if _, err := ResolveAuth(&Config{}, "", nil); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
}

func TestResolveAuthFlagWins(t *testing.T) {
	cfg := twoSessions()
	cfg.DefaultCloudGRPC = "prod:443"
	auth, err := ResolveAuth(cfg, "localhost:50051", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("flag must win, got %s", auth.CloudGRPC)
	}
}

func TestResolveAuthFlagNoMatch(t *testing.T) {
	_, err := ResolveAuth(twoSessions(), "missing:443", nil)
	if err == nil || !strings.Contains(err.Error(), "no auth session for missing:443") {
		t.Fatalf("want no-session error, got %v", err)
	}
}

func TestResolveAuthSingleSession(t *testing.T) {
	cfg := &Config{Auth: []AuthConfig{{CloudGRPC: "prod:443", Certificates: []CertificateInfo{{OrganizationID: 7}}}}}
	auth, err := ResolveAuth(cfg, "", nil)
	if err != nil || auth.CloudGRPC != "prod:443" {
		t.Fatalf("single session should resolve, got %v / %v", auth, err)
	}
}

func TestResolveAuthSingleSessionNoCerts(t *testing.T) {
	cfg := &Config{Auth: []AuthConfig{{CloudGRPC: "prod:443"}}}
	if _, err := ResolveAuth(cfg, "", nil); err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Fatalf("want no-certificates error, got %v", err)
	}
}

func TestResolveAuthValidDefault(t *testing.T) {
	cfg := twoSessions()
	cfg.DefaultCloudGRPC = "localhost:50051"
	auth, err := ResolveAuth(cfg, "", nil)
	if err != nil || auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("default should be used, got %v / %v", auth, err)
	}
}

func TestResolveAuthStaleDefaultFallsThrough(t *testing.T) {
	cfg := twoSessions()
	cfg.DefaultCloudGRPC = "gone:443"
	if _, err := ResolveAuth(cfg, "", nil); !errors.Is(err, ErrMultipleSessions) {
		t.Fatalf("stale default should fall through to ErrMultipleSessions, got %v", err)
	}
}

func TestResolveAuthMultipleNoPicker(t *testing.T) {
	err := func() error { _, e := ResolveAuth(twoSessions(), "", nil); return e }()
	if !errors.Is(err, ErrMultipleSessions) {
		t.Fatalf("want ErrMultipleSessions, got %v", err)
	}
	if !strings.Contains(err.Error(), "--cloud-grpc") {
		t.Fatalf("message must mention --cloud-grpc, got %v", err)
	}
}

func TestResolveAuthMultipleUsesPicker(t *testing.T) {
	cfg := twoSessions()
	called := false
	pick := func(c *Config) (*AuthConfig, error) { called = true; return &c.Auth[1], nil }
	auth, err := ResolveAuth(cfg, "", pick)
	if err != nil || !called || auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("picker should be used, got %v / called=%v / %v", auth, called, err)
	}
}

func TestResolveAuthPickerResultCertValidated(t *testing.T) {
	cfg := twoSessions()
	cfg.Auth[1].Certificates = nil // picker returns a cert-less session
	pick := func(c *Config) (*AuthConfig, error) { return &c.Auth[1], nil }
	if _, err := ResolveAuth(cfg, "", pick); err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Fatalf("want no-certificates error from picker result, got %v", err)
	}
}

func TestDefaultAuthLookup(t *testing.T) {
	cfg := twoSessions()
	if _, ok := cfg.DefaultAuth(); ok {
		t.Fatal("no default set should return ok=false")
	}
	cfg.DefaultCloudGRPC = "prod:443"
	a, ok := cfg.DefaultAuth()
	if !ok || a.CloudGRPC != "prod:443" {
		t.Fatalf("want prod session, got %v / %v", a, ok)
	}
	cfg.DefaultCloudGRPC = "gone:443"
	if _, ok := cfg.DefaultAuth(); ok {
		t.Fatal("stale default should return ok=false")
	}
}
