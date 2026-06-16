package containerd

import (
	"strings"
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

func TestMergeContainerEnvImagePathWins(t *testing.T) {
	imageEnv := []string{"PATH=/usr/share/grafana/bin:/usr/bin", "GF_X=1"}
	reqEnv := []string{}
	wendyEnv := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm",
		"WENDY_APP_ID=grafana",
	}
	got := mergeContainerEnv(imageEnv, reqEnv, wendyEnv)

	// The image PATH must be the only PATH (Wendy's default dropped), so the
	// entrypoint can find binaries on the image's custom PATH.
	var paths []string
	var hasWendyID, hasTerm bool
	for _, e := range got {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			paths = append(paths, v)
		}
		if e == "WENDY_APP_ID=grafana" {
			hasWendyID = true
		}
		if e == "TERM=xterm" {
			hasTerm = true
		}
	}
	if len(paths) != 1 || paths[0] != "/usr/share/grafana/bin:/usr/bin" {
		t.Errorf("PATH entries = %v; want exactly the image PATH", paths)
	}
	if !hasWendyID {
		t.Error("WENDY_APP_ID must still be injected")
	}
	if !hasTerm {
		t.Error("default TERM should remain when the image does not set it")
	}
}

func TestMergeContainerEnvDefaultsWhenImageHasNone(t *testing.T) {
	got := mergeContainerEnv(nil, nil, []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"WENDY_APP_ID=x",
	})
	var hasPath bool
	for _, e := range got {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
		}
	}
	if !hasPath {
		t.Error("default PATH should be present when the image provides none")
	}
}
