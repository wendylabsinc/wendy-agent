package interceptor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"math/big"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	_ "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// notServedByGoAgent are agent services the Go agent never registers; their
// methods stay out of the table and are therefore denied.
var notServedByGoAgent = map[string]bool{
	"wendy.agent.services.v1.WendyFileSyncService": true,
	"wendy.agent.services.v2.WendyFileSyncService": true,
	"wendy.agent.services.v2.WendySensorService":   true, // Swift agent only
}

// TestEveryAgentMethodHasARole enumerates every RPC of every agent service
// and every BLE command, and fails on one without a role decision: a new
// mutator cannot ship unmapped (it would be denied at runtime and the agent
// refuses to start with it). It also fails on table entries that name no
// real method, so a typo cannot hide a decision.
func TestEveryAgentMethodHasARole(t *testing.T) {
	seen := map[string]bool{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(string(fd.Package()), "wendy.agent.services.") {
			return true
		}
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			for j := 0; j < sd.Methods().Len(); j++ {
				full := "/" + string(sd.FullName()) + "/" + string(sd.Methods().Get(j).Name())
				seen[full] = true
				_, mapped := methodRoles[full]
				switch {
				case notServedByGoAgent[string(sd.FullName())] && mapped:
					t.Errorf("%s is mapped but its service is listed as not served", full)
				case !notServedByGoAgent[string(sd.FullName())] && !mapped:
					t.Errorf("%s has no role: add it to methodRoles", full)
				}
			}
		}
		return true
	})

	oneof := (&agentpb.BluetoothCommand{}).ProtoReflect().Descriptor().Oneofs().ByName("command")
	for i := 0; i < oneof.Fields().Len(); i++ {
		full := BLEMethodPrefix + string(oneof.Fields().Get(i).Name())
		seen[full] = true
		if _, ok := methodRoles[full]; !ok {
			t.Errorf("BLE command %s has no role: add it to methodRoles", full)
		}
	}

	if len(seen) < 180 {
		t.Fatalf("enumerated only %d methods; agent protos not registered?", len(seen))
	}
	for full := range methodRoles {
		if !seen[full] {
			t.Errorf("methodRoles names %s, which is not an agent method", full)
		}
	}
	for _, set := range []map[string]bool{ownerKeyMethods, peerMethods, tenantUnprovisionMethods} {
		for full := range set {
			if _, ok := methodRoles[full]; !ok {
				t.Errorf("%s is special-cased but has no methodRoles entry", full)
			}
		}
	}
}

const (
	mAddKey      = "/wendy.agent.services.v2.WendyTrustedKeysService/AddTrustedKey"
	mListKeys    = "/wendy.agent.services.v2.WendyTrustedKeysService/ListTrustedKeys"
	mRemoveKey   = "/wendy.agent.services.v2.WendyTrustedKeysService/RemoveTrustedKey"
	mUnprovision = "/wendy.agent.services.v2.WendyProvisioningService/Unprovision"
	mStartACME   = "/wendy.agent.services.v2.WendyProvisioningService/StartACMEProvisioning"
	mShell       = "/wendy.agent.services.v1.WendyShellService/HostShell"
	mDeploy      = "/wendy.agent.services.v2.WendyContainerService/StartContainer"
	mUpdateOS    = "/wendy.agent.services.v2.WendyOSUpdateService/UpdateOS"
	mVersion     = "/wendy.agent.services.v1.WendyAgentService/GetAgentVersion"
	mLogs        = "/wendy.agent.services.v2.WendyTelemetryService/StreamLogs"
	mMeshDial    = "/wendy.agent.services.v2.WendyMeshService/MeshDial"
	mWriteChunks = "/wendy.agent.services.v1.WendyContainerService/WriteChunks"
	mUnmapped    = "/wendy.agent.services.v2.WendySensorService/StreamSensors"
	mBLEWifi     = BLEMethodPrefix + "wifi_connect"
)

// testKeys maps a newKey SPKI to its private key, so leafWithKey can make a
// real self-signed local operator certificate.
var testKeys sync.Map

