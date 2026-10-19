package commands

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// fakeTrustedKeys records requests and answers with canned keys or a refusal.
type fakeTrustedKeys struct {
	agentpbv2.UnimplementedWendyTrustedKeysServiceServer
	keys    []*agentpbv2.TrustedKey
	refusal error
	added   *agentpbv2.AddTrustedKeyRequest
	removed string
}

func (f *fakeTrustedKeys) ListTrustedKeys(context.Context, *agentpbv2.ListTrustedKeysRequest) (*agentpbv2.ListTrustedKeysResponse, error) {
	if f.refusal != nil {
		return nil, f.refusal
	}
	return &agentpbv2.ListTrustedKeysResponse{Keys: f.keys}, nil
}

func (f *fakeTrustedKeys) AddTrustedKey(_ context.Context, req *agentpbv2.AddTrustedKeyRequest) (*agentpbv2.AddTrustedKeyResponse, error) {
	if f.refusal != nil {
		return nil, f.refusal
	}
	f.added = req
	return &agentpbv2.AddTrustedKeyResponse{Key: &agentpbv2.TrustedKey{
		Fingerprint: certs.SPKIFingerprint(req.GetPublicKey()), Role: req.GetRole(), Label: req.GetLabel(),
	}}, nil
}

func (f *fakeTrustedKeys) RemoveTrustedKey(_ context.Context, req *agentpbv2.RemoveTrustedKeyRequest) (*agentpbv2.RemoveTrustedKeyResponse, error) {
	if f.refusal != nil {
		return nil, f.refusal
	}
	f.removed = req.GetFingerprint()
	return &agentpbv2.RemoveTrustedKeyResponse{}, nil
}

