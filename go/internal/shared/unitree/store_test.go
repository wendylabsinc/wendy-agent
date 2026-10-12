package unitree

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPairing() Pairing {
	return Pairing{
		Device:   Device{Serial: "B42TEST", Name: "Rover", Address: "192.168.1.5", EncryptionKey: "0123456789abcdef0123456789abcdef"},
		PairedAt: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC),
	}
}

func TestPairingStoreSaveReconnectAndForget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unitree", "pairings.json")
	store := NewStore(path, "tenant-a")
	list, err := store.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("new store not empty: %v", err)
	}
	pairing := testPairing()
	if err := store.Save(pairing); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("pairing key file permissions %o, want 600", info.Mode().Perm())
	}
	list, err = NewStore(path, "tenant-a").List()
	if err != nil || len(list) != 1 {
		t.Fatalf("cannot reload pairing: %v", err)
	}
	if list[0] != pairing {
		t.Error("saved robot cannot reconnect with the same identity and key")
	}
	public, err := json.Marshal(list)
	if err != nil || strings.Contains(string(public), pairing.Device.EncryptionKey) {
		t.Fatal("pairing JSON exposes the encryption key")
	}
	pairing.Device.Address = "192.168.1.8"
	if err := store.Save(pairing); err != nil {
		t.Fatal(err)
	}
	list, err = store.List()
	if err != nil || len(list) != 1 || list[0].Device.Address != pairing.Device.Address {
		t.Fatal("saving an existing serial did not update its address")
	}
	if err := store.Remove(pairing.Device.Serial); err != nil {
		t.Fatal(err)
	}
	list, err = store.List()
	if err != nil || len(list) != 0 {
		t.Fatal("forgotten pairing remains in the store")
	}
	if err := store.Remove(pairing.Device.Serial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing pairing error: %v", err)
	}
	private, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(private), pairing.Device.EncryptionKey) {
		t.Fatal("forgotten key remains on disk")
	}
}

func TestPairingStoreEnrollmentOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairings.json")
	original := NewStore(path, "tenant-a")
	if err := original.Save(testPairing()); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"tenant-b", "local", ""} {
		foreign := NewStore(path, owner)
		if list, err := foreign.List(); err == nil || len(list) != 0 {
			t.Fatalf("owner %q could load old credentials: %v", owner, err)
		}
		if err := foreign.Save(testPairing()); err == nil {
			t.Fatalf("owner %q could overwrite another enrollment", owner)
		}
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"devices":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := original.List(); err == nil {
		t.Fatal("automatically adopted an unscoped pre-enrollment store")
	}
	if err := original.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cleared credential file remains on disk")
	}
	if err := original.Clear(); err != nil {
		t.Fatal("clearing missing store is not idempotent", err)
	}
}

func TestPairingStoreRejectsUnsafeFiles(t *testing.T) {
	for _, name := range []string{"public", "symlink", "directory", "corrupt", "version"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pairings.json")
			switch name {
			case "public":
				if err := os.WriteFile(path, []byte(`{"version":1}`), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte(`{"version":1}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(path, []byte(`{"encryption_key":"SECRET"`), 0600); err != nil {
					t.Fatal(err)
				}
			case "version":
				if err := os.WriteFile(path, []byte(`{"version":999}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			store := NewStore(path, "tenant-a")
			if _, err := store.List(); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("unsafe file accepted or content leaked: %v", err)
			}
			if err := store.Save(testPairing()); err == nil {
				t.Fatal("overwrote an unsafe store")
			}
		})
	}
}

func TestPairingStoreRequiresVerifiedIdentity(t *testing.T) {
	for _, name := range []string{"serial", "address", "key", "verification"} {
		t.Run(name, func(t *testing.T) {
			pair := testPairing()
			switch name {
			case "serial":
				pair.Device.Serial = ""
			case "address":
				pair.Device.Address = "example.com"
			case "key":
				pair.Device.EncryptionKey = "invalid"
			case "verification":
				pair.PairedAt = time.Time{}
			}
			path := filepath.Join(t.TempDir(), "pairings.json")
			if err := NewStore(path, "tenant-a").Save(pair); err == nil {
				t.Fatal("accepted an incomplete pairing")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid pairing created a file")
			}
		})
	}
}
