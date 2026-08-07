package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// The multi-service deploy path once dropped injected env entirely: it built
// each CreateContainerRequest from the wendy.json env alone, so --env and the
// per-device identity a fleet deploy computes never reached a service
// container. These tests pin both halves of the fix, the request env and the
// build fingerprint that decides whether a service is redeployed at all.

func TestServiceDeployEnvAppendsInjectedEnv(t *testing.T) {
	appCfg := &appconfig.AppConfig{Env: map[string]string{"A": "1", "B": "2"}}
	svc := &appconfig.ServiceConfig{Env: map[string]string{"B": "3"}}

	got := serviceDeployEnv(appCfg, svc, []string{"B=4", "C=5"})

	// Service env overrides app env, then injected entries come last so they
	// win on a key clash (the agent applies the list in order).
	want := []string{"A=1", "B=3", "B=4", "C=5"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestServiceDeployEnvWithoutInjectedEnvMatchesServiceEnv(t *testing.T) {
	appCfg := &appconfig.AppConfig{Env: map[string]string{"A": "1"}}
	svc := &appconfig.ServiceConfig{Env: map[string]string{"B": "2"}}

	plain := expandServiceEnv(appCfg, svc)
	got := serviceDeployEnv(appCfg, svc, nil)
	if len(got) != len(plain) {
		t.Fatalf("got %v, want %v", got, plain)
	}
	for i := range plain {
		if got[i] != plain[i] {
			t.Fatalf("entry %d: got %q, want %q", i, got[i], plain[i])
		}
	}
}

func TestBuildInputHashChangesWithInjectedEnv(t *testing.T) {
	dir := t.TempDir()
	// The Dockerfile argument is resolved inside the context directory, so pass
	// the bare name rather than an absolute path.
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	base, err := computeBuildInputHash(dir, "Dockerfile", "linux/arm64", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	withEnv, err := computeBuildInputHash(dir, "Dockerfile", "linux/arm64", nil, []string{"WT_FLEET_TOKEN=abc"})
	if err != nil {
		t.Fatal(err)
	}
	// Injected env applies at container create, so a change to it must not be
	// skipped as "unchanged since the last push".
	if base == withEnv {
		t.Fatal("build input hash ignored injected env; a per-device env change would be skipped as unchanged")
	}
}
