package commands

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

// trustedKeysSeedFile is the config-partition file the agent consumes on first
// boot into /etc/wendy-agent/trusted-keys.json (WDY-3882).
const trustedKeysSeedFile = "trusted-keys.json"

// localAccessOptions are the install flags that decide which public keys the
// device trusts as its owners before first boot.
type localAccessOptions struct {
	noLocalKey      bool     // --no-local-key: do not seed this CLI's key
	trustedKeyFiles []string // --trusted-key: further public keys to seed
}

// trustedKeyEntry and trustedKeysFile mirror the agent's trusted-keys.json
// (internal/agent/trustedkeys); the agent rejects unknown fields.
type trustedKeyEntry struct {
	PublicKey string    `json:"public_key"`
	Role      string    `json:"role"`
	Label     string    `json:"label,omitempty"`
	AddedAt   time.Time `json:"added_at"`
	AddedBy   string    `json:"added_by"`
}

type trustedKeysFile struct {
	Keys []trustedKeyEntry `json:"keys"`
}

// localKeyPath is a variable so tests do not touch the real config directory.
var localKeyPath = localkey.Path

// resolveTrustedKeysSeed builds the trusted-keys seed: this CLI's local
// operator key (unless --no-local-key) plus every --trusted-key, all as owner.
// It returns nil only when no key is seeded, after warning that the device
// then boots with today's open pre-enrollment path.
func resolveTrustedKeysSeed(opts localAccessOptions, out io.Writer) ([]byte, error) {
	now := time.Now().UTC().Truncate(time.Second)
	var f trustedKeysFile
	seen := map[string]string{}
	add := func(spki []byte, label string) error {
		sum := sha256.Sum256(spki)
		fp := "sha256:" + hex.EncodeToString(sum[:])
		if prev, dup := seen[fp]; dup {
			return fmt.Errorf("%s and %s are the same key (%s)", prev, label, fp)
		}
		seen[fp] = label
		f.Keys = append(f.Keys, trustedKeyEntry{
			PublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki})),
			Role:      "owner",
			Label:     label,
			AddedAt:   now,
			AddedBy:   "install",
		})
		fmt.Fprintf(out, "Owner key for the device: %s (%s)\n", label, fp)
		return nil
	}

	if !opts.noLocalKey {
		path, err := localKeyPath()
		if err != nil {
			return nil, err
		}
		key, err := localkey.LoadOrCreate(path)
		if err != nil {
			return nil, fmt.Errorf("loading the local operator key (pass --no-local-key to install without one): %w", err)
		}
		spki, err := localkey.PublicKeyDER(key)
		if err != nil {
			return nil, err
		}
		if err := add(spki, localKeyLabel()); err != nil {
			return nil, err
		}
	}
	for _, path := range opts.trustedKeyFiles {
		spki, err := readTrustedKeyFile(path)
		if err != nil {
			return nil, fmt.Errorf("--trusted-key %s: %w", path, err)
		}
		if err := add(spki, filepath.Base(path)); err != nil {
			return nil, fmt.Errorf("--trusted-key: %w", err)
		}
	}

	if len(f.Keys) == 0 {
		fmt.Fprintln(out, tui.WarningMessage("--no-local-key: no owner key is seeded. Until it is enrolled, the device accepts unauthenticated connections from anyone on its network, as images did before local access."))
		return nil, nil
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// readTrustedKeyFile accepts a SubjectPublicKeyInfo as PEM ("PUBLIC KEY") or
// DER. A private key is refused rather than reduced to its public half: the
// file should never have been handed around.
func readTrustedKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	der := b
	if block, _ := pem.Decode(b); block != nil {
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("PEM block is %q, want PUBLIC KEY", block.Type)
		}
		der = block.Bytes
	}
	if _, err := x509.ParsePKIXPublicKey(der); err != nil {
		return nil, fmt.Errorf("not a public key: %w", err)
	}
	return der, nil
}

// localKeyLabel is "<hostname>/<user>", so a device owner can tell whose key it is.
func localKeyLabel() string {
	host, _ := os.Hostname()
	name := "unknown"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if host == "" {
		host = "unknown"
	}
	return host + "/" + name
}
