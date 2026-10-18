package commands

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
)

func useLocalKeyPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), localkey.FileName)
	orig := localKeyPath
	localKeyPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { localKeyPath = orig })
	return path
}

func writePublicKeyFile(t *testing.T, name string) (string, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki}), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, spki
}

// decodeSeed parses the seed strictly, as the agent's trusted-keys loader does.
func decodeSeed(t *testing.T, data []byte) trustedKeysFile {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f trustedKeysFile
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("seed does not parse: %v\n%s", err, data)
	}
	return f
}

func seedSPKI(t *testing.T, e trustedKeyEntry) []byte {
	t.Helper()
	block, _ := pem.Decode([]byte(e.PublicKey))
	if block == nil || block.Type != "PUBLIC KEY" {
		t.Fatalf("entry %q has no PUBLIC KEY PEM", e.Label)
	}
	return block.Bytes
}

func TestResolveTrustedKeysSeedSeedsTheLocalKeyAsOwner(t *testing.T) {
	keyPath := useLocalKeyPath(t)
	var out bytes.Buffer
	data, err := resolveTrustedKeysSeed(localAccessOptions{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	f := decodeSeed(t, data)
	if len(f.Keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(f.Keys))
	}
	e := f.Keys[0]
	if e.Role != "owner" || e.AddedBy != "install" || e.AddedAt.IsZero() || !strings.Contains(e.Label, "/") {
		t.Errorf("entry = %+v, want owner/install with a host/user label and added_at", e)
	}
	key, err := localkey.LoadOrCreate(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := localkey.PublicKeyDER(key)
	if !bytes.Equal(seedSPKI(t, e), want) {
		t.Error("seeded key is not the CLI's local operator key")
	}
	if strings.Contains(string(data), "PRIVATE") {
		t.Fatal("seed carries private key material")
	}
	if !strings.Contains(out.String(), "sha256:") {
		t.Errorf("output does not name the owner key fingerprint: %q", out.String())
	}
}

func TestResolveTrustedKeysSeedAddsTrustedKeyFiles(t *testing.T) {
	useLocalKeyPath(t)
	ops, opsSPKI := writePublicKeyFile(t, "ops.pub")
	data, err := resolveTrustedKeysSeed(localAccessOptions{trustedKeyFiles: []string{ops}}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	f := decodeSeed(t, data)
	if len(f.Keys) != 2 || f.Keys[1].Label != "ops.pub" || f.Keys[1].Role != "owner" || !bytes.Equal(seedSPKI(t, f.Keys[1]), opsSPKI) {
		t.Fatalf("keys = %+v, want the local key then ops.pub as owner", f.Keys)
	}
}

func TestResolveTrustedKeysSeedNoLocalKey(t *testing.T) {
	keyPath := useLocalKeyPath(t)

	var out bytes.Buffer
	data, err := resolveTrustedKeysSeed(localAccessOptions{noLocalKey: true}, &out)
	if err != nil || data != nil {
		t.Fatalf("got (%q, %v), want no seed", data, err)
	}
	if !strings.Contains(out.String(), "--no-local-key") {
		t.Errorf("no warning printed: %q", out.String())
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Error("--no-local-key still created a local operator key")
	}

	ops, _ := writePublicKeyFile(t, "ops.pub")
	data, err = resolveTrustedKeysSeed(localAccessOptions{noLocalKey: true, trustedKeyFiles: []string{ops}}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if f := decodeSeed(t, data); len(f.Keys) != 1 || f.Keys[0].Label != "ops.pub" {
		t.Fatalf("keys = %+v, want only ops.pub", f.Keys)
	}
}

func TestResolveTrustedKeysSeedRejectsBadKeyFiles(t *testing.T) {
	useLocalKeyPath(t)
	ops, _ := writePublicKeyFile(t, "ops.pub")

	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalECPrivateKey(k)
	priv := filepath.Join(t.TempDir(), "id.pem")
	if err := os.WriteFile(priv, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(t.TempDir(), "junk.pub")
	if err := os.WriteFile(junk, []byte("not a key"), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, files := range map[string][]string{
		"private key": {priv},
		"not a key":   {junk},
		"missing":     {filepath.Join(t.TempDir(), "absent.pub")},
		"duplicate":   {ops, ops},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveTrustedKeysSeed(localAccessOptions{trustedKeyFiles: files}, &bytes.Buffer{}); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestWriteConfigFilesWritesTheTrustedKeysSeed(t *testing.T) {
	dir := t.TempDir()
	seed := []byte(`{"keys":[]}` + "\n")
	if err := writeConfigFiles(dir, nil, nil, "", nil, seed); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, trustedKeysSeedFile)
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, seed) {
		t.Fatalf("seed on the partition = (%q, %v), want %q", got, err, seed)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("seed mode %#o, want 0600", fi.Mode().Perm())
	}

	if err := writeConfigFiles(dir, nil, nil, "", nil, nil); err != nil {
		t.Fatal(err)
	}
}

// lossyTarget drops what it is given, like a write that never reached the card.
type lossyTarget struct{ dirTarget }

func (l lossyTarget) WriteFile(name string, data []byte, perm os.FileMode) error {
	return l.dirTarget.WriteFile(name, nil, perm)
}

func TestWriteConfigFilesFailsWhenTheSeedDoesNotReadBack(t *testing.T) {
	err := writeConfigFilesTo(lossyTarget{dirTarget(t.TempDir())}, nil, nil, "", nil, []byte(`{"keys":[]}`))
	if err == nil || !strings.Contains(err.Error(), "read-back") {
		t.Fatalf("err = %v, want a read-back failure", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-back failed for the wrong reason")
	}
}
