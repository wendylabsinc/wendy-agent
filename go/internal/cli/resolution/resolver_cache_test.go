package resolution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// writeTestConfig writes a config.json under a temp home dir and sets $HOME so
// config.Load() picks it up. It returns a cleanup function.
func writeTestConfig(t *testing.T, cfg *config.Config) func() {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".wendy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	orig := os.Getenv("HOME")
	os.Setenv("HOME", home)
	return func() { os.Setenv("HOME", orig) }
}

func TestResolveCache_Match(t *testing.T) {
	cleanup := writeTestConfig(t, &config.Config{
		DefaultDevice:         "mydevice.local",
		DefaultDeviceEndpoint: "192.168.1.50:50051",
	})
	defer cleanup()

	candidates, result := resolveCache("mydevice.local", 50051)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d (result: %s)", len(candidates), result)
	}
	if candidates[0].Source != SourceCache {
		t.Errorf("expected SourceCache, got %v", candidates[0].Source)
	}
	if candidates[0].IP.String() != "192.168.1.50" {
		t.Errorf("unexpected IP: %s", candidates[0].IP)
	}
	if candidates[0].Port != 50051 {
		t.Errorf("unexpected port: %d", candidates[0].Port)
	}
}

func TestResolveCache_DifferentHost(t *testing.T) {
	cleanup := writeTestConfig(t, &config.Config{
		DefaultDevice:         "otherdevice.local",
		DefaultDeviceEndpoint: "192.168.1.50:50051",
	})
	defer cleanup()

	candidates, result := resolveCache("mydevice.local", 50051)
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates for different host, got %d", len(candidates))
	}
	if result != "none (different host)" {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestResolveCache_EmptyEndpoint(t *testing.T) {
	cleanup := writeTestConfig(t, &config.Config{
		DefaultDevice:         "mydevice.local",
		DefaultDeviceEndpoint: "",
	})
	defer cleanup()

	candidates, result := resolveCache("mydevice.local", 50051)
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates when endpoint empty, got %d", len(candidates))
	}
	if result != "none" {
		t.Errorf("unexpected result: %s", result)
	}
}
