package commands

import (
	"strings"
	"testing"
)

func TestDeviceIDFromName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"playful-reed", "playful-reed"},
		{"Lab Pi 01", "lab-pi-01"},
		{"fleet-a/box-01", "fleet-a/box-01"},
		// Runs collapse and edges trim, so a name does not turn into a
		// segment pki-core would refuse.
		{"Lab  Pi", "lab-pi"},
		{" -edge- ", "edge"},
		{"a//b", "a/b"},
		{"box.01_v2", "box.01_v2"},
		// "." and ".." are the only dot segments the contract bans; "..." is
		// legal and is left alone rather than invented away.
		{"...", "..."},
		// Segments longer than 64 characters are truncated, not rejected.
		{strings.Repeat("x", 80), strings.Repeat("x", 64)},
	}
	for _, tt := range tests {
		got, err := deviceIDFromName(tt.name)
		if err != nil {
			t.Errorf("deviceIDFromName(%q): %v", tt.name, err)
			continue
		}
		if got != tt.want {
			t.Errorf("deviceIDFromName(%q) = %q, want %q", tt.name, got, tt.want)
		}
		for _, segment := range strings.Split(got, "/") {
			if !deviceIDSegment.MatchString(segment) {
				t.Errorf("deviceIDFromName(%q) = %q: segment %q is not a legal device id segment", tt.name, got, segment)
			}
		}
	}
}

func TestDeviceIDFromNameRefusesNamesWithNothingToKeep(t *testing.T) {
	// The device id is irreversible, so a name that carries no usable
	// character is refused rather than turned into something invented.
	for _, name := range []string{"", "   ", "///", "-", "/./", "/../"} {
		if got, err := deviceIDFromName(name); err == nil {
			t.Errorf("deviceIDFromName(%q) = %q, want an error", name, got)
		}
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
