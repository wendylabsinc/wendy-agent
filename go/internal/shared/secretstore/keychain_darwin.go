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
// `security` has no global one), so a write macOS cannot satisfy is answered
// with a blocking modal instead of an error. checkWritableKeychain settles
// beforehand — using read-only probes that never draw UI — whether this
// process is in a state where that can happen, and Put declines rather than
// hand `security` a write it would turn into a dialog. Callers keep the
// secret wherever it already lives (config.dehydrate leaves it inline), so a
// refusal costs storage hardening, never the secret itself.
func (k keychain) Put(account string, secret []byte) error {
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
	// deterministic-failure guard, not a live bug. It runs before the
	// keychain probes because it needs no process state to decide.
	if len(cmdLine) >= 4000 {
		return fmt.Errorf("secret too large for security(1) stdin line (%d bytes, limit ~4096): refusing truncated write", len(cmdLine))
	}
	if err := checkWritableKeychain(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	_, err := RunSecurity(ctx, cmdLine, "-i")
	return err
}

// Delete is gated on the same probes as Put: deleting from a locked keychain
// raises the unlock dialog, and tlscache calls Delete from a background
// goroutine to evict a broken session — a prompt raised there would reach the
// user with no CLI output to explain it. Skipping leaves an item that nothing
// references, which is inert.
func (k keychain) Delete(account string) {
	if err := checkWritableKeychain(); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	_, _ = RunSecurity(ctx, "", "delete-generic-password", "-s", k.service, "-a", account)
}
