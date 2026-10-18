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
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	_ "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
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
	return der
}

func leafWithKey(spki []byte, uris ...string) *x509.Certificate {
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
		{"local owner on enrolled box", enrolledBox, ctxWithLeaf(leafWithKey(ownerKey)), mAddKey, codes.OK, KindLocalKey},
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
	if _, err := store.Remove(trustedkeys.Fingerprint(op)); err != nil {
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
	if _, err := store.Remove(trustedkeys.Fingerprint(viewerSPKI)); err != nil {
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
