package secretstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

type fakeSecurity struct {
	calls []struct {
		stdin string
		args  []string
	}
	out []byte
	err error
}

func (f *fakeSecurity) run(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, struct {
		stdin string
		args  []string
	}{stdin, args})
	return f.out, f.err
}

func withFake(t *testing.T, f *fakeSecurity) {
	t.Helper()
	orig := RunSecurity
	RunSecurity = f.run
	t.Cleanup(func() { RunSecurity = orig })
}

func TestKeychainGetDecodesBase64(t *testing.T) {
	fake := &fakeSecurity{out: []byte(base64.StdEncoding.EncodeToString([]byte("blob")) + "\n")}
	withFake(t, fake)
	got := NewKeychain("svc-a").Get("acct1")
	if string(got) != "blob" {
		t.Fatalf("Get = %q, want blob", got)
	}
	want := "find-generic-password -s svc-a -a acct1 -w"
	if strings.Join(fake.calls[0].args, " ") != want {
		t.Errorf("args = %v, want %q", fake.calls[0].args, want)
	}
}

func TestKeychainGetMissOrDenied(t *testing.T) {
	fake := &fakeSecurity{err: errors.New("exit status 44")}
	withFake(t, fake)
	if got := NewKeychain("svc-a").Get("acct1"); got != nil {
		t.Errorf("Get on error = %q, want nil", got)
	}
}

func TestKeychainGetBadBase64(t *testing.T) {
	fake := &fakeSecurity{out: []byte("!!! not base64 !!!")}
	withFake(t, fake)
	if got := NewKeychain("svc-a").Get("acct1"); got != nil {
		t.Errorf("Get on undecodable payload = %q, want nil", got)
	}
}

func TestKeychainPutKeepsSecretOffArgvAndReportsError(t *testing.T) {
	fake := &fakeSecurity{}
	withFake(t, fake)
	if err := NewKeychain("svc-a").Put("acct1", []byte("secret")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	call := fake.calls[0]
	if strings.Join(call.args, " ") != "-i" {
		t.Fatalf("argv = %v, want [-i]", call.args)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("secret"))
	for _, frag := range []string{"add-generic-password", "-U", "-s svc-a", "-a acct1", "-w " + b64} {
		if !strings.Contains(call.stdin, frag) {
			t.Errorf("stdin %q missing %q", call.stdin, frag)
		}
	}

	failing := &fakeSecurity{err: errors.New("keychain locked")}
	withFake(t, failing)
	if err := NewKeychain("svc-a").Put("acct1", []byte("secret")); err == nil {
		t.Error("Put with failing security = nil error, want non-nil")
	}
}

// TestKeychainPutRefusesOversizedCommandLine is the regression test for the
// security(1) 4096-byte stdin-line truncation guard: an oversized secret must
// be refused before RunSecurity is ever invoked, never handed to it for a
// truncated (possibly silently "successful") write.
func TestKeychainPutRefusesOversizedCommandLine(t *testing.T) {
	fake := &fakeSecurity{}
	withFake(t, fake)
	// base64 inflates by ~4/3, so 4000 raw bytes comfortably pushes the full
	// "add-generic-password ..." command line past the 4000-byte guard.
	oversized := bytes.Repeat([]byte{'x'}, 4000)
	err := NewKeychain("svc-a").Put("acct1", oversized)
	if err == nil {
		t.Fatal("Put with oversized secret = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error %q missing size-guard wording", err.Error())
	}
	if len(fake.calls) != 0 {
		t.Errorf("fake runner was called %d times, want 0 — guard must short-circuit before invoking security", len(fake.calls))
	}
}

func TestKeychainDelete(t *testing.T) {
	fake := &fakeSecurity{}
	withFake(t, fake)
	NewKeychain("svc-a").Delete("acct1")
	want := "delete-generic-password -s svc-a -a acct1"
	if strings.Join(fake.calls[0].args, " ") != want {
		t.Errorf("args = %v, want %q", fake.calls[0].args, want)
	}
}
