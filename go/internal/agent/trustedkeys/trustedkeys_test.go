package trustedkeys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newSPKI(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestAbsentFileMeansNoLocalOperators(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Present() || s.Len() != 0 {
		t.Fatalf("absent file: Present=%v Len=%d, want false 0", s.Present(), s.Len())
	}
	if role, _ := s.Lookup(newSPKI(t)); role != RoleNone {
		t.Fatalf("absent file trusts a key: role %s", role)
	}
}

func TestAddLookupRemoveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted-keys.json")
	s, _ := Open(path)
	ownerKey, viewerKey := newSPKI(t), newSPKI(t)
	now := time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)
	if _, err := s.Add(ownerKey, RoleOwner, "laptop", AddedByInstall, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(viewerKey, RoleViewer, "ci", Fingerprint(ownerKey), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(viewerKey, RoleOperator, "again", Fingerprint(ownerKey), now); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Add = %v, want ErrAlreadyExists", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %#o, want 0600", fi.Mode().Perm())
	}

	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := reloaded.Lookup(ownerKey); role != RoleOwner {
		t.Fatalf("owner role after reload = %s", role)
	}
	if role, _ := reloaded.Lookup(viewerKey); role != RoleViewer {
		t.Fatalf("viewer role after reload = %s", role)
	}
	if got, want := reloaded.Floor(), now.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("Floor = %v, want %v", got, want)
	}
	if e := reloaded.List()[1]; e.AddedBy != Fingerprint(ownerKey) || e.Label != "ci" {
		t.Fatalf("entry = %+v", e)
	}

	if _, err := reloaded.Remove(Fingerprint(viewerKey), Fingerprint(ownerKey)); err != nil {
		t.Fatal(err)
	}
	if role, _ := reloaded.Lookup(viewerKey); role != RoleNone {
		t.Fatalf("removed key still trusted as %s", role)
	}
	if _, err := reloaded.Remove(Fingerprint(viewerKey), Fingerprint(ownerKey)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Remove = %v, want ErrNotFound", err)
	}
}

func TestLastOwnerCannotBeRemoved(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	a, b, op := newSPKI(t), newSPKI(t), newSPKI(t)
	now := time.Now()
	for _, k := range []struct {
		spki []byte
		role Role
	}{{a, RoleOwner}, {b, RoleOwner}, {op, RoleOperator}} {
		if _, err := s.Add(k.spki, k.role, "", AddedByInstall, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Remove(Fingerprint(a), Fingerprint(b)); err != nil {
		t.Fatalf("removing one of two owners: %v", err)
	}
	if _, err := s.Remove(Fingerprint(b), Fingerprint(b)); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing the last owner = %v, want ErrLastOwner", err)
	}
	if role, _ := s.Lookup(b); role != RoleOwner {
		t.Fatalf("last owner lost: role %s", role)
	}
}

func TestUnusableFileIsAnError(t *testing.T) {
	cases := map[string]struct {
		body string
		mode os.FileMode
	}{
		"not json":        {"{", 0o600},
		"trailing data":   {`{"keys":[]} {"keys":[]}`, 0o600},
		"unknown field":   {`{"keys":[],"allow_all":true}`, 0o600},
		"bad role":        {`{"keys":[{"public_key":"` + pemOf(t) + `","role":"admin"}]}`, 0o600},
		"bad key":         {`{"keys":[{"public_key":"nope","role":"owner"}]}`, 0o600},
		"group writable":  {`{"keys":[]}`, 0o620},
		"others writable": {`{"keys":[]}`, 0o602},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trusted-keys.json")
			if err := os.WriteFile(path, []byte(c.body), c.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, c.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("Open accepted an unusable trusted keys file")
			}
		})
	}
}

func pemOf(t *testing.T) string {
	s, _ := Open(filepath.Join(t.TempDir(), "k.json"))
	e, err := s.Add(newSPKI(t), RoleOwner, "", AddedByInstall, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(e.PublicKey)
	return string(quoted[1 : len(quoted)-1])
}

// Cloud-enrolled: the saved keys stay on disk but trust nothing; unenrolling
// brings them back unchanged.
func TestDormantStoreTrustsNothingAndKeepsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted-keys.json")
	s, _ := Open(path)
	owner := newSPKI(t)
	if _, err := s.Add(owner, RoleOwner, "", AddedByInstall, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if !s.SetDormant(true) || s.SetDormant(true) {
		t.Fatal("SetDormant must report only the transition")
	}
	if role, _ := s.Lookup(owner); role != RoleNone {
		t.Fatalf("dormant store trusts the owner: role %s", role)
	}
	if !s.Floor().IsZero() {
		t.Fatal("dormant store still floors the clock")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) || !s.Present() {
		t.Fatal("dormancy touched the keys file")
	}
	if !s.SetDormant(false) {
		t.Fatal("unenroll transition not reported")
	}
	if role, _ := s.Lookup(owner); role != RoleOwner {
		t.Fatalf("after unenroll role = %s, want owner", role)
	}
}

// SetDormant serializes with writes: it waits for a write holding the store
// lock, so a change in flight lands before the enrollment flip, and every
// later write sees the store dormant.
func TestSetDormantWaitsForWritesInFlight(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	s.mu.Lock() // a mutate past its dormant re-check
	flipped := make(chan struct{})
	go func() {
		s.SetDormant(true)
		close(flipped)
	}()
	select {
	case <-flipped:
		t.Fatal("SetDormant did not wait for the write holding the lock")
	case <-time.After(50 * time.Millisecond):
	}
	s.mu.Unlock()
	<-flipped
	if _, err := s.Add(newSPKI(t), RoleOwner, "", AddedByInstall, time.Now()); !errors.Is(err, ErrDormant) {
		t.Fatalf("Add after the flip = %v, want ErrDormant", err)
	}
}

// The fingerprint is computed once, on load and on Add, and equals the SPKI hash.
func TestEntryFingerprintIsCached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted-keys.json")
	s, _ := Open(path)
	spki := newSPKI(t)
	added, err := s.Add(spki, RoleOwner, "", AddedByInstall, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Fingerprint(spki)
	if added.Fingerprint() != want || reopened.List()[0].Fingerprint() != want {
		t.Fatalf("fingerprints %q / %q, want %q", added.Fingerprint(), reopened.List()[0].Fingerprint(), want)
	}
}
