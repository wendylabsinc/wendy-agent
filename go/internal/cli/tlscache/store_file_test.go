package tlscache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreRoundTrip(t *testing.T) {
	s := &fileStore{dir: filepath.Join(t.TempDir(), "tls-sessions")}
	if got := s.Get("k1"); got != nil {
		t.Fatalf("Get on empty store = %q, want nil", got)
	}
	if err := s.Put("k1", []byte("blob-1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := string(s.Get("k1")); got != "blob-1" {
		t.Fatalf("Get after Put = %q, want blob-1", got)
	}
	if err := s.Put("k1", []byte("blob-2")); err != nil { // overwrite
		t.Fatalf("Put overwrite: %v", err)
	}
	if got := string(s.Get("k1")); got != "blob-2" {
		t.Fatalf("Get after overwrite = %q, want blob-2", got)
	}
	s.Delete("k1")
	if got := s.Get("k1"); got != nil {
		t.Fatalf("Get after Delete = %q, want nil", got)
	}
	s.Delete("k1") // deleting a missing key must not panic
}

func TestFileStorePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls-sessions")
	s := &fileStore{dir: dir}
	if err := s.Put("k1", []byte("secret")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
	fi, err := os.Stat(filepath.Join(dir, "k1.tlssession"))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
}

func TestFileStorePrunesOldSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls-sessions")
	s := &fileStore{dir: dir}
	if err := s.Put("old", []byte("stale")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	oldPath := filepath.Join(dir, "old.tlssession")
	past := time.Now().Add(-sessionFileMaxAge - time.Hour)
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := s.Put("fresh", []byte("new")); err != nil { // Put triggers pruning
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("expected old session pruned, stat err = %v", err)
	}
	if got := string(s.Get("fresh")); got != "new" {
		t.Errorf("fresh session lost by pruning: %q", got)
	}
}

func TestFileStoreNoTempFileLeftovers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls-sessions")
	s := &fileStore{dir: dir}
	if err := s.Put("k1", []byte("blob")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "k1.tlssession" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir contents = %v, want exactly [k1.tlssession]", names)
	}
}
