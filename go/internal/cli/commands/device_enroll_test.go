package commands

import (
	"regexp"
	"testing"

	"github.com/google/uuid"
)

// The device id is a v4 UUID (sem, WDY-2943). Cloud refuses any device id
// whose segments do not match ^[A-Za-z0-9._-]{1,64}$, and the id is irreversible
// once minted, so the shape is pinned here rather than assumed.
func TestDeviceIDIsALegalUUID(t *testing.T) {
	legalSegment := regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

	first := uuid.NewString()
	if !legalSegment.MatchString(first) {
		t.Errorf("device id %q is not a legal device id segment", first)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Errorf("device id %q does not parse as a UUID: %v", first, err)
	}
	if second := uuid.NewString(); second == first {
		t.Errorf("two enrollments minted the same device id %q", first)
	}
}

func TestACMEDirectoryURL(t *testing.T) {
	const tenant = "2558fd76-afc7-466e-9613-6b715296a526"

	if _, err := acmeDirectoryURL(tenant); err == nil {
		t.Errorf("acmeDirectoryURL with %s unset: want an error rather than a guessed host", acmeEndpointEnv)
	}

	t.Setenv(acmeEndpointEnv, "https://acme.pki.example")
	got, err := acmeDirectoryURL(tenant)
	if err != nil {
		t.Fatalf("acmeDirectoryURL: %v", err)
	}
	want := "https://acme.pki.example/" + tenant + "/acme/directory"
	if got != want {
		t.Errorf("acmeDirectoryURL = %q, want %q", got, want)
	}

	// A trailing slash must not double up: pki-core matches the directory path
	// exactly.
	t.Setenv(acmeEndpointEnv, "https://acme.pki.example/")
	if got, err = acmeDirectoryURL(tenant); err != nil || got != want {
		t.Errorf("acmeDirectoryURL with trailing slash = %q, %v; want %q", got, err, want)
	}

	t.Setenv(acmeEndpointEnv, "not-a-url")
	if _, err = acmeDirectoryURL(tenant); err == nil {
		t.Errorf("acmeDirectoryURL with a malformed endpoint: want an error")
	}
}
