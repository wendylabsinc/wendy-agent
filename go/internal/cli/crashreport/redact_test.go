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
