package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildxLocalCacheDir(t *testing.T) {
	const userCache = "/home/u/.cache"
	base := filepath.Join(userCache, "wendy", "buildx")

	tests := []struct {
		name     string
		cacheKey string
		want     string
	}{
		{"empty key uses shared base dir", "", base},
		{"service key gets isolated subdir", "myapp-gpu", filepath.Join(base, "myapp-gpu")},
		{"distinct services get distinct dirs", "myapp-vui", filepath.Join(base, "myapp-vui")},
		{"unsafe chars are sanitized", "My/App:1", filepath.Join(base, "my-app-1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildxLocalCacheDir(userCache, tt.cacheKey); got != tt.want {
				t.Fatalf("buildxLocalCacheDir(%q) = %q, want %q", tt.cacheKey, got, tt.want)
			}
		})
	}

	// The core WDY-1689 invariant: two different concurrent service builds never
	// resolve to the same local cache dir.
	a := buildxLocalCacheDir(userCache, "app-a")
	b := buildxLocalCacheDir(userCache, "app-b")
	if a == b {
		t.Fatalf("distinct cache keys collided on %q", a)
	}
}

func TestBuildxArgsRequestPlainProgress(t *testing.T) {
	// Both buildx arg builders must request --progress=plain so the CLI can
	// parse a deterministic format. Guard against accidental removal.
	for _, f := range []string{"docker.go", "ocilayers.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !strings.Contains(string(src), `"--progress", "plain"`) {
			t.Errorf("%s: expected buildx args to include --progress plain", f)
		}
	}
}

func TestAppleContainerBuildRequestsPlainProgress(t *testing.T) {
	// Apple Container builds must also request --progress=plain so their output
	// renders through the shared build progress UI (default --progress auto emits
	// an interactive [+] Building UI the build parser cannot read). The adjacent
	// "build", "--progress", "plain" tokens are unique to the apple-container arg
	// builders (buildx prepends "buildx", "build"). Guard against accidental removal.
	for _, f := range []string{"docker.go", "ocilayers.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !strings.Contains(string(src), `"build", "--progress", "plain"`) {
			t.Errorf("%s: expected apple container build args to include --progress plain", f)
		}
	}
}
