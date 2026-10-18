// Package trustedkeys is the device's list of local operator keys: public keys
// whose self-issued client certificates the agent accepts without a CA chain,
// each with one of three roles. It is the non-cloud access model (WDY-3882):
// `wendy os install` seeds the installing operator's key as the first owner,
// owners add further keys. pki-core is not involved.
//
// An absent file means "no local operators", never "allow all".
package trustedkeys

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/atomicfile"
	"github.com/wendylabsinc/wendy/go/internal/shared/flock"
)

// AddedByInstall is the added_by value of the key seeded by `wendy os install`.
const AddedByInstall = "install"

// Role is ordered: a higher role includes every right of the lower ones.
type Role int

const (
	RoleNone Role = iota
	RoleViewer
	RoleOperator
	RoleOwner
)

func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleOperator:
		return "operator"
	case RoleOwner:
		return "owner"
	default:
		return "none"
	}
}

// ParseRole accepts the three role names; anything else is an error.
func ParseRole(s string) (Role, error) {
	switch s {
	case "viewer":
		return RoleViewer, nil
	case "operator":
		return RoleOperator, nil
	case "owner":
		return RoleOwner, nil
	}
	return RoleNone, fmt.Errorf("unknown role %q (want owner, operator or viewer)", s)
}

var (
	ErrNotFound      = errors.New("trusted key not found")
	ErrAlreadyExists = errors.New("key is already trusted")
	ErrLastOwner     = errors.New("cannot remove the last owner key")
	ErrActorNotOwner = errors.New("acting key is no longer a trusted owner")
)

// Entry is one trusted key as stored on disk.
type Entry struct {
	// PublicKey is the SubjectPublicKeyInfo as PEM ("PUBLIC KEY").
	PublicKey string    `json:"public_key"`
	Role      string    `json:"role"`
	Label     string    `json:"label,omitempty"`
	AddedAt   time.Time `json:"added_at"`
	// AddedBy is the fingerprint of the key that added this one, or "install".
	AddedBy string `json:"added_by"`

	spki []byte
	role Role
}

// SPKI returns the SubjectPublicKeyInfo DER.
func (e Entry) SPKI() []byte { return e.spki }

// Fingerprint is "sha256:" + hex SHA-256 of the SPKI DER — the same format as
// the CLI's device pin store.
func (e Entry) Fingerprint() string { return Fingerprint(e.spki) }

type fileFormat struct {
	Keys []Entry `json:"keys"`
}

// Store is the loaded file. It is safe for concurrent use.
type Store struct {
	path    string
	mu      sync.RWMutex
	present bool
	entries []Entry
}

// SelfSigned reports whether leaf is signed by its own key — the shape of a
// local operator certificate. The mTLS listener accepts a local operator
// certificate only when it is self-signed, so a self-signed certificate that
// names no tenant and whose key is not (or no longer) trusted is refused
// everywhere instead of being mistaken for a legacy CA-issued certificate.
func SelfSigned(leaf *x509.Certificate) bool {
	return leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
}

// Fingerprint returns "sha256:<hex>" of a SubjectPublicKeyInfo DER.
func Fingerprint(spki []byte) string {
	sum := sha256.Sum256(spki)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParsePublicKey accepts a SubjectPublicKeyInfo as PEM ("PUBLIC KEY") or DER
// and returns the DER after checking it parses (ECDSA, Ed25519, RSA, ML-DSA).
func ParsePublicKey(b []byte) ([]byte, error) {
	der := b
	if block, _ := pem.Decode(b); block != nil {
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("PEM block is %q, want PUBLIC KEY", block.Type)
		}
		der = block.Bytes
	}
	if _, err := x509.ParsePKIXPublicKey(der); err != nil {
		return nil, fmt.Errorf("parsing public key: %w", err)
	}
	return der, nil
}

// Open loads path. A missing file yields an empty store with Present() false;
// a file that exists but cannot be parsed or validated is an error (the
// caller must fail closed, not fall back to an open device).
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) reload() error {
	fi, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.present, s.entries = false, nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.path, err)
	}
	// Whoever can write this file can grant themselves owner: refuse a file
	// other users could have edited.
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (mode %#o); refusing to trust it", s.path, fi.Mode().Perm())
	}
	if err := checkOwner(fi); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.path, err)
	}
	entries, err := parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	s.present, s.entries = true, entries
	return nil
}

