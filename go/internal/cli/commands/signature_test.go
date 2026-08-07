package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// readOptionalSignature is the shared seam both artifact-signature call
// sites (the UpdateAgent binary upload and the RunContainer image push) use
// to pick up an optional detached signature file. No signer exists yet, so
// the common case is an empty path — this must stay silent (nil, nil), not
// an error, for every command that never sets it.
func TestReadOptionalSignatureEmptyPathReturnsNil(t *testing.T) {
	data, err := readOptionalSignature("")
	if err != nil {
		t.Fatalf("readOptionalSignature(\"\") error = %v, want nil", err)
	}
	if data != nil {
		t.Fatalf("readOptionalSignature(\"\") = %v, want nil", data)
	}
}

func TestReadOptionalSignatureMissingFileReturnsNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.sig")
	data, err := readOptionalSignature(path)
	if err != nil {
		t.Fatalf("readOptionalSignature(missing) error = %v, want nil", err)
	}
	if data != nil {
		t.Fatalf("readOptionalSignature(missing) = %v, want nil", data)
	}
}

func TestReadOptionalSignaturePresentFileReturnsBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.sig")
	want := []byte("fake-ml-dsa65-signature-bytes")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := readOptionalSignature(path)
	if err != nil {
		t.Fatalf("readOptionalSignature(present) error = %v, want nil", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("readOptionalSignature(present) = %q, want %q", got, want)
	}
}

func TestReadOptionalSignatureDirectoryPathReturnsError(t *testing.T) {
	// A directory is a real misconfiguration (not merely "no signature
	// produced yet") and must be surfaced, not silently swallowed.
	dir := t.TempDir()
	if _, err := readOptionalSignature(dir); err == nil {
		t.Fatal("readOptionalSignature(dir) error = nil, want an error")
	}
}
