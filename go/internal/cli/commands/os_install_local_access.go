package commands

import (
	"crypto"
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
	"regexp"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

// The seed is only honoured by agents built from the release that carries the
// agent's trusted-keys support (#2366) and this installer; an older agent
// ignores it and boots open. Images and the agent are versioned separately
// (0.19.5 vs 2026.10.18-115727), so each has its own floor. Until a floor is
// set, localKeyVersionUnset refuses every seed (ruling 2026-10-18 17:13).
const (
	localKeyVersionUnset = "v0.0.0-unset"
	// TODO(WDY-3882): the first wendy-agent release that honours the seed.
	minLocalKeyAgentVersion = localKeyVersionUnset
	// TODO(WDY-3882): the first WendyOS image release whose baked agent and
	// units honour the seed.
	minLocalKeyImageVersion = localKeyVersionUnset
)

// releasedVersion is the exact shape of a published release: WendyOS images
// are 0.19.5, the agent 2026.10.18-115727. Anything else (nightly-…, a PR
// build, a -dev or -rc suffix) cannot be placed against a release, and
// CompareVersions would even rank a -dev build above every release.
var releasedVersion = map[string]*regexp.Regexp{
	"WendyOS":     regexp.MustCompile(`^v?\d+\.\d+\.\d+$`),
	"wendy-agent": regexp.MustCompile(`^v?\d{4}\.\d{2}\.\d{2}-\d{6}$`),
}

// checkLocalKeyVersion refuses to seed unless got, the version of what will
// boot ("WendyOS" image or "wendy-agent"), is a released version at or above
// min.
func checkLocalKeyVersion(what, got, min string) error {
	if min != localKeyVersionUnset && releasedVersion[what].MatchString(got) && version.CompareVersions(got, min) >= 0 {
		return nil
	}
	floor := min
	if min == localKeyVersionUnset {
		floor += " (not released yet)"
	}
	return fmt.Errorf("cannot seed an owner key: %s %q may ignore it and boot accepting unauthenticated connections; seeding needs %s %s or newer. Pass --no-local-key to install without an owner key", what, got, what, floor)
}

// checkSeedAgent applies the agent floor to the agent an install stages on the
// config partition, which replaces the baked one on first boot. No seed, no gate.
func checkSeedAgent(trustedKeys []byte, agentVersion string) error {
	if len(trustedKeys) == 0 {
		return nil
	}
	return checkLocalKeyVersion("wendy-agent", agentVersion, minLocalKeyAgentVersion)
}

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

// resolveTrustedKeysSeed builds the owner-key seed for an install of WendyOS
// imageVersion and refuses it when that image's agent may ignore it. The
// agent staged on the config partition is gated separately (checkSeedAgent).
func resolveTrustedKeysSeed(opts localAccessOptions, imageVersion string, out io.Writer) ([]byte, error) {
	data, err := buildTrustedKeysSeed(opts, out)
	if err != nil || data == nil {
		return data, err
	}
	if err := checkLocalKeyVersion("WendyOS", imageVersion, minLocalKeyImageVersion); err != nil {
		return nil, err
	}
	return data, nil
}

// buildTrustedKeysSeed builds the trusted-keys seed: this CLI's local
// operator key (unless --no-local-key) plus every --trusted-key, all as owner.
// It returns nil only when no key is seeded, after warning that the device
// then boots with today's open pre-enrollment path.
func buildTrustedKeysSeed(opts localAccessOptions, out io.Writer) ([]byte, error) {
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
		return nil
	}

	if !opts.noLocalKey {
		key, err := loadLocalKey()
		if err != nil {
			return nil, err
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

// loadLocalKey loads this CLI's local operator key, creating it on first use.
func loadLocalKey() (crypto.Signer, error) {
	path, err := localKeyPath()
	if err != nil {
		return nil, err
	}
	key, err := localkey.LoadOrCreate(path)
	if err != nil {
		return nil, fmt.Errorf("loading the local operator key (pass --no-local-key to install without one): %w", err)
	}
	return key, nil
}

// printOwnerKeys lists the keys in a seed that has been written and read back.
// It is the only place the CLI claims an owner key for a device.
func printOwnerKeys(out io.Writer, seed []byte) {
	var f trustedKeysFile
	if len(seed) == 0 || json.Unmarshal(seed, &f) != nil {
		return
	}
	for _, k := range f.Keys {
		if block, _ := pem.Decode([]byte(k.PublicKey)); block != nil {
			sum := sha256.Sum256(block.Bytes)
			fmt.Fprintf(out, "Owner key for the device: %s (sha256:%s)\n", k.Label, hex.EncodeToString(sum[:]))
		}
	}
}

// rejectLocalAccessFlags refuses --no-local-key and --trusted-key on install
// types that seed no owner key, rather than ignoring them.
func rejectLocalAccessFlags(opts localAccessOptions, installType string) error {
	if opts.noLocalKey || len(opts.trustedKeyFiles) > 0 {
		return usageErrorf("--no-local-key and --trusted-key are not supported on this install type (%s): it seeds no owner key", installType)
	}
	return nil
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
	if s := os.Getenv("SUDO_USER"); s != "" && os.Geteuid() == 0 {
		name = s // the elevated Thor run installs for the user who ran sudo
	}
	if host == "" {
		host = "unknown"
	}
	return host + "/" + name
}