func parse(data []byte) ([]Entry, error) {
	var f fileFormat
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parsing trusted keys: %w", err)
	}
	if dec.More() {
		return nil, errors.New("parsing trusted keys: trailing data after the JSON object")
	}
	seen := make(map[string]bool, len(f.Keys))
	for i := range f.Keys {
		e := &f.Keys[i]
		spki, err := ParsePublicKey([]byte(e.PublicKey))
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		role, err := ParseRole(e.Role)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		fp := Fingerprint(spki)
		if seen[fp] {
			return nil, fmt.Errorf("key %d: duplicate key %s", i, fp)
		}
		seen[fp] = true
		e.spki, e.role = spki, role
	}
	return f.Keys, nil
}

// Present reports whether the trusted-keys file exists.
func (s *Store) Present() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.present
}

// Len is the number of trusted keys.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Lookup returns the role of the key with this SPKI DER (RoleNone if it is not
// trusted) and its fingerprint.
func (s *Store) Lookup(spki []byte) (Role, string) {
	fp := Fingerprint(spki)
	if s == nil {
		return RoleNone, fp
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.Fingerprint() == fp {
			return e.role, fp
		}
	}
	return RoleNone, fp
}

// List returns a copy of the entries.
func (s *Store) List() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Entry(nil), s.entries...)
}

// EntryRole is the parsed role of e.
func (e Entry) EntryRole() Role { return e.role }

// actorIsOwner is checked inside mutate's lock: the Authorizer's owner check
// runs before it, so an owner removed in between must not still write.
func actorIsOwner(entries []Entry, actor string) bool {
	for _, e := range entries {
		if e.Fingerprint() == actor {
			return e.role == RoleOwner
		}
	}
	return false
}

// Add trusts a new key. addedBy is the acting key's fingerprint, which must
// still be an owner when the write happens (ErrActorNotOwner otherwise), or
// AddedByInstall for the key seeded at install time.
func (s *Store) Add(spki []byte, role Role, label, addedBy string, now time.Time) (Entry, error) {
	if role == RoleNone {
		return Entry{}, errors.New("role is required")
	}
	if _, err := x509.ParsePKIXPublicKey(spki); err != nil {
		return Entry{}, fmt.Errorf("parsing public key: %w", err)
	}
	var added Entry
	err := s.mutate(func(entries []Entry) ([]Entry, error) {
		if addedBy != AddedByInstall && !actorIsOwner(entries, addedBy) {
			return nil, ErrActorNotOwner
		}
		fp := Fingerprint(spki)
		for _, e := range entries {
			if e.Fingerprint() == fp {
				return nil, ErrAlreadyExists
			}
		}
		added = Entry{
			PublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki})),
			Role:      role.String(),
			Label:     label,
			AddedAt:   now.UTC().Truncate(time.Second),
			AddedBy:   addedBy,
			spki:      spki,
			role:      role,
		}
		return append(entries, added), nil
	})
	return added, err
}

// Remove drops the key with this fingerprint. actor is the acting key's
// fingerprint and must still be an owner when the write happens
// (ErrActorNotOwner otherwise). The last owner key cannot be removed:
// recovery from a lost owner is a reinstall, so the device must never be
// left without one.
func (s *Store) Remove(fingerprint, actor string) (Entry, error) {
	var removed Entry
	err := s.mutate(func(entries []Entry) ([]Entry, error) {
		if !actorIsOwner(entries, actor) {
			return nil, ErrActorNotOwner
		}
		idx, owners := -1, 0
		for i, e := range entries {
			if e.role == RoleOwner {
				owners++
			}
			if e.Fingerprint() == fingerprint {
				idx = i
			}
		}
		if idx < 0 {
			return nil, ErrNotFound
		}
		if entries[idx].role == RoleOwner && owners == 1 {
			return nil, ErrLastOwner
		}
		removed = entries[idx]
		return append(entries[:idx:idx], entries[idx+1:]...), nil
	})
	return removed, err
}

// mutate re-reads the file under an exclusive lock, applies fn and writes the
// result atomically (0600), so concurrent writers (agent, installer tooling)
// never lose an update.
func (s *Store) mutate(fn func([]Entry) ([]Entry, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := flock.Acquire(s.path+".lock", 5*time.Second)
	if err != nil {
		return fmt.Errorf("locking trusted keys: %w", err)
	}
	defer release()
	if err := s.reload(); err != nil {
		return err
	}
	next, err := fn(append([]Entry(nil), s.entries...))
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(fileFormat{Keys: next}, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing trusted keys: %w", err)
	}
	s.present, s.entries = true, next
	return nil
}

// Floor is the latest added_at: a lower bound for the device clock when
// checking a local operator certificate's NotBefore on a device that has no
// enrollment certificate to take a floor from (no RTC, offline).
func (s *Store) Floor() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var floor time.Time
	for _, e := range s.entries {
		if e.AddedAt.After(floor) {
			floor = e.AddedAt
		}
	}
	return floor
}
