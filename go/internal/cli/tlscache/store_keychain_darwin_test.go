package tlscache

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// fakeSecurity records invocations of the security CLI and returns canned output.
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

func TestKeychainGetDecodesBase64(t *testing.T) {
	blob := []byte("session-blob")
	fake := &fakeSecurity{out: []byte(base64.StdEncoding.EncodeToString(blob) + "\n")}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()

	got := newKeychainStore().get("abc123")
	if string(got) != "session-blob" {
		t.Fatalf("get = %q, want session-blob", got)
	}
	args := fake.calls[0].args
	want := []string{"find-generic-password", "-s", "wendy-tls-session", "-a", "abc123", "-w"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestKeychainGetMissOrDenied(t *testing.T) {
	fake := &fakeSecurity{err: errors.New("exit status 44")}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()
	if got := newKeychainStore().get("abc123"); got != nil {
		t.Errorf("get on security error = %q, want nil", got)
	}
}

func TestKeychainGetBadBase64(t *testing.T) {
	fake := &fakeSecurity{out: []byte("!!! not base64 !!!")}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()
	if got := newKeychainStore().get("abc123"); got != nil {
		t.Errorf("get on undecodable payload = %q, want nil", got)
	}
}

func TestKeychainPutKeepsSecretOffArgv(t *testing.T) {
	fake := &fakeSecurity{}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()

	blob := []byte("ticket-secret")
	newKeychainStore().put("abc123", blob)

	call := fake.calls[0]
	// The whole add command rides stdin via `security -i`; argv holds only the flag.
	if strings.Join(call.args, " ") != "-i" {
		t.Fatalf("argv = %v, want [-i]", call.args)
	}
	b64 := base64.StdEncoding.EncodeToString(blob)
	for _, frag := range []string{"add-generic-password", "-U", "-s wendy-tls-session", "-a abc123", "-w " + b64} {
		if !strings.Contains(call.stdin, frag) {
			t.Errorf("stdin %q missing %q", call.stdin, frag)
		}
	}
}

func TestKeychainDelete(t *testing.T) {
	fake := &fakeSecurity{}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()
	newKeychainStore().delete("abc123")
	want := []string{"delete-generic-password", "-s", "wendy-tls-session", "-a", "abc123"}
	if strings.Join(fake.calls[0].args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", fake.calls[0].args, want)
	}
}

func TestDarwinDefaultIsKeychain(t *testing.T) {
	t.Setenv("WENDY_TLS_SESSION_STORE", "")
	if _, ok := newDefaultStore().(keychainStore); !ok {
		t.Errorf("darwin default = %T, want keychainStore", newDefaultStore())
	}
}
