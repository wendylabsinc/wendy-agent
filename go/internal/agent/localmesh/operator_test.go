package localmesh

import (
	"crypto/mldsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/netip"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func TestOperatorMLDSAManifestAndAdmission(t *testing.T) {
	principal := "spiffe://wendy.sh/tenant/" + testTenantUUID + "/operator/auth0|alice"
	key, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	uri, _ := url.Parse(principal)
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1234), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	pki := testPKI()
	raw, err := x509.CreateCertificate(rand.Reader, template, pki.certificate, key.Public(), pki.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	operator, err := NewOperatorCredentials(leafPEM, pki.chainPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewCredentials(leafPEM, pki.chainPEM, keyPEM); err == nil {
		t.Fatal("enrolled as device")
	}
	_, device := testCredentials(t)
	server, err := lanServerTLS(device)
	if err != nil {
		t.Fatal(err)
	}
	client, err := operator.PeerTLS(device.Device)
	if err != nil {
		t.Fatal(err)
	}
	if result := rootQUICRoundtrip(client, server); result.err != nil {
		t.Fatal(result.err)
	}
	operatorServer, err := lanServerTLS(operator)
	if err != nil {
		t.Fatal(err)
	}
	deviceClient, err := device.PeerTLS(operator.Device)
	if err != nil {
		t.Fatal(err)
	}
	if result := rootQUICRoundtrip(deviceClient, operatorServer); result.err != nil {
		t.Fatal(result.err)
	}
	leaf, _ := x509.ParseCertificate(raw)
	ca := pki.certificate
	if err := server.VerifyConnection(tls.ConnectionState{DidResume: true, PeerCertificates: []*x509.Certificate{leaf, ca}}); err != nil {
		t.Fatal(err)
	}
	cache, err := OpenIdentityCache(filepath.Join(t.TempDir(), "cache.json"), DefaultCacheLimits(), device.Verify, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cache.Put(operator.Certificate.Certificate, now); err != nil {
		t.Fatal(err)
	}
	directory, err := NewDirectory(device.Device.Tenant(), 16, cache)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Principal: operator.Device.Principal(), Version: ManifestVersionRoute, Device: operator.Device.UUID(), Revision: 1, Issued: now.UnixMilli(), Expires: now.Add(time.Minute).UnixMilli(), Route: &RouteOrigin{Prefix: netip.PrefixFrom(operator.Device.MeshAddress(), 128), RouterID: 123, Seqno: 1}}
	signed, err := SignManifest(m, operator.Certificate.Certificate, operator.Signer, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = directory.Accept(signed, now); err != nil {
		t.Fatal(err)
	}
	if !directory.RouteAuthorizations(now)[*m.Route] {
		t.Fatal("operator return route missing")
	}
	m.AgentPort = 50052
	if _, err = SignManifest(m, operator.Certificate.Certificate, operator.Signer, now); err == nil {
		t.Fatal("operator advertised agent")
	}
	m.AgentPort = 0
	m.Route.Prefix = netip.PrefixFrom(device.Device.MeshAddress(), 128)
	if _, err = SignManifest(m, operator.Certificate.Certificate, operator.Signer, now); err == nil {
		t.Fatal("operator claimed device route")
	}
}
