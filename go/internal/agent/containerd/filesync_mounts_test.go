package containerd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	localoci "github.com/wendylabsinc/wendy/go/internal/agent/oci"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestApplyFileSyncMounts_MountsDeclaredFilesAtWorkingDirDestinations(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "com.example.app")
	if err := os.MkdirAll(filepath.Join(appDir, "public", "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "public", "assets", "logo.txt"), []byte("logo"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := fileSyncAppDir
	fileSyncAppDir = func(appID string) (string, error) { return filepath.Join(root, appID), nil }
	t.Cleanup(func() { fileSyncAppDir = orig })

	spec := localoci.DefaultSpec("rootfs", []string{"/bin/app"})
	err := applyFileSyncMounts(spec, "com.example.app", "/app", []appconfig.FileSyncEntry{
		{Path: "config.json"},
		{Path: "assets", To: "public/assets"},
	})
	if err != nil {
		t.Fatalf("applyFileSyncMounts: %v", err)
	}

	mounts := map[string]localoci.Mount{}
	for _, m := range spec.Mounts {
		mounts[m.Destination] = m
	}
	if got := mounts["/app/config.json"].Source; got != filepath.Join(appDir, "config.json") {
		t.Fatalf("config source = %q", got)
	}
	assets := mounts["/app/public/assets"]
	if assets.Source != filepath.Join(appDir, "public", "assets") {
		t.Fatalf("assets source = %q", assets.Source)
	}
	if !slices.Contains(assets.Options, "rbind") || !slices.Contains(assets.Options, "ro") {
		t.Fatalf("assets options = %v, want rbind and ro", assets.Options)
	}
}

func TestContainerFileSyncDestination_RejectsEscapes(t *testing.T) {
	if _, err := containerFileSyncDestination("/app", "../etc/passwd"); err == nil {
		t.Fatal("expected .. destination to be rejected")
	}
	got, err := containerFileSyncDestination("app", "./config.json")
	if err != nil {
		t.Fatalf("containerFileSyncDestination: %v", err)
	}
	if got != "/app/config.json" {
		t.Fatalf("destination = %q, want /app/config.json", got)
	}
}
