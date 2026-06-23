package commands

import (
	"path/filepath"
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
