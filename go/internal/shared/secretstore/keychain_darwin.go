package secretstore

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const securityTimeout = 5 * time.Second

// RunSecurity invokes /usr/bin/security. Package-level so tests fake it.
var RunSecurity = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.Output()
}

// keychain stores secrets in the user's login Keychain under one service
// name. Items are created by (and read back through) /usr/bin/security
// itself, whose default ACL covers it — reads must never prompt; any
// prompt/denial surfaces as a plain miss.
type keychain struct{ service string }

// NewKeychain returns a Keychain-backed Store scoped to the given service.
func NewKeychain(service string) Store { return keychain{service: service} }

func (k keychain) Get(account string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	out, err := RunSecurity(ctx, "", "find-generic-password", "-s", k.service, "-a", account, "-w")
	if err != nil {
		return nil // not found, denied, or security failed — miss
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil
	}
	return blob
}

// Put carries a hazard reads do not: `security` exposes no way to suppress
// user interaction (`add-generic-password` has no no-interaction flag and
// `security` has no global one), so in a context where the keychain search
// list does not resolve — a sandboxed process, a non-login session — macOS
// answers the write with a blocking "A keychain cannot be found to store ..."
// modal. Nothing here can prevent that, so callers that must never interrupt
// the user cannot make this backend their default; tlscache's
// newPlatformStore documents that reasoning for the session-ticket cache.
func (k keychain) Put(account string, secret []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	// `security -i` reads the command from stdin so the secret never appears
	// on argv (argv is world-readable via ps). base64 and account names
	// contain no whitespace, so no quoting is needed.
	cmdLine := fmt.Sprintf("add-generic-password -U -s %s -a %s -j wendy-cli-secret -w %s\n",
		k.service, account, base64.StdEncoding.EncodeToString(secret))
	// `security -i` consumes stdin in 4096-byte lines: a command line at or
	// past that boundary can be truncated — and if the cut lands on the
	// trailing newline, the truncated write can succeed instead of erroring,
	// silently storing a corrupt value. Refuse before that can happen rather
	// than risk it. Today's payloads (a P-256 PEM key, short tokens, TLS
	// session-ticket blobs) are all well under this, so this is a
	// deterministic-failure guard, not a live bug.
	if len(cmdLine) >= 4000 {
		return fmt.Errorf("secret too large for security(1) stdin line (%d bytes, limit ~4096): refusing truncated write", len(cmdLine))
	}
	_, err := RunSecurity(ctx, cmdLine, "-i")
	return err
}

func (k keychain) Delete(account string) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	_, _ = RunSecurity(ctx, "", "delete-generic-password", "-s", k.service, "-a", account)
}