func trustedKeysClient(t *testing.T, srv agentpbv2.WendyTrustedKeysServiceServer) agentpbv2.WendyTrustedKeysServiceClient {
	t.Helper()
	g := grpc.NewServer()
	if srv != nil {
		agentpbv2.RegisterWendyTrustedKeysServiceServer(g, srv)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go g.Serve(ln) //nolint:errcheck
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return agentpbv2.NewWendyTrustedKeysServiceClient(conn)
}

func keysCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// showOutput is `wendy auth local-key show` for a fresh config dir.
func showOutput(t *testing.T) string {
	t.Helper()
	t.Setenv("WENDY_CONFIG_DIR", t.TempDir())
	var out bytes.Buffer
	if err := runAuthLocalKeyShow(&out, false); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestLocalKeyShowOutputIsAddableAsIs(t *testing.T) {
	shown := showOutput(t)
	path := filepath.Join(t.TempDir(), "alice.pub")
	if err := os.WriteFile(path, []byte(shown), 0o600); err != nil {
		t.Fatal(err)
	}
	der, err := readPublicKeyArg(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	fp := certs.SPKIFingerprint(der)
	if !strings.Contains(shown, "Fingerprint: "+fp) {
		t.Fatalf("show printed a fingerprint other than its key's %s:\n%s", fp, shown)
	}
	// The same key the dial presents.
	key, err := localkey.LoadOrCreate(filepath.Join(os.Getenv("WENDY_CONFIG_DIR"), localkey.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if mine, _ := localkey.Fingerprint(key); mine != fp {
		t.Fatalf("show printed %s, local key is %s", fp, mine)
	}
}

func TestReadPublicKeyArgRefusesPrivateKeysAndJunk(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, localkey.FileName)
	if _, err := localkey.LoadOrCreate(priv); err != nil {
		t.Fatal(err)
	}
	if _, err := readPublicKeyArg(priv, nil); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("private key accepted: %v", err)
	}
	if _, err := readPublicKeyArg("-", strings.NewReader("not a key")); err == nil {
		t.Fatal("junk accepted")
	}
}

func TestDeviceKeysAddSendsKeyAndRole(t *testing.T) {
	shown := showOutput(t)
	der, err := readPublicKeyArg("-", strings.NewReader(shown))
	if err != nil {
		t.Fatal(err)
	}
	role, err := parseTrustedKeyRole("Operator")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTrustedKeys{}
	var out bytes.Buffer
	if err := runDeviceKeysAdd(keysCtx(t), trustedKeysClient(t, fake), &out, der, role, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParsePKIXPublicKey(fake.added.GetPublicKey()); err != nil {
		t.Fatalf("device received an unparseable key: %v", err)
	}
	if fake.added.GetRole() != agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OPERATOR || fake.added.GetLabel() != "alice" {
		t.Fatalf("request = %v", fake.added)
	}
	if !strings.Contains(out.String(), "as operator") {
		t.Fatalf("output = %q", out.String())
	}
	if _, err := parseTrustedKeyRole("admin"); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestDeviceKeysListMarksThisComputer(t *testing.T) {
	shown := showOutput(t)
	der, _ := readPublicKeyArg("-", strings.NewReader(shown))
	mine := certs.SPKIFingerprint(der)
	fake := &fakeTrustedKeys{keys: []*agentpbv2.TrustedKey{
		{Fingerprint: mine, Role: agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OWNER, Label: "me", AddedBy: "install", AddedAt: 1},
		{Fingerprint: "sha256:other", Role: agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_VIEWER, AddedBy: mine},
	}}
	var out bytes.Buffer
	if err := runDeviceKeysList(keysCtx(t), trustedKeysClient(t, fake), &out, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "me (this computer)") || !strings.Contains(lines[2], "viewer") || strings.Contains(lines[2], "this computer") {
		t.Fatalf("list output:\n%s", out.String())
	}
	out.Reset()
	if err := runDeviceKeysList(keysCtx(t), trustedKeysClient(t, fake), &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"thisComputer": true`) {
		t.Fatalf("json output:\n%s", out.String())
	}
}

func TestDeviceKeysRemove(t *testing.T) {
	fake := &fakeTrustedKeys{}
	var out bytes.Buffer
	if err := runDeviceKeysRemove(keysCtx(t), trustedKeysClient(t, fake), &out, " sha256:abc ", false); err != nil {
		t.Fatal(err)
	}
	if fake.removed != "sha256:abc" {
		t.Fatalf("removed %q", fake.removed)
	}
}

// The device's refusals are its decision; the CLI prints them verbatim.
func TestDeviceKeysSurfaceDeviceRefusalsVerbatim(t *testing.T) {
	for _, refusal := range []error{
		status.Error(codes.FailedPrecondition, "local operator keys are managed by cloud while enrolled"),
		status.Error(codes.FailedPrecondition, "cannot remove the last owner key"),
		status.Error(codes.PermissionDenied, "trusted key management requires an owner key"),
	} {
		client := trustedKeysClient(t, &fakeTrustedKeys{refusal: refusal})
		var out bytes.Buffer
		want := status.Convert(refusal).Message()
		for name, err := range map[string]error{
			"list":   runDeviceKeysList(keysCtx(t), client, &out, false),
			"add":    runDeviceKeysAdd(keysCtx(t), client, &out, nil, agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_VIEWER, "", false),
			"remove": runDeviceKeysRemove(keysCtx(t), client, &out, "sha256:x", false),
		} {
			if err == nil || err.Error() != want {
				t.Errorf("%s: err = %v, want exactly %q", name, err, want)
			}
		}
	}
	// An agent that predates local access does not serve the service.
	err := runDeviceKeysList(keysCtx(t), trustedKeysClient(t, nil), &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "wendy device update") {
		t.Fatalf("old agent: %v", err)
	}
}

func TestLocalKeyShowJSON(t *testing.T) {
	t.Setenv("WENDY_CONFIG_DIR", t.TempDir())
	var out bytes.Buffer
	if err := runAuthLocalKeyShow(&out, true); err != nil {
		t.Fatal(err)
	}
	var shown struct{ Fingerprint, PublicKey string }
	if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(shown.PublicKey))
	if block == nil || block.Type != "PUBLIC KEY" || shown.Fingerprint != certs.SPKIFingerprint(block.Bytes) {
		t.Fatalf("json output:\n%s", out.String())
	}
}