func newKey(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	testKeys.Store(string(der), k)
	return der
}

// selfSignedLeaf is the shape of a local operator certificate, over a newKey key.
func selfSignedLeaf(spki []byte) *x509.Certificate {
	k, _ := testKeys.Load(string(spki))
	key := k.(*ecdsa.PrivateKey)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		panic(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return leaf
}

// leafWithKey is a certificate carrying spki: a real self-signed one when it
// names no tenant (a local operator certificate), otherwise a literal for
// the CA-issued shapes, whose chain the handshake has already verified.
func leafWithKey(spki []byte, uris ...string) *x509.Certificate {
	if len(uris) == 0 {
		if _, ok := testKeys.Load(string(spki)); ok {
			return selfSignedLeaf(spki)
		}
	}
	leaf := buildLeaf(leafOptions{commonName: "operator"})
	leaf.RawSubjectPublicKeyInfo = spki
	for _, u := range uris {
		parsed, _ := url.Parse(u)
		leaf.URIs = append(leaf.URIs, parsed)
	}
	return leaf
}

func TestAuthorizerRoleMatrix(t *testing.T) {
	store, err := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, operatorKey, viewerKey, unknownKey := newKey(t), newKey(t), newKey(t), newKey(t)
	for _, k := range []struct {
		spki []byte
		role trustedkeys.Role
	}{{ownerKey, trustedkeys.RoleOwner}, {operatorKey, trustedkeys.RoleOperator}, {viewerKey, trustedkeys.RoleViewer}} {
		if _, err := store.Add(k.spki, k.role, "", trustedkeys.AddedByInstall, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	enrolled := certs.Scope{OrgID: 5}
	tenantUser := leafWithKey(unknownKey, "urn:wendy:org:5:user:alice")
	peerDevice := leafWithKey(unknownKey, "urn:wendy:org:5:asset:9")
	otherTenant := leafWithKey(unknownKey, "urn:wendy:org:6:user:mallory")
	// The owner's key inside a certificate that names a tenant is a tenant
	// certificate: the key does not lift it to local owner.
	ownerKeyTenantCert := leafWithKey(ownerKey, "urn:wendy:org:5:user:alice")

	type call struct {
		name     string
		authz    Authorizer
		ctx      context.Context
		method   string
		wantCode codes.Code
		wantKind PrincipalKind
	}
	mtlsOn := func(scope certs.Scope) Authorizer {
		return Authorizer{Logger: zap.NewNop(), Listener: ListenerMTLS, Keys: store, Expected: scope, OrgMode: OrgModeStrict}
	}
	localOnly := mtlsOn(certs.Scope{})
	enrolledBox := mtlsOn(enrolled)
	socket := Authorizer{Logger: zap.NewNop(), Listener: ListenerAdminSocket}
	preEnroll := Authorizer{Logger: zap.NewNop(), Listener: ListenerPreEnroll}
	bg := context.Background()

	calls := []call{
		{"local owner manages keys", localOnly, ctxWithLeaf(leafWithKey(ownerKey)), mAddKey, codes.OK, KindLocalKey},
		{"local owner unprovisions", localOnly, ctxWithLeaf(leafWithKey(ownerKey)), mUnprovision, codes.OK, KindLocalKey},
		{"local owner enrolls", localOnly, ctxWithLeaf(leafWithKey(ownerKey)), mStartACME, codes.OK, KindLocalKey},
		{"local operator opens shell", localOnly, ctxWithLeaf(leafWithKey(operatorKey)), mShell, codes.OK, KindLocalKey},
		{"local operator deploys", localOnly, ctxWithLeaf(leafWithKey(operatorKey)), mDeploy, codes.OK, KindLocalKey},
		{"local operator updates OS", localOnly, ctxWithLeaf(leafWithKey(operatorKey)), mUpdateOS, codes.OK, KindLocalKey},
		{"local operator cannot manage keys", localOnly, ctxWithLeaf(leafWithKey(operatorKey)), mListKeys, codes.PermissionDenied, 0},
		{"local operator cannot unprovision", localOnly, ctxWithLeaf(leafWithKey(operatorKey)), mUnprovision, codes.PermissionDenied, 0},
		{"local viewer reads version", localOnly, ctxWithLeaf(leafWithKey(viewerKey)), mVersion, codes.OK, KindLocalKey},
		{"local viewer reads logs", localOnly, ctxWithLeaf(leafWithKey(viewerKey)), mLogs, codes.OK, KindLocalKey},
		{"local viewer cannot deploy", localOnly, ctxWithLeaf(leafWithKey(viewerKey)), mDeploy, codes.PermissionDenied, 0},
		{"local viewer cannot open shell", localOnly, ctxWithLeaf(leafWithKey(viewerKey)), mShell, codes.PermissionDenied, 0},
		{"unknown key refused", localOnly, ctxWithLeaf(leafWithKey(unknownKey)), mVersion, codes.PermissionDenied, 0},
		{"tenant cert on local-only box refused", localOnly, ctxWithLeaf(tenantUser), mVersion, codes.PermissionDenied, 0},
		{"unmapped method denied to owner", localOnly, ctxWithLeaf(leafWithKey(ownerKey)), mUnmapped, codes.PermissionDenied, 0},
		{"no client certificate", localOnly, bg, mVersion, codes.Unauthenticated, 0},

		{"tenant user is operator", enrolledBox, ctxWithLeaf(tenantUser), mShell, codes.OK, KindTenantUser},
		{"tenant user keeps unenroll", enrolledBox, ctxWithLeaf(tenantUser), mUnprovision, codes.OK, KindTenantUser},
		{"tenant user cannot manage keys", enrolledBox, ctxWithLeaf(tenantUser), mListKeys, codes.PermissionDenied, 0},
		{"tenant cert with owner key is not local owner", enrolledBox, ctxWithLeaf(ownerKeyTenantCert), mAddKey, codes.PermissionDenied, 0},
		{"other tenant refused", enrolledBox, ctxWithLeaf(otherTenant), mVersion, codes.PermissionDenied, 0},
		// Store not dormant beside a tenant scope: on a real device only the
		// ~0.5 s between unenroll and the agent restart.
		{"local owner beside a tenant scope (store not dormant)", enrolledBox, ctxWithLeaf(leafWithKey(ownerKey)), mAddKey, codes.OK, KindLocalKey},
		{"peer device may mesh dial", enrolledBox, ctxWithLeaf(peerDevice), mMeshDial, codes.OK, KindPeerDevice},
		{"peer device may deliver a build", enrolledBox, ctxWithLeaf(peerDevice), mWriteChunks, codes.OK, KindPeerDevice},
		{"peer device has no operator rights", enrolledBox, ctxWithLeaf(peerDevice), mShell, codes.PermissionDenied, 0},
		{"peer device cannot even read", enrolledBox, ctxWithLeaf(peerDevice), mVersion, codes.PermissionDenied, 0},
		{"BLE wifi by tenant user", enrolledBox, ctxWithLeaf(tenantUser), mBLEWifi, codes.OK, KindTenantUser},
		{"BLE wifi by peer device refused", enrolledBox, ctxWithLeaf(peerDevice), mBLEWifi, codes.PermissionDenied, 0},

		{"admin socket deploys", socket, bg, mDeploy, codes.OK, KindAdminSocket},
		{"admin socket cannot unprovision", socket, bg, mUnprovision, codes.PermissionDenied, 0},
		{"admin socket cannot manage keys", socket, bg, mAddKey, codes.PermissionDenied, 0},
		{"pre-enroll keeps today's access", preEnroll, bg, mStartACME, codes.OK, KindPreEnroll},
		{"pre-enroll cannot plant a key", preEnroll, bg, mAddKey, codes.PermissionDenied, 0},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			p, err := c.authz.Authorize(c.ctx, c.method)
			if got := status.Code(err); got != c.wantCode {
				t.Fatalf("Authorize(%s) code = %v (%v), want %v", c.method, got, err, c.wantCode)
			}
			if err == nil && p.Kind != c.wantKind {
				t.Fatalf("principal kind = %s, want %s", p.Kind, c.wantKind)
			}
		})
	}
}

// A key removed while its connection stays open loses access on the next call.
func TestAuthorizerReadsKeysPerCall(t *testing.T) {
	store, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	owner, op := newKey(t), newKey(t)
	store.Add(owner, trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now())
	store.Add(op, trustedkeys.RoleOperator, "", trustedkeys.Fingerprint(owner), time.Now())
	a := Authorizer{Logger: zap.NewNop(), Listener: ListenerMTLS, Keys: store, OrgMode: OrgModeStrict}
	ctx := ctxWithLeaf(leafWithKey(op))
	if _, err := a.Authorize(ctx, mShell); err != nil {
		t.Fatalf("operator before removal: %v", err)
	}
	if _, err := store.Remove(trustedkeys.Fingerprint(op), trustedkeys.Fingerprint(owner)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authorize(ctx, mShell); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("operator after removal = %v, want PermissionDenied", err)
	}
}

// A key removed while its holder keeps a connection (live or resumed) must not
// fall through to the legacy no-tenant path that grace/off org modes admit.
func TestRemovedKeyDoesNotFallBackToLegacyAccess(t *testing.T) {
	store, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	ownerKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	viewerKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ownerSPKI, _ := x509.MarshalPKIXPublicKey(ownerKey.Public())
	viewerSPKI, _ := x509.MarshalPKIXPublicKey(viewerKey.Public())
	store.Add(ownerSPKI, trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now())
	store.Add(viewerSPKI, trustedkeys.RoleViewer, "", trustedkeys.Fingerprint(ownerSPKI), time.Now())

	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, viewerKey.Public(), viewerKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	ctx := ctxWithLeaf(leaf)
	if _, err := store.Remove(trustedkeys.Fingerprint(viewerSPKI), trustedkeys.Fingerprint(ownerSPKI)); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []OrgMode{OrgModeGrace, OrgModeOff} {
		a := Authorizer{Logger: zap.NewNop(), Listener: ListenerMTLS, Keys: store, Expected: certs.Scope{OrgID: 5}, OrgMode: mode}
		if _, err := a.Authorize(ctx, mShell); status.Code(err) != codes.PermissionDenied {
			t.Errorf("mode %s: removed key = %v, want PermissionDenied", mode, err)
		}
	}
}

func TestSignerCertificatesHaveNoAccess(t *testing.T) {
	store, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	signer := leafWithKey(newKey(t), "spiffe://wendy.sh/tenant/11111111-1111-4111-8111-111111111111/signer/ci")
	a := Authorizer{Logger: zap.NewNop(), Listener: ListenerMTLS, Keys: store, OrgMode: OrgModeOff}
	if _, err := a.Authorize(ctxWithLeaf(signer), mVersion); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("signer certificate = %v, want PermissionDenied", err)
	}
}

