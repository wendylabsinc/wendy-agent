package ipcam

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(filepath.Join(t.TempDir(), "cameras.json"))
	if err := r.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	return r
}

// A camera is identified by MAC, so re-registering the same camera at a new
// address must reuse its ID rather than allocate a second entry.
func TestUpsertIsKeyedByMAC(t *testing.T) {
	r := newTestRegistry(t)
	first, err := r.Upsert(Camera{MAC: "ec:71:db:2a:ae:7e", Address: "10.98.0.50", Model: "RLC-520A"})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second, err := r.Upsert(Camera{MAC: "ec:71:db:2a:ae:7e", Address: "10.98.0.77"})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("ID changed across upsert: %d then %d", first.ID, second.ID)
	}
	if second.Address != "10.98.0.77" {
		t.Fatalf("address not updated: %q", second.Address)
	}
	// A later probe returning less detail must not blank what an earlier one found.
	if second.Model != "RLC-520A" {
		t.Fatalf("model lost on upsert without model: %q", second.Model)
	}
	if got := len(r.List()); got != 1 {
		t.Fatalf("List() has %d cameras, want 1", got)
	}
}

func TestUpsertAllocatesFromBand(t *testing.T) {
	r := newTestRegistry(t)
	a, err := r.Upsert(Camera{MAC: "aa:aa:aa:aa:aa:aa"})
	if err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	b, err := r.Upsert(Camera{MAC: "bb:bb:bb:bb:bb:bb"})
	if err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	if a.ID != IDBandStart {
		t.Fatalf("first ID = %d, want %d", a.ID, IDBandStart)
	}
	if b.ID != IDBandStart+1 {
		t.Fatalf("second ID = %d, want %d", b.ID, IDBandStart+1)
	}
}

func TestUpsertReusesForgottenID(t *testing.T) {
	r := newTestRegistry(t)
	a, err := r.Upsert(Camera{MAC: "aa:aa:aa:aa:aa:aa"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !r.Forget(a.ID) {
		t.Fatal("Forget returned false for a known ID")
	}
	if _, ok := r.Get(a.ID); ok {
		t.Fatal("Get succeeded after Forget")
	}
	b, err := r.Upsert(Camera{MAC: "bb:bb:bb:bb:bb:bb"})
	if err != nil {
		t.Fatalf("upsert after forget: %v", err)
	}
	if b.ID != IDBandStart {
		t.Fatalf("freed ID not reused: got %d, want %d", b.ID, IDBandStart)
	}
}

func TestUpsertBandExhaustion(t *testing.T) {
	r := newTestRegistry(t)
	total := IDBandEnd - IDBandStart + 1
	for i := 0; i < total; i++ {
		if _, err := r.Upsert(Camera{MAC: fmt.Sprintf("aa:aa:aa:aa:%02x:%02x", i/256, i%256)}); err != nil {
			t.Fatalf("upsert %d failed early: %v", i, err)
		}
	}
	if _, err := r.Upsert(Camera{MAC: "ff:ff:ff:ff:ff:ff"}); !errors.Is(err, ErrBandExhausted) {
		t.Fatalf("error = %v, want ErrBandExhausted", err)
	}
}

// IDs must survive an agent restart, since users learn them from camera list.
func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cameras.json")
	first := NewRegistry(path)
	if err := first.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	saved, err := first.Upsert(Camera{MAC: "ec:71:db:2a:ae:7e", Address: "10.98.0.50", Model: "RLC-520A"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	second := NewRegistry(path)
	if err := second.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := second.Get(saved.ID)
	if !ok {
		t.Fatalf("camera %d missing after reload", saved.ID)
	}
	if got.MAC != saved.MAC || got.Model != "RLC-520A" {
		t.Fatalf("reloaded camera = %+v, want MAC and model preserved", got)
	}
	// Online is live state, so a camera loaded from disk starts offline.
	if got.Online {
		t.Fatal("camera loaded from disk reported online")
	}
}

func TestMarkSeenSetsOnline(t *testing.T) {
	r := newTestRegistry(t)
	c, err := r.Upsert(Camera{MAC: "ec:71:db:2a:ae:7e", Address: "10.98.0.50"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	r.MarkSeen(c.MAC, "10.98.0.51", true)
	got, _ := r.Get(c.ID)
	if !got.Online {
		t.Fatal("camera not marked online")
	}
	if got.Address != "10.98.0.51" {
		t.Fatalf("address = %q, want updated", got.Address)
	}
	r.MarkSeen(c.MAC, "", false)
	got, _ = r.Get(c.ID)
	if got.Online {
		t.Fatal("camera still online after being marked offline")
	}
	if got.Address != "10.98.0.51" {
		t.Fatalf("address = %q, an empty address must not erase it", got.Address)
	}
}

// Registration goes through Upsert, which allocates; MarkSeen must not create
// an entry with a zero ID for a MAC it has never seen.
func TestMarkSeenIgnoresUnknownMAC(t *testing.T) {
	r := newTestRegistry(t)
	r.MarkSeen("00:11:22:33:44:55", "10.98.0.9", true)
	if got := len(r.List()); got != 0 {
		t.Fatalf("registry has %d cameras, want 0", got)
	}
}

func TestListIsOrderedByID(t *testing.T) {
	r := newTestRegistry(t)
	for _, mac := range []string{"cc:cc:cc:cc:cc:cc", "aa:aa:aa:aa:aa:aa", "bb:bb:bb:bb:bb:bb"} {
		if _, err := r.Upsert(Camera{MAC: mac}); err != nil {
			t.Fatalf("upsert %s: %v", mac, err)
		}
	}
	list := r.List()
	for i := 1; i < len(list); i++ {
		if list[i-1].ID >= list[i].ID {
			t.Fatalf("List() not ordered by ID: %d then %d", list[i-1].ID, list[i].ID)
		}
	}
}

func TestForgetUnknownID(t *testing.T) {
	r := newTestRegistry(t)
	if r.Forget(IDBandStart) {
		t.Fatal("Forget returned true for an unknown ID")
	}
}
