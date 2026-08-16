package secretstore

import (
	"errors"
	"strings"
	"testing"
)

// wroteAnything reports whether any recorded call was a mutating security(1)
// invocation — the `-i` stdin form used by Put, or delete-generic-password.
// Those are exactly the calls that can block on a macOS modal.
func (f *fakeSecurity) wroteAnything() bool {
	for _, c := range f.calls {
		if len(c.args) == 0 {
			continue
		}
		if c.args[0] == "-i" || c.args[0] == "delete-generic-password" {
			return true
		}
	}
	return false
}

// TestKeychainPutRefusesWithoutDefaultKeychain is the regression test for the
// reported bug: a `wendy` invocation whose HOME holds no user keychain (sudo,
// launchd, any non-login context) makes `SecKeychainCopyDefault` fail, and a
// write in that state pops the blocking "A keychain cannot be found to store
// key-<hex>" modal — whose "Reset To Defaults" button rewrites the user's
// keychain search list. Put must detect that with the read-only probe and
// never reach the write.
func TestKeychainPutRefusesWithoutDefaultKeychain(t *testing.T) {
	fake := &fakeSecurity{respond: func(args []string) ([]byte, error) {
		if args[0] == "default-keychain" {
			return nil, errors.New("exit status 1") // SecKeychainCopyDefault failed
		}
		return nil, nil
	}}
	withFake(t, fake)

	err := NewKeychain("svc-a").Put("key-5bccc0bca49d432c", []byte("secret"))
	if err == nil {
		t.Fatal("Put with no default keychain = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "default keychain") {
		t.Errorf("error %q does not name the unresolvable default keychain", err)
	}
	if fake.wroteAnything() {
		t.Errorf("Put reached a mutating security(1) call (%v) — the write must be short-circuited before macOS can raise a modal", fake.calls)
	}
}

// TestKeychainPutRefusesWhenKeychainLocked covers the other prompt the write
// path can raise: a locked keychain answers a write with the unlock dialog.
// `show-keychain-info` reports lock state without any UI, so Put can decline
// instead. Callers keep the secret wherever it already lives.
func TestKeychainPutRefusesWhenKeychainLocked(t *testing.T) {
	fake := &fakeSecurity{respond: func(args []string) ([]byte, error) {
		switch args[0] {
		case "default-keychain":
			return []byte(`    "` + testKeychainPath + `"` + "\n"), nil
		case "show-keychain-info":
			return nil, errors.New("SecKeychainCopySettings: User interaction is not allowed.")
		}
		return nil, nil
	}}
	withFake(t, fake)

	err := NewKeychain("svc-a").Put("acct1", []byte("secret"))
	if err == nil {
		t.Fatal("Put against a locked keychain = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error %q does not explain the lock", err)
	}
	if fake.wroteAnything() {
		t.Errorf("Put reached a mutating security(1) call (%v) despite the locked keychain", fake.calls)
	}
}

// TestKeychainDeleteSkipsUnwritableKeychain guards the quietest instance of
// the bug: tlscache evicts a broken session by calling Delete from a
// background goroutine, so an unlock prompt raised there would appear with no
// CLI context at all.
func TestKeychainDeleteSkipsUnwritableKeychain(t *testing.T) {
	fake := &fakeSecurity{respond: func(args []string) ([]byte, error) {
		if args[0] == "default-keychain" {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	}}
	withFake(t, fake)

	NewKeychain("svc-a").Delete("acct1")
	if fake.wroteAnything() {
		t.Errorf("Delete reached a mutating security(1) call (%v) with no writable keychain", fake.calls)
	}
}

// TestKeychainPutBlankDefaultKeychainIsRefused pins the parse: a successful
// exit with empty output must not be read as "some keychain resolved".
func TestKeychainPutBlankDefaultKeychainIsRefused(t *testing.T) {
	fake := &fakeSecurity{respond: func(args []string) ([]byte, error) {
		if args[0] == "default-keychain" {
			return []byte("   \n"), nil
		}
		return nil, nil
	}}
	withFake(t, fake)

	if err := NewKeychain("svc-a").Put("acct1", []byte("secret")); err == nil {
		t.Fatal("Put with a blank default-keychain answer = nil error, want a refusal")
	}
	if fake.wroteAnything() {
		t.Errorf("Put reached a mutating security(1) call (%v) on a blank keychain path", fake.calls)
	}
}

// TestDefaultKeychainResolvedOncePerProcess keeps the guard cheap: the
// resolution depends on HOME, which cannot change under a running CLI, so
// repeated writes must not each pay a subprocess for it.
func TestDefaultKeychainResolvedOncePerProcess(t *testing.T) {
	resolves := 0
	fake := &fakeSecurity{respond: func(args []string) ([]byte, error) {
		if args[0] == "default-keychain" {
			resolves++
			return []byte(`    "` + testKeychainPath + `"` + "\n"), nil
		}
		return nil, nil
	}}
	withFake(t, fake)

	store := NewKeychain("svc-a")
	for range 3 {
		if err := store.Put("acct1", []byte("secret")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if resolves != 1 {
		t.Errorf("`security default-keychain` ran %d times across 3 Puts, want 1", resolves)
	}
}