// Cloud-enrolled (store dormant): no caller is ever a local key, and every
// trusted-keys RPC is refused for every caller and audited. Unenrolling
// restores the local path.
func TestCloudEnrolledDisablesLocalKeys(t *testing.T) {
	store, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	owner := newKey(t)
	store.Add(owner, trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now())
	store.SetDormant(true)
	core, logs := observer.New(zapcore.InfoLevel)
	a := Authorizer{Logger: zap.New(core), Listener: ListenerMTLS, Keys: store, Expected: certs.Scope{OrgID: 5}, OrgMode: OrgModeStrict}
	socket := Authorizer{Logger: zap.New(core), Listener: ListenerAdminSocket}
	ownerCtx := ctxWithLeaf(leafWithKey(owner))
	tenantCtx := ctxWithLeaf(leafWithKey(newKey(t), "urn:wendy:org:5:user:alice"))

	for _, m := range []string{mVersion, mShell, mAddKey} {
		if _, err := a.Authorize(ownerCtx, m); status.Code(err) != codes.PermissionDenied {
			t.Errorf("owner key while enrolled, %s = %v, want PermissionDenied", m, err)
		}
	}
	for _, m := range []string{mAddKey, mListKeys, mRemoveKey} {
		_, err := a.Authorize(tenantCtx, m)
		if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != trustedkeys.ErrDormant.Error() {
			t.Errorf("tenant user %s while enrolled = %v, want FailedPrecondition %q", m, err, trustedkeys.ErrDormant)
		}
		// The admin socket has no keys store in production (and does not
		// serve the keys service): still refused.
		if _, err := socket.Authorize(context.Background(), m); status.Code(err) != codes.PermissionDenied {
			t.Errorf("admin socket %s while enrolled = %v, want PermissionDenied", m, err)
		}
	}
	if n := logs.FilterField(zap.String("reason", "cloud-enrolled")).FilterField(zap.String("audit", "trusted_keys")).Len(); n != 3 {
		t.Errorf("audit rows = %d, want 3", n)
	}
	if p, err := a.Authorize(tenantCtx, mShell); err != nil || p.Kind != KindTenantUser {
		t.Fatalf("tenant user while enrolled = %v %v, want tenant-user", p.Kind, err)
	}

	store.SetDormant(false)
	if p, err := a.Authorize(ownerCtx, mAddKey); err != nil || p.Kind != KindLocalKey {
		t.Fatalf("owner key after unenroll = %v %v, want local-key", p.Kind, err)
	}
	if _, err := a.Authorize(tenantCtx, mListKeys); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("tenant user keys RPC after unenroll = %v, want PermissionDenied", err)
	}
}

