package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
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

// testPrivKeys maps a testSPKI SPKI to its private key, for asLocalKey's
// self-signed certificate.
var testPrivKeys sync.Map

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
	testPrivKeys.Store(string(der), k)
	return der
}

// asLocalKey returns the handler context the real Authorizer builds for a
// client certificate carrying spki.
func asLocalKey(t *testing.T, store *trustedkeys.Store, spki []byte, method string) context.Context {
	t.Helper()
	k, _ := testPrivKeys.Load(string(spki))
	key := k.(*ecdsa.PrivateKey)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}}})
	authz := &interceptor.Authorizer{Logger: zap.NewNop(), Listener: interceptor.ListenerMTLS, Keys: store}
	var got context.Context
	_, err = authz.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, func(ctx context.Context, _ any) (any, error) {
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

// The Authorizer checks the owner role before the store lock. Owners A and B
// both pass it, then remove each other concurrently: exactly one removal may
// land, the loser is refused inside the lock (it is no longer an owner), and
// a removed owner cannot re-add a key either.
func TestTrustedKeysServiceRechecksActorInsideLock(t *testing.T) {
	store, err := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := testSPKI(t), testSPKI(t), testSPKI(t)
	for _, k := range [][]byte{a, b, c} {
		if _, err := store.Add(k, trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	core, logs := observer.New(zapcore.InfoLevel)
	svc := NewTrustedKeysService(zap.New(core), store)
	ctxA := asLocalKey(t, store, a, keysPrefix+"RemoveTrustedKey")
	ctxB := asLocalKey(t, store, b, keysPrefix+"RemoveTrustedKey")

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, call := range []struct {
		ctx    context.Context
		target []byte
	}{{ctxA, b}, {ctxB, a}} {
		go func() {
			<-start
			_, err := svc.RemoveTrustedKey(call.ctx, &agentpbv2.RemoveTrustedKeyRequest{Fingerprint: trustedkeys.Fingerprint(call.target)})
			errs <- err
		}()
	}
	close(start)
	var ok, denied int
	for range 2 {
		switch err := <-errs; status.Code(err) {
		case codes.OK:
			ok++
		case codes.PermissionDenied:
			denied++
		default:
			t.Fatalf("mutual removal: unexpected error %v", err)
		}
	}
	if ok != 1 || denied != 1 {
		t.Fatalf("mutual removal: %d succeeded, %d refused; want exactly one each", ok, denied)
	}
	if store.Len() != 2 {
		t.Fatalf("keys after mutual removal = %d, want 2 (winner + C)", store.Len())
	}
	refused := logs.FilterField(zap.String("reason", "actor not owner")).All()
	if len(refused) != 1 || refused[0].ContextMap()["action"] != "remove_refused" {
		t.Fatalf("refusal audit rows = %v, want one remove_refused", refused)
	}

	loser := ctxB
	if role, _ := store.Lookup(b); role == trustedkeys.RoleOwner {
		loser = ctxA
	}
	if _, err := svc.AddTrustedKey(loser, &agentpbv2.AddTrustedKeyRequest{
		PublicKey: testSPKI(t), Role: agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OWNER}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("add by a removed owner = %v, want PermissionDenied", err)
	}
}

// An Add/Remove that passed the Authorizer just before the device enrolled
// must not write: the store re-checks dormancy under its lock.
func TestTrustedKeysServiceRefusesWritesAfterEnrollment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted-keys.json")
	store, err := trustedkeys.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, other := testSPKI(t), testSPKI(t)
	store.Add(owner, trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now())
	store.Add(other, trustedkeys.RoleViewer, "", trustedkeys.AddedByInstall, time.Now())
	core, logs := observer.New(zapcore.InfoLevel)
	svc := NewTrustedKeysService(zap.New(core), store)
	addCtx := asLocalKey(t, store, owner, keysPrefix+"AddTrustedKey")
	rmCtx := asLocalKey(t, store, owner, keysPrefix+"RemoveTrustedKey")
	before, _ := os.ReadFile(path)

	store.SetDormant(true) // enrollment lands between Authorize and the write
	_, err = svc.AddTrustedKey(addCtx, &agentpbv2.AddTrustedKeyRequest{
		PublicKey: testSPKI(t), Role: agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OWNER})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("add after enrollment = %v, want FailedPrecondition", err)
	}
	_, err = svc.RemoveTrustedKey(rmCtx, &agentpbv2.RemoveTrustedKeyRequest{Fingerprint: trustedkeys.Fingerprint(other)})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("remove after enrollment = %v, want FailedPrecondition", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("keys file written after enrollment")
	}
	if n := logs.FilterField(zap.String("reason", "cloud-enrolled")).Len(); n != 2 {
		t.Fatalf("audit rows = %d, want 2", n)
	}
}
