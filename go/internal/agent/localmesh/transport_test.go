package localmesh

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"sync"
	"testing"
	"time"

	quic "github.com/quic-go/quic-go"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// Test identities are real device principals: a tenant UUID and a device UUID,
// the same two values the mesh address and link ordering are derived from.
// Device A is the lower UUID, device B the higher.
const (
	testTenantUUID  = "0b8f3a67-2c1d-4e5f-9a8b-0c1d2e3f4051"
	testOtherTenant = "1c9a4b78-3d2e-4f60-9b8c-1d2e3f405162"
	testDeviceAID   = "2b1c0000-0000-4000-8000-000000000445"
	testDeviceBID   = "2b1c0000-0000-4000-8000-000000000460"
	testDeviceCID   = "2b1c0000-0000-4000-8000-000000000470"
)

func testPrincipal(tenant, device string) string {
	return certs.DeviceSPIFFEURI(tenant, device)
}

func testDevice(t *testing.T, principal string) meshid.Device {
	t.Helper()
	device, err := meshid.DeviceFromPrincipal(principal)
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func testDevices(t *testing.T) (meshid.Device, meshid.Device) {
	t.Helper()
	return testDevice(t, testPrincipal(testTenantUUID, testDeviceAID)),
		testDevice(t, testPrincipal(testTenantUUID, testDeviceBID))
}

func testTenant(t *testing.T) meshid.Tenant {
	t.Helper()
	tenant, err := meshid.ParseTenant(testTenantUUID)
	if err != nil {
		t.Fatal(err)
	}
	return tenant
}

// testPKI is one shared CA so a cross-tenant certificate in a test is rejected
// for its principal, never because it happened to be signed by another key.
type testCA struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	chainPEM    string
}

