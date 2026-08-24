package lock

import (
	"os"
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
		ManagedBases: map[string]ManagedBase{
			"debian": {Ref: "debian:12", Revision: 3},
		},
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
	if loaded.ManagedBases["debian"] != original.ManagedBases["debian"] {
		t.Fatalf("ManagedBases = %+v", loaded.ManagedBases)
	}
}

func TestSaveSkipsUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.stagefile.lock.yaml")
	f := &File{Version: 1, SourceHash: "sha256:abc", Images: map[string]string{"debian:12": "sha256:def"}}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("unchanged lockfile was rewritten (mtime churn re-triggers wendy watch)")
	}
	f.SourceHash = "sha256:changed"
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SourceHash != "sha256:changed" {
		t.Fatalf("SourceHash = %q", reloaded.SourceHash)
	}
}
