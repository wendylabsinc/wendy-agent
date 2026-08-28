package secretstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// This file exists to keep `security` writes from ever becoming a macOS
// dialog. Two states turn a write into UI instead of an error:
//
//  1. The user's default keychain does not resolve. `SecKeychainCopyDefault`
//     reads ~/Library/Preferences/com.apple.security.plist and falls back to
//     $HOME/Library/Keychains/login.keychain-db, so any invocation whose HOME
//     has neither — `sudo wendy ...` (HOME=/var/root), a launchd job, a
//     sandboxed or non-login session — hits it. macOS answers the write with
//     "A keychain cannot be found to store <account>", a blocking modal whose
//     "Reset To Defaults" button rewrites the user's keychain search list.
//     Reproduce the underlying failure without raising the dialog:
//     `HOME=$(mktemp -d) security default-keychain`.
//
//  2. The target keychain is locked, which a write answers with the unlock
//     prompt.
//
// Both are detectable with commands that only read: `default-keychain` reads
// a plist, `show-keychain-info` reports lock state and returns
// "User interaction is not allowed." rather than prompting. Probing first and
// declining is the only way to guarantee silence while `security(1)` remains
// the executor — and it has to remain the executor, because items it creates
// carry an ACL naming `security` itself, which is what lets every rebuilt
// wendy binary keep reading them.

// defaultKeychain caches `security default-keychain`'s answer for the life of
// the process. What it depends on — HOME and the user's security preferences
// — cannot change under a running CLI, so one subprocess covers every write.
// Lock state is deliberately not cached: a keychain can lock mid-run, and a
// stale "unlocked" verdict is exactly the prompt this file exists to prevent.
var (
	defaultKeychainMu   sync.Mutex
	defaultKeychainPath string
	defaultKeychainErr  error
	defaultKeychainDone bool
)

func resetKeychainProbeForTest() {
	defaultKeychainMu.Lock()
	defer defaultKeychainMu.Unlock()
	defaultKeychainPath, defaultKeychainErr, defaultKeychainDone = "", nil, false
}

func defaultKeychain() (string, error) {
	defaultKeychainMu.Lock()
	defer defaultKeychainMu.Unlock()
	if !defaultKeychainDone {
		defaultKeychainPath, defaultKeychainErr = resolveDefaultKeychain()
		defaultKeychainDone = true
	}
	return defaultKeychainPath, defaultKeychainErr
}

func resolveDefaultKeychain() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	out, err := RunSecurity(ctx, "", "default-keychain", "-d", "user")
	if err != nil {
		return "", fmt.Errorf("no default keychain for this user: %w", err)
	}
	// `security` prints the path indented and double-quoted.
	path := strings.Trim(strings.TrimSpace(string(out)), `"`)
	if path == "" {
		return "", fmt.Errorf("no default keychain for this user: `security default-keychain` named none")
	}
	return path, nil
}

// checkWritableKeychain reports nil only when a `security` write is certain
// not to raise UI. Its error is returned verbatim to Put's caller, so it
// names both the state and the remedy.
func checkWritableKeychain() error {
	path, err := defaultKeychain()
	if err != nil {
		return fmt.Errorf("skipping macOS Keychain write to avoid a blocking system prompt: %w"+
			" (set WENDY_SECRET_STORE=file to silence this path permanently)", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	if _, err := RunSecurity(ctx, "", "show-keychain-info", path); err != nil {
		return fmt.Errorf("skipping macOS Keychain write to avoid a blocking unlock prompt: %s is locked or unreadable: %w"+
			" (unlock it with `security unlock-keychain`)", path, err)
	}
	return nil
}