var testPKI = sync.OnceValue(func() testCA {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local-mesh test CA"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	if err != nil {
		panic(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return testCA{ca, caKey, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
})

// testIssue mints a leaf for one principal under the shared test CA.
func testIssue(t *testing.T, principal string) (certPEM, keyPEM string) {
	t.Helper()
	pki := testPKI()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(principal)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, pki.certificate, &key.PublicKey, pki.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func testCredentials(t *testing.T) (*Credentials, *Credentials) {
	return testCredentialsWithPEM(t, nil)
}

func testCredentialsWithPEM(t *testing.T, capture func(meshid.Device, string, string, string)) (*Credentials, *Credentials) {
	t.Helper()
	chainPEM := testPKI().chainPEM
	issue := func(principal string) *Credentials {
		certPEM, keyPEM := testIssue(t, principal)
		if capture != nil {
			capture(testDevice(t, principal), certPEM, chainPEM, keyPEM)
		}
		credentials, err := NewCredentials(certPEM, chainPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return credentials
	}
	return issue(testPrincipal(testTenantUUID, testDeviceAID)), issue(testPrincipal(testTenantUUID, testDeviceBID))
}

// testCredentialsFor builds credentials for an arbitrary principal under the
// same CA - a different device, or the same device UUID in another tenant.
func testCredentialsFor(t *testing.T, principal string) *Credentials {
	t.Helper()
	chainPEM := testPKI().chainPEM
	certPEM, keyPEM := testIssue(t, principal)
	credentials, err := NewCredentials(certPEM, chainPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

// testForeignCredentials mints credentials whose chain is signed by a CA this
// mesh does not trust, so a rejected handshake can only be about trust.
func testForeignCredentials(t *testing.T, principal string) *Credentials {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "untrusted local-mesh CA"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	chainPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(principal)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	leaf, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := NewCredentials(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})), chainPEM, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})))
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func TestLocalMeshQUICHandshakeAndControl(t *testing.T) {
	a, b := testCredentials(t)
	serverTLS, err := b.PeerTLS(a.Device)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := quic.ListenAddr("127.0.0.1:0", serverTLS, QUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept(ctx)
		if err != nil {
			result <- err
			return
		}
		defer conn.CloseWithError(0, "done")
		stream, err := OpenControl(ctx, conn, b.Device, a.Device)
		if err != nil {
			result <- err
			return
		}
		m, err := ReadControl(stream)
		if err == nil && m.Kind != "bundle" {
			err = fmt.Errorf("unexpected message %s", m.Kind)
		}
		if err == nil {
			_, err = b.Verify(m.Bundle, time.Now())
		}
		result <- err
	}()
	clientTLS, err := a.PeerTLS(b.Device)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := quic.DialAddr(ctx, listener.Addr().String(), clientTLS, QUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "done")
	stream, err := OpenControl(ctx, conn, a.Device, b.Device)
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteControl(stream, ControlMessage{Kind: "bundle", Bundle: a.Certificate.Certificate}); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}

// TestCredentialsPinPeerPrincipal is the anti-spoofing contract: a dial pinned
// to one device principal accepts that device and nothing else, and the same
// device UUID under a different tenant is a different device.
func TestCredentialsPinPeerPrincipal(t *testing.T) {
	a, b := testCredentials(t)
	sameUUIDOtherTenant := testCredentialsFor(t, testPrincipal(testOtherTenant, testDeviceBID))
	pinned, err := a.PeerTLS(b.Device)
	if err != nil {
		t.Fatal(err)
	}
	if err = pinned.VerifyPeerCertificate(b.Certificate.Certificate, nil); err != nil {
		t.Fatalf("pinned peer rejected: %v", err)
	}
	if err = pinned.VerifyPeerCertificate(sameUUIDOtherTenant.Certificate.Certificate, nil); err == nil {
		t.Fatal("same device UUID in another tenant satisfied the pin")
	}
	if err = pinned.VerifyPeerCertificate(a.Certificate.Certificate, nil); err == nil {
		t.Fatal("own certificate satisfied the peer pin")
	}
	verified, err := a.Verify(b.Certificate.Certificate, time.Now())
	if err != nil || verified.Device.Principal() != b.Device.Principal() {
		t.Fatal(verified, err)
	}
	if _, err = a.Verify(sameUUIDOtherTenant.Certificate.Certificate, time.Now()); err == nil {
		t.Fatal("peer outside the tenant accepted")
	}
	if _, err = a.Verify(b.Certificate.Certificate, time.Now().Add(2*time.Hour)); err == nil {
		t.Fatal("expired identity accepted")
	}
}

// TestCredentialsRejectNonDeviceIdentity is the no-fallback contract: an
// identity that is not a device principal - a legacy numeric URN, a user, or a
// device name that is not a UUID - never becomes a mesh identity.
func TestCredentialsRejectNonDeviceIdentity(t *testing.T) {
	chainPEM := testPKI().chainPEM
	for _, principal := range []string{
		"urn:wendy:org:64:asset:445",
		testPrincipal(testTenantUUID, "service/user-8f2c1d34-5e6f-4a7b-9c8d-1e2f30415263"),
		testPrincipal(testTenantUUID, "rack-7/orin"),
		testPrincipal(testTenantUUID, "2B1C0000-0000-4000-8000-000000000445"),
	} {
		certPEM, keyPEM := testIssue(t, principal)
		if _, err := NewCredentials(certPEM, chainPEM, keyPEM); err == nil {
			t.Fatalf("principal %s accepted as a mesh identity", principal)
		}
	}
}

func TestCredentialsRejectPeerOutsideTenant(t *testing.T) {
	a, _ := testCredentials(t)
	other := testDevice(t, testPrincipal(testOtherTenant, testDeviceAID))
	if _, err := a.PeerTLS(other); err == nil {
		t.Fatal("peer outside the tenant got a TLS config")
	}
	if _, err := a.PeerTLS(a.Device); err == nil {
		t.Fatal("self link accepted")
	}
}
