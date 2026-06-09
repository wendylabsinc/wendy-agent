package providers

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestLocalProviderSupportsOnlyNativeBuildTypes(t *testing.T) {
	p := &LocalProvider{}

	for _, unsupported := range []string{"docker", "compose"} {
		if slices.Contains(p.SupportedBuildTypes(), unsupported) {
			t.Fatalf("LocalProvider supports %q, want only host-native build types", unsupported)
		}
	}
	for _, supported := range []string{"swift", "go", "python"} {
		if !slices.Contains(p.SupportedBuildTypes(), supported) {
			t.Fatalf("LocalProvider does not support %q", supported)
		}
	}
}

func TestLocalProviderDoesNotClaimDockerfileProjects(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &LocalProvider{}
	if p.CanBuild(dir) {
		t.Fatal("LocalProvider.CanBuild() = true for Dockerfile-only project, want false")
	}

	_, err := p.Build(context.Background(), models.ExternalDevice{ID: "local", ProviderKey: p.Key()}, dir, "app", false)
	if err == nil {
		t.Fatal("LocalProvider.Build() succeeded for Dockerfile-only project, want error")
	}
	if !strings.Contains(err.Error(), "cannot determine build method") {
		t.Fatalf("LocalProvider.Build() error = %q, want cannot determine build method", err)
	}
}
