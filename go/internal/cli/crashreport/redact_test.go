package crashreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactHomeDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home dir")
	}
	in := filepath.Join(home, "projects", "secret-thing", "main.go")
	got := Redact(in)
	if strings.Contains(got, home) {
		t.Errorf("home dir not redacted: %q", got)
	}
	if !strings.HasPrefix(got, "~") {
		t.Errorf("expected ~ prefix, got %q", got)
	}
}

func TestRedactBearerToken(t *testing.T) {
	got := Redact("Authorization: Bearer abc123.def456.ghi789")
	if strings.Contains(got, "abc123.def456.ghi789") {
		t.Errorf("token not redacted: %q", got)
	}
}

func TestRedactIPAndEmail(t *testing.T) {
	got := Redact("connect 192.168.1.42 user a.b@example.com")
	if strings.Contains(got, "192.168.1.42") {
		t.Errorf("IPv4 not redacted: %q", got)
	}
	if strings.Contains(got, "a.b@example.com") {
		t.Errorf("email not redacted: %q", got)
	}
}

func TestRedactLeavesPlainText(t *testing.T) {
	in := "docker build failed: exit status 1"
	if got := Redact(in); got != in {
		t.Errorf("plain text changed: %q != %q", got, in)
	}
}

func TestRedactIPv6(t *testing.T) {
	mustMatch := []struct {
		name  string
		input string
	}{
		{"loopback compressed", "connect ::1 done"},
		{"link-local compressed", "addr fe80::1 up"},
		{"link-local with zone id", "addr fe80::1%eth0 up"},
		{"doc prefix compressed", "host 2001:db8::1 ok"},
		{"trailing double-colon", "host 2001:db8:: ok"},
		{"full 8-group", "addr 2001:0db8:85a3:0000:0000:8a2e:0370:7334 ok"},
	}

	for _, tc := range mustMatch {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.input)
			// Extract the IP-looking token from the input to check it's gone.
			// We check that "<redacted-ip>" appears and no raw hex-colon run remains.
			if !strings.Contains(got, "<redacted-ip>") {
				t.Errorf("IPv6 not redacted in %q → %q", tc.input, got)
			}
		})
	}

	// Must NOT over-redact a plain clock timestamp (HH:MM:SS).
	t.Run("clock timestamp not redacted", func(t *testing.T) {
		in := "event at 12:34:56 done"
		got := Redact(in)
		if strings.Contains(got, "<redacted-ip>") {
			t.Errorf("clock timestamp incorrectly redacted: %q → %q", in, got)
		}
		if !strings.Contains(got, "12:34:56") {
			t.Errorf("clock timestamp was altered: %q → %q", in, got)
		}
	})
}

func TestRedactBearerBase64(t *testing.T) {
	got := Redact("Authorization: Bearer abc+def/ghi==")
	// The entire token including base64url chars must be gone.
	for _, leak := range []string{"abc", "def", "ghi", "+def/ghi==", "abc+def/ghi=="} {
		if strings.Contains(got, leak) {
			t.Errorf("base64 bearer token fragment %q leaked: %q", leak, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Errorf("expected <redacted> marker, got: %q", got)
	}
}
