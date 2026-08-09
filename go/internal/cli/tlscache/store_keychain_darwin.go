package tlscache

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// keychainService names the Keychain items holding wendy session tickets.
const keychainService = "wendy-tls-session"

const securityTimeout = 5 * time.Second

// runSecurity invokes /usr/bin/security (same pattern as
// wifi_scan_darwin.go's lookupKeychainPassword). Swapped in tests.
var runSecurity = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.Output()
}

// keychainStore keeps session tickets in the user's login Keychain. It is
// OPT-IN ONLY (WENDY_TLS_SESSION_STORE=keychain), not the macOS default — see
// newPlatformStore in store_select_darwin.go for why.
//
// Items are created by (and read back through) /usr/bin/security itself, whose
// default ACL covers it — reads must never prompt; any prompt/denial surfaces
// as a plain cache miss. put carries the residual hazard that motivated the
// default flip: `security` exposes no way to suppress user interaction, so
// where the keychain search list does not resolve, macOS answers the write
// with a blocking "A keychain cannot be found to store ..." modal. Nothing
// here can prevent that, which is why callers have to ask for this backend.
type keychainStore struct{}

func newKeychainStore() sessionStore { return keychainStore{} }

func (keychainStore) get(key string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	out, err := runSecurity(ctx, "", "find-generic-password", "-s", keychainService, "-a", key, "-w")
	if err != nil {
		return nil // not found, denied, or security failed — cache miss
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil
	}
	return blob
}

func (keychainStore) put(key string, blob []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	// `security -i` reads the command from stdin so the ticket secret never
	// appears on argv (argv is world-readable via ps). base64 and the hex key
	// contain no whitespace, so no quoting is needed.
	cmdLine := fmt.Sprintf("add-generic-password -U -s %s -a %s -j wendy-cli-tls-session -w %s\n",
		keychainService, key, base64.StdEncoding.EncodeToString(blob))
	_, _ = runSecurity(ctx, cmdLine, "-i")
}

func (keychainStore) delete(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	_, _ = runSecurity(ctx, "", "delete-generic-password", "-s", keychainService, "-a", key)
}
