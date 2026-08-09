package tlscache

import (
	"os"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/secretstore"
)

// sessionFileMaxAge matches crypto/tls's maxSessionTicketLifetime: a ticket
// older than 7 days can never resume, so its file is dead weight.
const sessionFileMaxAge = 7 * 24 * time.Hour

// fileStore keeps one 0600 file per session under a 0700 directory. It is the
// default backend on platforms without a secret store; the client's ML-DSA
// private key already lives unencrypted in ~/.wendy/config.json there, so a
// 0600 ticket file adds no new exposure class.
type fileStore struct{ dir string }

func newFileStore() secretstore.Store {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil
	}
	return &fileStore{dir: filepath.Join(dir, "tls-sessions")}
}

func (s *fileStore) path(key string) string {
	return filepath.Join(s.dir, key+".tlssession")
}

func (s *fileStore) Get(key string) []byte {
	blob, err := os.ReadFile(s.path(key))
	if err != nil {
		return nil
	}
	return blob
}

func (s *fileStore) Put(key string, blob []byte) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	// Atomic replace: concurrent CLI processes are last-writer-wins, and a
	// reader never observes a partial file.
	tmp, err := os.CreateTemp(s.dir, key+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path(key)); err != nil {
		os.Remove(tmpName)
		return err
	}
	s.prune()
	return nil
}

func (s *fileStore) Delete(key string) {
	os.Remove(s.path(key))
}

// prune drops session files old enough that their tickets can no longer
// resume. Best-effort by design.
func (s *fileStore) prune() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-sessionFileMaxAge)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".tlssession" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}
