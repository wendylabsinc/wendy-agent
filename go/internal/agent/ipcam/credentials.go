package ipcam

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Credential is a camera login. Network cameras need one and local cameras do
// not, which is the single irreducible difference in how the two are used.
type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CredentialStore holds camera logins on the device, keyed by MAC so they stay
// attached to the physical camera across address changes.
//
// The file is mode 0600 in the agent's root-owned state directory, and
// credentials are never returned across gRPC: the video service reports only
// whether a camera has them. Keeping them here rather than in an application
// image is the point, since an image is copied to a registry and to every
// device that runs the app.
type CredentialStore struct {
	path string

	mu sync.Mutex
	by map[string]Credential
}

// NewCredentialStore returns a store backed by path. Call Load before use.
func NewCredentialStore(path string) *CredentialStore {
	return &CredentialStore{path: path, by: make(map[string]Credential)}
}

// Load reads the store. A missing file is the normal initial state.
func (s *CredentialStore) Load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading camera credentials: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.Unmarshal(data, &s.by); err != nil {
		return fmt.Errorf("parsing camera credentials: %w", err)
	}
	if s.by == nil {
		s.by = make(map[string]Credential)
	}
	return nil
}

// save writes the store atomically at 0600. Callers hold s.mu.
func (s *CredentialStore) save() error {
	data, err := json.MarshalIndent(s.by, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding camera credentials: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating credential directory: %w", err)
	}
	// The temp file is written at 0600 too: it holds the same secret, and a
	// rename carries the mode of the file being renamed, not the destination's.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing camera credentials: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replacing camera credentials: %w", err)
	}
	return nil
}

// Set stores the login for a camera, replacing any existing one.
func (s *CredentialStore) Set(mac string, c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.by[mac]
	s.by[mac] = c
	if err := s.save(); err != nil {
		if existed {
			s.by[mac] = previous
		} else {
			delete(s.by, mac)
		}
		return err
	}
	return nil
}

// Get returns the stored login for a camera.
func (s *CredentialStore) Get(mac string) (Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.by[mac]
	return c, ok
}

// Has reports whether a login is stored, without exposing it.
func (s *CredentialStore) Has(mac string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.by[mac]
	return ok
}

// Delete removes a credential. Deleting an unknown MAC succeeds: the caller
// wanted no credential stored, and there is none.
func (s *CredentialStore) Delete(mac string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.by[mac]
	if !existed {
		return nil
	}
	delete(s.by, mac)
	if err := s.save(); err != nil {
		s.by[mac] = previous
		return err
	}
	return nil
}
