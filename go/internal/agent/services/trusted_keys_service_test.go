package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func testSPKI(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// asLocalKey returns the handler context the real Authorizer builds for a
// client certificate carrying spki.
func asLocalKey(t *testing.T, store *trustedkeys.Store, spki []byte, method string) context.Context {
	t.Helper()
	leaf := &x509.Certificate{RawSubjectPublicKeyInfo: spki, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}}})
	authz := &interceptor.Authorizer{Logger: zap.NewNop(), Listener: interceptor.ListenerMTLS, Keys: store}
	var got context.Context
	_, err := authz.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, func(ctx context.Context, _ any) (any, error) {
		got = ctx
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Authorizer refused the owner: %v", err)
	}
	return got
}

const keysPrefix = "/wendy.agent.services.v2.WendyTrustedKeysService/"

func TestTrustedKeysServiceAuditsChangesAndKeepsLastOwner(t *testing.T) {
	store, err := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	ownerSPKI := testSPKI(t)
	if _, err := store.Add(ownerSPKI, trustedkeys.RoleOwner, "laptop", trustedkeys.AddedByInstall, time.Now()); err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zapcore.InfoLevel)
	svc := NewTrustedKeysService(zap.New(core), store)
	ownerFP := trustedkeys.Fingerprint(ownerSPKI)
	owner := asLocalKey(t, store, ownerSPKI, keysPrefix+"AddTrustedKey")

	newSPKI := testSPKI(t)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: newSPKI})
	added, err := svc.AddTrustedKey(owner, &agentpbv2.AddTrustedKeyRequest{
		PublicKey: pemKey, Role: agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OPERATOR, Label: "ci"})
	if err != nil {
		t.Fatalf("AddTrustedKey: %v", err)
	}
	if added.GetKey().GetAddedBy() != ownerFP || added.GetKey().GetRole() != agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OPERATOR {
		t.Fatalf("added key = %+v", added.GetKey())
	}
	if _, err := svc.AddTrustedKey(owner, &agentpbv2.AddTrustedKeyRequest{PublicKey: newSPKI, Role: agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_VIEWER}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate add = %v, want AlreadyExists", err)
	}
	if _, err := svc.AddTrustedKey(owner, &agentpbv2.AddTrustedKeyRequest{PublicKey: testSPKI(t)}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("add without role = %v, want InvalidArgument", err)
	}

	list, err := svc.ListTrustedKeys(owner, &agentpbv2.ListTrustedKeysRequest{})
	if err != nil || len(list.GetKeys()) != 2 {
		t.Fatalf("ListTrustedKeys = %v, %v; want 2 keys", list, err)
	}

	if _, err := svc.RemoveTrustedKey(owner, &agentpbv2.RemoveTrustedKeyRequest{Fingerprint: ownerFP}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("removing the last owner = %v, want FailedPrecondition", err)
	}
	if _, err := svc.RemoveTrustedKey(owner, &agentpbv2.RemoveTrustedKeyRequest{Fingerprint: added.GetKey().GetFingerprint()}); err != nil {
		t.Fatalf("RemoveTrustedKey: %v", err)
	}

	want := []struct{ action, fingerprint string }{
		{"add", added.GetKey().GetFingerprint()},
		{"remove_refused", ownerFP},
		{"remove", added.GetKey().GetFingerprint()},
	}
	audit := logs.FilterField(zap.String("audit", "trusted_keys")).All()
	if len(audit) != len(want) {
		t.Fatalf("audit rows = %d, want %d: %v", len(audit), len(want), audit)
	}
	for i, w := range want {
		f := audit[i].ContextMap()
		if f["action"] != w.action || f["fingerprint"] != w.fingerprint || f["actor_fingerprint"] != ownerFP {
			t.Errorf("audit row %d = %v, want action=%s fingerprint=%s actor=%s", i, f, w.action, w.fingerprint, ownerFP)
		}
	}
}

// Defense in depth: the Authorizer already keeps non-owners out; the service
// refuses a call that reaches it without an owner key principal.
func TestTrustedKeysServiceRefusesCallsWithoutOwnerPrincipal(t *testing.T) {
	store, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	svc := NewTrustedKeysService(zap.NewNop(), store)
	if _, err := svc.ListTrustedKeys(context.Background(), &agentpbv2.ListTrustedKeysRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("no principal = %v, want PermissionDenied", err)
	}
}
