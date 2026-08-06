package lock

import (
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsNilNil(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f != nil {
		t.Fatalf("expected nil file for a missing lockfile, got %+v", f)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.stagefile.lock.yaml")
	original := &File{
		Version:    1,
		SourceHash: "sha256:abc123",
		Images:     map[string]string{"debian:12": "sha256:deadbeef"},
	}
	if err := original.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.SourceHash != original.SourceHash {
		t.Fatalf("SourceHash = %q, want %q", loaded.SourceHash, original.SourceHash)
	}
	if loaded.Images["debian:12"] != "sha256:deadbeef" {
		t.Fatalf("Images = %+v", loaded.Images)
	}
}
