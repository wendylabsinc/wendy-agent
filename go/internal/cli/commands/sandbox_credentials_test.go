package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOrGenerateSandboxCredentialsAt_GeneratesWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-credentials.json")

	creds, err := readOrGenerateSandboxCredentialsAt(path)
	if err != nil {
		t.Fatalf("readOrGenerateSandboxCredentialsAt: %v", err)
	}
	if creds.User != "admin" {
		t.Errorf("User = %q, want admin", creds.User)
	}
	if len(creds.Password) == 0 {
		t.Error("Password is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading persisted file: %v", err)
	}
	var onDisk sandboxAdminCredentials
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshalling persisted file: %v", err)
	}
	if onDisk != creds {
		t.Errorf("persisted %+v != returned %+v", onDisk, creds)
	}
}

func TestReadOrGenerateSandboxCredentialsAt_ReadsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-credentials.json")
	want := sandboxAdminCredentials{User: "someone", Password: "existing-secret"}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readOrGenerateSandboxCredentialsAt(path)
	if err != nil {
		t.Fatalf("readOrGenerateSandboxCredentialsAt: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadOrGenerateSandboxCredentialsAt_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-credentials.json")

	first, err := readOrGenerateSandboxCredentialsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readOrGenerateSandboxCredentialsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("second call generated a different password: %+v != %+v", first, second)
	}
}
