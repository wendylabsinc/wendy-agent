package secretstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// testKeychainPath is the path the faked `security default-keychain` reports.
const testKeychainPath = "/Users/tester/Library/Keychains/login.keychain-db"

type fakeSecurity struct {
	calls []struct {
		stdin string
		args  []string
	}
	out []byte
	err error
	// respond, when set, answers each invocation by argv and takes precedence
	// over out/err — needed by tests that must distinguish the read-only
	// keychain probes from the write they gate.
	respond func(args []string) ([]byte, error)
}

func (f *fakeSecurity) run(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, struct {
		stdin string
		args  []string
	}{stdin, args})
	if f.respond != nil {
		return f.respond(args)
	}
	return f.out, f.err
}

// withProbesOK lets the two read-only keychain probes succeed and keeps
// out/err for everything else, so tests about Put/Delete themselves do not
// have to restate the probes.
func (f *fakeSecurity) withProbesOK() *fakeSecurity {
	f.respond = func(args []string) ([]byte, error) {
		switch args[0] {
		case "default-keychain":
			return []byte(`    "` + testKeychainPath + `"` + "\n"), nil
		case "show-keychain-info":
			return []byte("no-timeout\n"), nil
		}
		return f.out, f.err
	}
	return f
}

// writeCall returns the first mutating security(1) invocation recorded, so
// assertions skip over the probes that now precede every write.
func (f *fakeSecurity) writeCall(t *testing.T) (stdin string, args []string) {
	t.Helper()
	for _, c := range f.calls {
		if len(c.args) > 0 && (c.args[0] == "-i" || c.args[0] == "delete-generic-password") {
			return c.stdin, c.args
		}
	}
	t.Fatalf("no mutating security(1) call recorded, got %v", f.calls)
	return "", nil
}

func withFake(t *testing.T, f *fakeSecurity) {
	t.Helper()
	orig := RunSecurity
	RunSecurity = f.run
	resetKeychainProbeForTest()
	t.Cleanup(func() {
		RunSecurity = orig
		resetKeychainProbeForTest()
	})
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
	fake := (&fakeSecurity{}).withProbesOK()
	withFake(t, fake)
	if err := NewKeychain("svc-a").Put("acct1", []byte("secret")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stdin, args := fake.writeCall(t)
	if strings.Join(args, " ") != "-i" {
		t.Fatalf("argv = %v, want [-i]", args)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("secret"))
	for _, frag := range []string{"add-generic-password", "-U", "-s svc-a", "-a acct1", "-w " + b64} {
		if !strings.Contains(stdin, frag) {
			t.Errorf("stdin %q missing %q", stdin, frag)
		}
	}

	failing := (&fakeSecurity{err: errors.New("write refused")}).withProbesOK()
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
	fake := (&fakeSecurity{}).withProbesOK()
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
	fake := (&fakeSecurity{}).withProbesOK()
	withFake(t, fake)
	NewKeychain("svc-a").Delete("acct1")
	_, args := fake.writeCall(t)
	want := "delete-generic-password -s svc-a -a acct1"
	if strings.Join(args, " ") != want {
		t.Errorf("args = %v, want %q", args, want)
	}
}
