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

func (k keychain) Put(account string, secret []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	// `security -i` reads the command from stdin so the secret never appears
	// on argv (argv is world-readable via ps). base64 and account names
	// contain no whitespace, so no quoting is needed.
	cmdLine := fmt.Sprintf("add-generic-password -U -s %s -a %s -j wendy-cli-secret -w %s\n",
		k.service, account, base64.StdEncoding.EncodeToString(secret))
	_, err := RunSecurity(ctx, cmdLine, "-i")
	return err
}

func (k keychain) Delete(account string) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	_, _ = RunSecurity(ctx, "", "delete-generic-password", "-s", k.service, "-a", account)
}