// A certificate whose identity cannot be read is refused in every org mode:
// it is never "no identity", which off/grace admit as a legacy certificate.
func TestMalformedIdentityIsRefusedInEveryMode(t *testing.T) {
	store, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	for _, uri := range []string{
		"spiffe://wendy.sh/tenant/11111111-1111-4111-8111-111111111111/workload/x",
		"urn:wendy:org:5:robot:1",
	} {
		leaf := leafWithKey(newKey(t), uri)
		if _, _, err := certs.IdentityFromCert(leaf); err == nil {
			t.Fatalf("%s: IdentityFromCert accepted it; the test needs an unreadable identity", uri)
		}
		for _, mode := range []OrgMode{OrgModeOff, OrgModeGrace, OrgModeStrict} {
			a := Authorizer{Logger: zap.NewNop(), Listener: ListenerMTLS, Keys: store, Expected: certs.Scope{OrgID: 5}, OrgMode: mode}
			if p, err := a.Authorize(ctxWithLeaf(leaf), mShell); status.Code(err) != codes.PermissionDenied {
				t.Errorf("%s under %s = %v %v, want PermissionDenied", uri, mode, p.Kind, err)
			}
		}
	}
}

// A trusted key in a certificate that is not self-signed (e.g. CA-issued with
// no tenant, as BLE's chain-only handshake admits) never gets the key's role:
// the same rule the mTLS handshake applies.
func TestTrustedKeyNeedsSelfSignedCertificate(t *testing.T) {
	store, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	owner := newKey(t)
	store.Add(owner, trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now())
	notSelfSigned := buildLeaf(leafOptions{commonName: "operator"})
	notSelfSigned.RawSubjectPublicKeyInfo = owner
	for _, mode := range []OrgMode{OrgModeOff, OrgModeGrace, OrgModeStrict} {
		a := Authorizer{Logger: zap.NewNop(), Listener: ListenerMTLS, Keys: store, Expected: certs.Scope{OrgID: 5}, OrgMode: mode}
		if p, err := a.Authorize(ctxWithLeaf(notSelfSigned), mAddKey); status.Code(err) != codes.PermissionDenied {
			t.Errorf("mode %s: not self-signed = %v %v, want PermissionDenied", mode, p.Kind, err)
		}
		if p, err := a.Authorize(ctxWithLeaf(leafWithKey(owner)), mAddKey); err != nil || p.Kind != KindLocalKey {
			t.Errorf("mode %s: self-signed = %v %v, want local-key", mode, p.Kind, err)
		}
	}
}
