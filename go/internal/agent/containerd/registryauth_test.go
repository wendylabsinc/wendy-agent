package containerd

import (
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestRegistryHostMatches(t *testing.T) {
	tests := []struct {
		name, requested, configured string
		want                        bool
	}{
		{"docker hub canonical", "registry-1.docker.io", "docker.io", true},
		{"docker hub index alias", "index.docker.io", "docker.io", true},
		{"exact match", "ghcr.io", "ghcr.io", true},
		{"case-insensitive", "GHCR.IO", "ghcr.io", true},
		{"empty configured matches nothing", "ghcr.io", "", false},
		{"mismatch", "ghcr.io", "docker.io", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := registryHostMatches(tt.requested, tt.configured); got != tt.want {
				t.Errorf("registryHostMatches(%q,%q)=%v want %v", tt.requested, tt.configured, got, tt.want)
			}
		})
	}
}

func TestAuthorizerResolver(t *testing.T) {
	// No auth (or empty creds) -> nil so the caller falls back to anonymous.
	if r := authorizerResolver(nil); r != nil {
		t.Error("expected nil resolver for nil auth")
	}
	if r := authorizerResolver(&agentpb.RegistryAuth{RegistryHost: "ghcr.io"}); r != nil {
		t.Error("expected nil resolver when username and password are both empty")
	}

	// Auth present -> a usable resolver.
	if r := authorizerResolver(&agentpb.RegistryAuth{
		RegistryHost: "ghcr.io",
		Username:     "alice",
		Password:     "s3cret",
	}); r == nil {
		t.Error("expected a resolver when credentials are present")
	}
}
