package mtls

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type selfIssuedOpts struct {
	notBefore, notAfter time.Time
	uris                []string
	noClientAuth        bool
}

// selfIssued is what the CLI presents as a local operator: its own key in a
// certificate it signed itself.
func selfIssued(t *testing.T, key crypto.Signer, o selfIssuedOpts) tls.Certificate {
	t.Helper()
	if o.notBefore.IsZero() {
		o.notBefore = time.Now().Add(-time.Minute)
	}
	if o.notAfter.IsZero() {
		o.notAfter = time.Now().Add(time.Hour)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "wendy-operator"},
		NotBefore:    o.notBefore,
		NotAfter:     o.notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if !o.noClientAuth {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	for _, u := range o.uris {
		parsed, _ := url.Parse(u)
		tmpl.URIs = append(tmpl.URIs, parsed)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func spkiOf(t *testing.T, key crypto.Signer) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// localOnlyServer is the TLS config of a device that is not enrolled: its
// self-signed certificate and the trusted keys, no CA chain.
func localOnlyServer(t *testing.T, keys *trustedkeys.Store, registry bool) *tls.Config {
	t.Helper()
	keyPEM, err := certs.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := SelfSignedCert(keyPEM, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	newCfg := NewServerTLSConfig
	if registry {
		newCfg = NewRegistryTLSConfig
	}
	cfg, err := newCfg(certPEM, "", keyPEM, nil, time.Time{}, keys)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func handshake(t *testing.T, server *tls.Config, client tls.Certificate) error {
	t.Helper()
	// TCP, not net.Pipe: in TLS 1.3 the client finishes before the server
	// judges its certificate, and the server's alert needs a buffered write.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	clientCfg := &tls.Config{
		Certificates: []tls.Certificate{client},
		// The CLI authenticates the device by its SPKI pin, not a chain.
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- tls.Server(s, server).HandshakeContext(ctx)
	}()
	clientErr := tls.Client(c, clientCfg).HandshakeContext(ctx)
	if err := <-serverErr; err != nil {
		return err
	}
	return clientErr
}

func TestLocalOperatorHandshake(t *testing.T) {
	keys, err := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mlKey, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	viewerKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	unknownKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	keys.Add(spkiOf(t, ecKey), trustedkeys.RoleOwner, "ecdsa", trustedkeys.AddedByInstall, now)
	keys.Add(spkiOf(t, mlKey), trustedkeys.RoleOperator, "ml-dsa", trustedkeys.AddedByInstall, now)
	keys.Add(spkiOf(t, viewerKey), trustedkeys.RoleViewer, "viewer", trustedkeys.AddedByInstall, now)

	server := localOnlyServer(t, keys, false)
	registry := localOnlyServer(t, keys, true)
	cases := []struct {
		name   string
		server *tls.Config
		client tls.Certificate
		ok     bool
	}{
		{"trusted ECDSA key", server, selfIssued(t, ecKey, selfIssuedOpts{}), true},
		{"trusted ML-DSA-65 key", server, selfIssued(t, mlKey, selfIssuedOpts{}), true},
		{"trusted viewer key", server, selfIssued(t, viewerKey, selfIssuedOpts{}), true},
		{"unknown key", server, selfIssued(t, unknownKey, selfIssuedOpts{}), false},
		{"trusted key claiming a tenant", server, selfIssued(t, ecKey, selfIssuedOpts{uris: []string{"urn:wendy:org:5:user:alice"}}), false},
		{"trusted key without clientAuth", server, selfIssued(t, ecKey, selfIssuedOpts{noClientAuth: true}), false},
		{"trusted key, expired", server, selfIssued(t, ecKey, selfIssuedOpts{notBefore: now.Add(-2 * time.Hour), notAfter: now.Add(-time.Hour)}), false},
		{"trusted key, not yet valid", server, selfIssued(t, ecKey, selfIssuedOpts{notBefore: now.Add(48 * time.Hour), notAfter: now.Add(72 * time.Hour)}), false},
		{"trusted key, lifetime over cap", server, selfIssued(t, ecKey, selfIssuedOpts{notAfter: now.Add(3 * 365 * 24 * time.Hour)}), false},
		{"registry admits operator", registry, selfIssued(t, mlKey, selfIssuedOpts{}), true},
		{"registry refuses viewer", registry, selfIssued(t, viewerKey, selfIssuedOpts{}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := handshake(t, c.server, c.client)
			if (err == nil) != c.ok {
				t.Fatalf("handshake error = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

// The device clock may sit far behind (no RTC, offline). The latest added_at
// of the trusted keys floors it, like the enrollment certificate does on an
// enrolled device.
func TestLocalOperatorNotBeforeFloor(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	issued := time.Now()
	leaf, _ := x509.ParseCertificate(selfIssued(t, key, selfIssuedOpts{notBefore: issued, notAfter: issued.Add(time.Hour)}).Certificate[0])
	clockBehind := issued.Add(-365 * 24 * time.Hour)
	if err := verifyLocalOperatorCert(leaf, time.Time{}, clockBehind); err == nil {
		t.Fatal("accepted a not-yet-valid certificate without a floor")
	}
	if err := verifyLocalOperatorCert(leaf, issued.Add(-time.Minute), clockBehind); err != nil {
		t.Fatalf("floored check refused: %v", err)
	}
}

func TestSelfSignedCertUsesDeviceKey(t *testing.T) {
	keyPEM, _ := certs.GenerateKeyPair()
	certPEM, err := SelfSignedCert(keyPEM, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		t.Fatalf("self-signed cert does not match the device key: %v", err)
	}
	block, _ := pem.Decode([]byte(certPEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if checkRevocation(leaf) != nil {
		t.Fatalf("self-signed lifetime %v exceeds the cap", leaf.NotAfter.Sub(leaf.NotBefore))
	}
}

// On an enrolled device the chain path keeps working beside local keys, and
// the direct-PKI tenant gate lets a local operator certificate through while
// still refusing everything else that names no tenant or a foreign one.
// The store is not dormant here: this is the chain config with local keys
// active, which a real device only runs between unenroll and its restart.
func TestEnrolledDeviceKeepsChainPathBesideLocalKeys(t *testing.T) {
	keys, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	localKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	keys.Add(spkiOf(t, localKey), trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now())

	ca, caKey, caPEM := testCAKeyPair(t)
	devicePEM, deviceKeyPEM := testLeafCertificate(t, "device")
	cfg, err := NewServerTLSConfig(devicePEM, caPEM, deviceKeyPEM, nil, time.Time{}, keys)
	if err != nil {
		t.Fatal(err)
	}
	tenantRaw := testPeerLeafRaw(t, ca, caKey, "urn:wendy:org:7:user:alice")
	if err := cfg.VerifyPeerCertificate([][]byte{tenantRaw}, nil); err != nil {
		t.Fatalf("chain certificate refused: %v", err)
	}
	if err := handshake(t, cfg, selfIssued(t, localKey, selfIssuedOpts{})); err != nil {
		t.Fatalf("local operator refused on an enrolled device: %v", err)
	}
	strangerKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err := handshake(t, cfg, selfIssued(t, strangerKey, selfIssuedOpts{})); err == nil {
		t.Fatal("self-issued certificate with an untrusted key accepted")
	}

	// Direct-PKI device: VerifyConnection enforces the tenant prefix.
	const tenant = "11111111-1111-4111-8111-111111111111"
	block, _ := pem.Decode([]byte(devicePEM))
	leaf, _ := x509.ParseCertificate(block.Bytes)
	kb, _ := pem.Decode([]byte(deviceKeyPEM))
	dk, _ := x509.ParseECPrivateKey(kb.Bytes)
	principal, _ := url.Parse("spiffe://wendy.sh/tenant/" + tenant + "/device/sim")
	leaf.URIs = []*url.URL{principal}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &dk.PublicKey, dk)
	if err != nil {
		t.Fatal(err)
	}
	pkiDevicePEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	pkiCfg, err := NewServerTLSConfig(pkiDevicePEM, caPEM, deviceKeyPEM, nil, time.Time{}, keys)
	if err != nil {
		t.Fatal(err)
	}
	localLeaf, _ := x509.ParseCertificate(selfIssued(t, localKey, selfIssuedOpts{}).Certificate[0])
	strangerLeaf, _ := x509.ParseCertificate(selfIssued(t, strangerKey, selfIssuedOpts{}).Certificate[0])
	foreign, _ := url.Parse("spiffe://wendy.sh/tenant/22222222-2222-4222-8222-222222222222/operator/alice")
	foreignWithLocalKey := *localLeaf
	foreignWithLocalKey.URIs = []*url.URL{foreign}
	for _, tc := range []struct {
		name    string
		leaf    *x509.Certificate
		wantErr bool
	}{
		{"local operator", localLeaf, false},
		{"untrusted key, no tenant", strangerLeaf, true},
		{"trusted key, foreign tenant", &foreignWithLocalKey, true},
	} {
		if err := pkiCfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{tc.leaf}}); (err != nil) != tc.wantErr {
			t.Errorf("%s: VerifyConnection error = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

// Resumed sessions skip VerifyPeerCertificate but run VerifyConnection: a key
// removed after the first handshake must not resume (the registry has no
// per-request check), and a trusted key must sign its own certificate.
func TestLocalOperatorRecheckedOnResumeAndMustBeSelfSigned(t *testing.T) {
	keys, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	owner, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	op, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	keys.Add(spkiOf(t, owner), trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now())
	keys.Add(spkiOf(t, op), trustedkeys.RoleOperator, "", trustedkeys.AddedByInstall, time.Now())
	registry := localOnlyServer(t, keys, true)

	opLeaf, _ := x509.ParseCertificate(selfIssued(t, op, selfIssuedOpts{}).Certificate[0])
	resumed := tls.ConnectionState{DidResume: true, PeerCertificates: []*x509.Certificate{opLeaf}}
	if err := registry.VerifyConnection(resumed); err != nil {
		t.Fatalf("resume with a trusted operator key: %v", err)
	}
	if _, err := keys.Remove(trustedkeys.Fingerprint(spkiOf(t, op)), trustedkeys.Fingerprint(spkiOf(t, owner))); err != nil {
		t.Fatal(err)
	}
	if err := registry.VerifyConnection(resumed); err == nil {
		t.Fatal("removed key resumed a registry session")
	}

	// The owner's key in a certificate signed by some other key.
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "x"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, owner.Public(), other)
	if err != nil {
		t.Fatal(err)
	}
	server := localOnlyServer(t, keys, false)
	if err := server.VerifyPeerCertificate([][]byte{der}, nil); err == nil {
		t.Fatal("accepted a trusted key in a certificate it did not sign")
	}
}

// Cloud-enrolled (store dormant): a trusted local key is refused at the
// handshake like any unknown certificate, logged once with its fingerprint;
// tenant certificates are unaffected. Unenrolling admits the key again.
func TestCloudEnrolledRefusesLocalOperators(t *testing.T) {
	keys, _ := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	localKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	keys.Add(spkiOf(t, localKey), trustedkeys.RoleOwner, "", trustedkeys.AddedByInstall, time.Now())
	keys.SetDormant(true)

	core, logs := observer.New(zapcore.InfoLevel)
	ca, caKey, caPEM := testCAKeyPair(t)
	devicePEM, deviceKeyPEM := testLeafCertificate(t, "device")
	cfg, err := NewServerTLSConfig(devicePEM, caPEM, deviceKeyPEM, zap.New(core), time.Time{}, keys)
	if err != nil {
		t.Fatal(err)
	}
	local := selfIssued(t, localKey, selfIssuedOpts{})
	if err := handshake(t, cfg, local); err == nil {
		t.Fatal("trusted local key accepted while cloud-enrolled")
	}
	refused := logs.FilterMessage("local operator certificate refused: cloud-enrolled").
		FilterField(zap.String("fingerprint", trustedkeys.Fingerprint(spkiOf(t, localKey))))
	if refused.Len() != 1 || logs.Len() != 1 {
		t.Fatalf("log entries = %d (refusals with fingerprint %d), want exactly one", logs.Len(), refused.Len())
	}
	localLeaf, _ := x509.ParseCertificate(local.Certificate[0])
	if err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{localLeaf}}); err == nil {
		t.Fatal("trusted local key resumed while cloud-enrolled")
	}
	if err := cfg.VerifyPeerCertificate([][]byte{testPeerLeafRaw(t, ca, caKey, "urn:wendy:org:7:user:alice")}, nil); err != nil {
		t.Fatalf("tenant certificate refused while cloud-enrolled: %v", err)
	}
	// The local-only server keeps running briefly after enrollment, until
	// the agent restarts: it must refuse too.
	if err := handshake(t, localOnlyServer(t, keys, false), local); err == nil {
		t.Fatal("local-only server accepted a local key while cloud-enrolled")
	}

	keys.SetDormant(false)
	if err := handshake(t, cfg, local); err != nil {
		t.Fatalf("local key refused after unenroll: %v", err)
	}
}
