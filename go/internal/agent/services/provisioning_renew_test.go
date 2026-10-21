package services

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
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/acmeenroll"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const renewTestPrincipal = "spiffe://wendy.sh/tenant/11111111-1111-4111-8111-111111111111/device/sim"

type renewCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newRenewCA(t *testing.T) renewCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "device-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return renewCA{cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

// issue mints a device leaf for pub, valid from notBefore to notAfter.
func (ca renewCA) issue(t *testing.T, pub crypto.PublicKey, notBefore, notAfter time.Time) string {
	t.Helper()
	principal, _ := url.Parse(renewTestPrincipal)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "sim"},
		NotBefore: notBefore, NotAfter: notAfter, URIs: []*url.URL{principal},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// enrolledForRenewal persists a pki-core enrollment whose P-256 leaf has a
// tenth of its lifetime left, and points renewal at endpoint.
func enrolledForRenewal(t *testing.T, ca renewCA, endpoint string, roots *x509.CertPool) (*ProvisioningService, string, string) {
	t.Helper()
	dir := t.TempDir()
	keyPEM, err := certs.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := certs.ParseSigningPrivateKeyPEM([]byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	leaf := ca.issue(t, signer.Public(), now.Add(-81*24*time.Hour), now.Add(9*24*time.Hour))
	state, _ := json.Marshal(provisioningState{
		Enrolled: true, CloudHost: "api.dev.wendy.sh:443", CertPEM: leaf, ChainPEM: ca.pem,
		PrincipalURI: renewTestPrincipal, ACMEDirectoryURL: "https://acme.pki.test/t/acme/directory",
	})
	if err := WriteDeviceKey(dir, []byte(keyPEM)); err != nil {
		t.Fatal(err)
	}
	if err := WriteProvisioningState(dir, state); err != nil {
		t.Fatal(err)
	}
	t.Setenv(renewEndpointEnv, endpoint)
	old := renewRootCAs
	renewRootCAs = roots
	t.Cleanup(func() { renewRootCAs = old })
	return NewProvisioningService(zap.NewNop(), dir), dir, leaf
}

// fakeRenew is pki-core's renew frontend: it records the presented leaf and
// the CSR, and answers with respond.
type fakeRenew struct {
	requests  atomic.Int32
	presented []*x509.Certificate
	csr       *x509.CertificateRequest
}

func startFakeRenew(t *testing.T, respond func(w http.ResponseWriter, csr *x509.CertificateRequest)) (*fakeRenew, string, *x509.CertPool) {
	t.Helper()
	f := &fakeRenew{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.presented = r.TLS.PeerCertificates
		var body struct{ CSR string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("renew body: %v", err)
		}
		block, _ := pem.Decode([]byte(body.CSR))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil {
			t.Errorf("renew CSR: %v", err)
			return
		}
		f.csr = csr
		respond(w, csr)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	return f, srv.URL + "/v1/renew", roots
}

func renewed(t *testing.T, ca renewCA) func(http.ResponseWriter, *x509.CertificateRequest) {
	return func(w http.ResponseWriter, csr *x509.CertificateRequest) {
		leaf := ca.issue(t, csr.PublicKey, time.Now().Add(-time.Minute), time.Now().Add(90*24*time.Hour))
		renewJSON(t, w, http.StatusOK, map[string]string{"certificate": leaf + ca.pem})
	}
}

func renewJSON(t *testing.T, w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func TestRenewalSwitchesP256DeviceToMLDSA65(t *testing.T) {
	ca := newRenewCA(t)
	f, endpoint, roots := startFakeRenew(t, renewed(t, ca))
	svc, dir, oldLeaf := enrolledForRenewal(t, ca, endpoint, roots)

	ok, err := svc.RenewIfDue(context.Background(), time.Now())
	if err != nil || !ok {
		t.Fatalf("RenewIfDue = %v, %v; want renewed", ok, err)
	}
	if f.requests.Load() != 1 {
		t.Fatalf("renew requests = %d, want 1", f.requests.Load())
	}
	if old, _ := storedDeviceLeaf(oldLeaf); len(f.presented) == 0 || !f.presented[0].Equal(old) {
		t.Fatal("renewal did not present the current leaf over mTLS")
	}
	if pub, ok := f.csr.PublicKey.(*mldsa.PublicKey); !ok || pub.Parameters() != mldsa.MLDSA65() {
		t.Fatalf("renewal CSR key = %T, want ML-DSA-65", f.csr.PublicKey)
	}

	// Algorithm in use, after a restart: the stored identity is ML-DSA-65.
	reloaded := NewProvisioningService(zap.NewNop(), dir)
	certPEM, _, keyPEM := reloaded.ProvisioningCerts()
	pair, err := certs.TLSKeyPair(certPEM, "", string(keyPEM))
	if err != nil {
		t.Fatalf("renewed identity does not load: %v", err)
	}
	if key, ok := pair.PrivateKey.(*mldsa.PrivateKey); !ok || key.PublicKey().Parameters() != mldsa.MLDSA65() {
		t.Fatalf("device key in use = %T, want ML-DSA-65", pair.PrivateKey)
	}
	if _, err := os.Stat(filepath.Join(dir, pendingDeviceKeyFile)); !os.IsNotExist(err) {
		t.Fatal("pending renewal key left behind")
	}
	if ok, err := reloaded.RenewIfDue(context.Background(), time.Now()); ok || err != nil {
		t.Fatalf("fresh certificate renewed again: %v, %v", ok, err)
	}
}

// Steady state: an ML-DSA-65 device renews again over mTLS with its ML-DSA-65
// leaf and stays ML-DSA-65.
func TestRenewalMLDSA65DeviceRenewsAgain(t *testing.T) {
	ca := newRenewCA(t)
	f, endpoint, roots := startFakeRenew(t, renewed(t, ca))
	svc, dir, _ := enrolledForRenewal(t, ca, endpoint, roots)
	if ok, err := svc.RenewIfDue(context.Background(), time.Now()); !ok || err != nil {
		t.Fatalf("first RenewIfDue = %v, %v", ok, err)
	}
	first := NewProvisioningService(zap.NewNop(), dir)
	firstCert, _, firstKey := first.ProvisioningCerts()

	// Two thirds into the 90-day leaf: due again.
	if ok, err := first.RenewIfDue(context.Background(), time.Now().Add(65*24*time.Hour)); !ok || err != nil {
		t.Fatalf("second RenewIfDue = %v, %v", ok, err)
	}
	if f.requests.Load() != 2 {
		t.Fatalf("renew requests = %d, want 2", f.requests.Load())
	}
	if leaf, _ := storedDeviceLeaf(firstCert); len(f.presented) == 0 || !f.presented[0].Equal(leaf) {
		t.Fatal("second renewal did not present the ML-DSA-65 leaf over mTLS")
	}
	if pub, ok := f.csr.PublicKey.(*mldsa.PublicKey); !ok || pub.Parameters() != mldsa.MLDSA65() {
		t.Fatalf("renewal CSR key = %T, want ML-DSA-65", f.csr.PublicKey)
	}

	certPEM, _, keyPEM := NewProvisioningService(zap.NewNop(), dir).ProvisioningCerts()
	if certPEM == firstCert || string(keyPEM) == string(firstKey) {
		t.Fatal("reloaded identity is still the first renewal's")
	}
	leaf, err := storedDeviceLeaf(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	if pub, ok := leaf.PublicKey.(*mldsa.PublicKey); !ok || pub.Parameters() != mldsa.MLDSA65() {
		t.Fatalf("renewed leaf key = %T, want ML-DSA-65", leaf.PublicKey)
	}
	if _, err := certs.TLSKeyPair(certPEM, "", string(keyPEM)); err != nil || !isMLDSA65Key(keyPEM) {
		t.Fatalf("reloaded identity is not a matching ML-DSA-65 pair: %v", err)
	}
}

// A refusal (here: tier policy not permitting ML-DSA) is an explicit error,
// sent once, and leaves the current identity untouched: never a P-256 retry.
func TestRenewalRefusalIsExplicitAndNotRetriedWithP256(t *testing.T) {
	ca := newRenewCA(t)
	f, endpoint, roots := startFakeRenew(t, func(w http.ResponseWriter, _ *x509.CertificateRequest) {
		http.Error(w, `ca: leaf algorithm not permitted: "ML_DSA_65" not in resolved allow-list [ECDSA_P256]`, http.StatusBadRequest)
	})
	svc, dir, oldLeaf := enrolledForRenewal(t, ca, endpoint, roots)
	_, _, oldKey := svc.ProvisioningCerts()

	ok, err := svc.RenewIfDue(context.Background(), time.Now())
	if ok || err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "ML_DSA_65") {
		t.Fatalf("RenewIfDue = %v, %v; want the refusal as an error", ok, err)
	}
	if f.requests.Load() != 1 {
		t.Fatalf("renew requests = %d, want exactly 1 (no fallback)", f.requests.Load())
	}
	certPEM, _, keyPEM := NewProvisioningService(zap.NewNop(), dir).ProvisioningCerts()
	if certPEM != oldLeaf || string(keyPEM) != string(oldKey) {
		t.Fatal("refused renewal changed the stored identity")
	}
}

func TestRenewalRejectsLeafForAnotherKey(t *testing.T) {
	ca := newRenewCA(t)
	_, endpoint, roots := startFakeRenew(t, func(w http.ResponseWriter, _ *x509.CertificateRequest) {
		other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		leaf := ca.issue(t, other.Public(), time.Now(), time.Now().Add(time.Hour))
		renewJSON(t, w, http.StatusOK, map[string]string{"certificate": leaf})
	})
	svc, dir, oldLeaf := enrolledForRenewal(t, ca, endpoint, roots)
	if ok, err := svc.RenewIfDue(context.Background(), time.Now()); ok || err == nil {
		t.Fatalf("RenewIfDue accepted a P-256 leaf for another key: %v, %v", ok, err)
	}
	if certPEM, _, _ := NewProvisioningService(zap.NewNop(), dir).ProvisioningCerts(); certPEM != oldLeaf {
		t.Fatal("rejected renewal changed the stored certificate")
	}
}

func TestRenewalOutcomesAreNamed(t *testing.T) {
	for code, want := range map[int]string{
		http.StatusAccepted:  "operator-approved grant",
		http.StatusForbidden: "denied the renewal: principal disabled",
	} {
		ca := newRenewCA(t)
		_, endpoint, roots := startFakeRenew(t, func(w http.ResponseWriter, _ *x509.CertificateRequest) {
			renewJSON(t, w, code, map[string]string{"detail": "principal disabled"})
		})
		svc, _, _ := enrolledForRenewal(t, ca, endpoint, roots)
		if _, err := svc.RenewIfDue(context.Background(), time.Now()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("HTTP %d: err = %v, want %q", code, err, want)
		}
	}
}

func TestRenewalNotDueOrExpired(t *testing.T) {
	ca := newRenewCA(t)
	f, endpoint, roots := startFakeRenew(t, renewed(t, ca))
	svc, _, _ := enrolledForRenewal(t, ca, endpoint, roots)
	if ok, err := svc.RenewIfDue(context.Background(), time.Now().Add(-60*24*time.Hour)); ok || err != nil {
		t.Fatalf("not-yet-due renewal = %v, %v", ok, err)
	}
	if _, err := svc.RenewIfDue(context.Background(), time.Now().Add(10*24*time.Hour)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired renewal err = %v", err)
	}
	if f.requests.Load() != 0 {
		t.Fatal("renewal contacted pki-core when it must not")
	}
}

// A crash after the renewed certificate was saved but before device-key.pem
// was replaced is finished at the next start; a pending key that never
// committed is dropped.
func TestInterruptedRenewalRecovery(t *testing.T) {
	ca := newRenewCA(t)
	svc, dir, _ := enrolledForRenewal(t, ca, "https://unused.invalid/v1/renew", nil)
	_, _, oldKey := svc.ProvisioningCerts()
	newKey, err := certs.GenerateMLDSAKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(dir, pendingDeviceKeyFile)

	// Not committed: the stored leaf is still the old key's.
	if err := os.WriteFile(pending, []byte(newKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, key := NewProvisioningService(zap.NewNop(), dir).ProvisioningCerts(); string(key) != string(oldKey) {
		t.Fatal("uncommitted pending key was adopted")
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal("uncommitted pending key was kept")
	}

	// Committed certificate, key not yet moved.
	signer, _ := certs.ParseSigningPrivateKeyPEM([]byte(newKey))
	data, _ := os.ReadFile(filepath.Join(dir, "provisioning.json"))
	var state provisioningState
	_ = json.Unmarshal(data, &state)
	state.CertPEM = ca.issue(t, signer.Public(), time.Now(), time.Now().Add(time.Hour))
	data, _ = json.Marshal(state)
	if err := WriteProvisioningState(dir, data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte(newKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, key := NewProvisioningService(zap.NewNop(), dir).ProvisioningCerts(); string(key) != newKey {
		t.Fatal("committed renewal key was not adopted")
	}
	if stored, _ := os.ReadFile(filepath.Join(dir, "device-key.pem")); string(stored) != newKey {
		t.Fatal("device-key.pem was not replaced by the renewal key")
	}
}

func TestRenewEndpointFor(t *testing.T) {
	t.Setenv(renewEndpointEnv, "")
	for in, want := range map[string]string{
		"https://acme.dev.pki.wendy.sh/1111/acme/directory": "https://renew.dev.pki.wendy.sh/v1/renew",
		"https://acme.pki.wendy.sh:8444/acme/directory":     "", // explicit port: frontends differ
		"http://acme.pki.wendy.sh/acme/directory":           "",
		"https://pki.wendy.sh/acme/directory":               "",
		"":                                                  "",
	} {
		if got := renewEndpointFor(in); got != want {
			t.Errorf("renewEndpointFor(%q) = %q, want %q", in, got, want)
		}
	}
	t.Setenv(renewEndpointEnv, "https://renew.example:8451/v1/renew")
	if got := renewEndpointFor(""); got != "https://renew.example:8451/v1/renew" {
		t.Errorf("override ignored: %q", got)
	}
}

// New pki-core enrollments use ML-DSA-65 from the first certificate.
func TestACMEProvisioningGeneratesMLDSA65DeviceKey(t *testing.T) {
	svc := NewProvisioningService(zap.NewNop(), t.TempDir())
	var deviceKey []byte
	stubACMEEnroll(t, func(_ context.Context, _ acmeenroll.Config, _ string, key []byte) (string, string, error) {
		deviceKey = append([]byte(nil), key...)
		return "leaf", "chain", nil
	})
	if _, err := NewProvisioningServiceV2(svc).StartACMEProvisioning(context.Background(), acmeProvisionRequest()); err != nil {
		t.Fatal(err)
	}
	signer, err := certs.ParseSigningPrivateKeyPEM(deviceKey)
	if err != nil {
		t.Fatal(err)
	}
	if key, ok := signer.(*mldsa.PrivateKey); !ok || key.PublicKey().Parameters() != mldsa.MLDSA65() {
		t.Fatalf("ACME device key = %T, want ML-DSA-65", signer)
	}
}

// The renewed leaf and chain must chain to the anchor the device already
// trusts: a renewal never installs a new CA into the agent's mTLS trust.
func TestRenewalRefusesForeignTrust(t *testing.T) {
	for name, respond := range map[string]func(ca, foreign renewCA, t *testing.T, w http.ResponseWriter, csr *x509.CertificateRequest){
		"leaf from another CA": func(_, foreign renewCA, t *testing.T, w http.ResponseWriter, csr *x509.CertificateRequest) {
			leaf := foreign.issue(t, csr.PublicKey, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
			renewJSON(t, w, http.StatusOK, map[string]string{"certificate": leaf + foreign.pem})
		},
		"foreign CA appended to a genuine chain": func(ca, foreign renewCA, t *testing.T, w http.ResponseWriter, csr *x509.CertificateRequest) {
			leaf := ca.issue(t, csr.PublicKey, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
			renewJSON(t, w, http.StatusOK, map[string]string{"certificate": leaf + ca.pem + foreign.pem})
		},
	} {
		t.Run(name, func(t *testing.T) {
			ca, foreign := newRenewCA(t), newRenewCA(t)
			_, endpoint, roots := startFakeRenew(t, func(w http.ResponseWriter, csr *x509.CertificateRequest) {
				respond(ca, foreign, t, w, csr)
			})
			svc, dir, oldLeaf := enrolledForRenewal(t, ca, endpoint, roots)
			if ok, err := svc.RenewIfDue(context.Background(), time.Now()); ok || err == nil || !strings.Contains(err.Error(), "trust anchor") {
				t.Fatalf("RenewIfDue = %v, %v; want a trust-anchor refusal", ok, err)
			}
			if certPEM, chainPEM, _ := NewProvisioningService(zap.NewNop(), dir).ProvisioningCerts(); certPEM != oldLeaf || chainPEM != ca.pem {
				t.Fatal("refused renewal changed the stored identity or trust")
			}
		})
	}
}

func TestRenewalDoesNotFollowRedirects(t *testing.T) {
	ca := newRenewCA(t)
	f, endpoint, roots := startFakeRenew(t, func(w http.ResponseWriter, _ *x509.CertificateRequest) {
		w.Header().Set("Location", "https://elsewhere.invalid/v1/renew")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	svc, _, _ := enrolledForRenewal(t, ca, endpoint, roots)
	if _, err := svc.RenewIfDue(context.Background(), time.Now()); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("err = %v, want the redirect reported", err)
	}
	if f.requests.Load() != 1 {
		t.Fatalf("renew requests = %d, want 1", f.requests.Load())
	}
}

// Once the renewed certificate is saved the renewal has happened: a failed key
// move must not read as a failure (that would start another renewal and
// overwrite the only matching key); the restart adopts the pending key.
func TestRenewalCommittedCertificateSurvivesKeyWriteFailure(t *testing.T) {
	ca := newRenewCA(t)
	_, endpoint, roots := startFakeRenew(t, renewed(t, ca))
	svc, dir, _ := enrolledForRenewal(t, ca, endpoint, roots)
	keyPath := filepath.Join(dir, "device-key.pem")
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(keyPath, 0o700); err != nil { // makes WriteDeviceKey fail
		t.Fatal(err)
	}
	if ok, err := svc.RenewIfDue(context.Background(), time.Now()); !ok || err != nil {
		t.Fatalf("RenewIfDue = %v, %v; want renewed despite the key write failure", ok, err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	certPEM, _, keyPEM := NewProvisioningService(zap.NewNop(), dir).ProvisioningCerts()
	if _, err := certs.TLSKeyPair(certPEM, "", string(keyPEM)); err != nil || !isMLDSA65Key(keyPEM) {
		t.Fatalf("restart did not recover the renewed identity: %v", err)
	}
}

// A pending key that outlived an unenrollment is removed at the next start.
func TestPendingKeyDroppedOnUnenrolledDevice(t *testing.T) {
	dir := t.TempDir()
	key, _ := certs.GenerateMLDSAKeyPair()
	pending := filepath.Join(dir, pendingDeviceKeyFile)
	if err := os.WriteFile(pending, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	NewProvisioningService(zap.NewNop(), dir)
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal("pending key survived on an unenrolled device")
	}
}

// A leftover key of the other algorithm is replaced, never enrolled with.
func TestEnrollmentReplacesLeftoverKeyOfOtherAlgorithm(t *testing.T) {
	p256, _ := certs.GenerateKeyPair()
	mldsaKey, _ := certs.GenerateMLDSAKeyPair()

	t.Run("ACME replaces P-256", func(t *testing.T) {
		dir := t.TempDir()
		if err := WriteDeviceKey(dir, []byte(p256)); err != nil {
			t.Fatal(err)
		}
		var got []byte
		stubACMEEnroll(t, func(_ context.Context, _ acmeenroll.Config, _ string, key []byte) (string, string, error) {
			got = append([]byte(nil), key...)
			return "leaf", "chain", nil
		})
		if _, err := NewProvisioningServiceV2(NewProvisioningService(zap.NewNop(), dir)).StartACMEProvisioning(context.Background(), acmeProvisionRequest()); err != nil {
			t.Fatal(err)
		}
		if !isMLDSA65Key(got) {
			t.Fatal("ACME enrolled with the leftover P-256 key")
		}
	})
	t.Run("ACME reuses ML-DSA-65", func(t *testing.T) {
		dir := t.TempDir()
		if err := WriteDeviceKey(dir, []byte(mldsaKey)); err != nil {
			t.Fatal(err)
		}
		var got []byte
		stubACMEEnroll(t, func(_ context.Context, _ acmeenroll.Config, _ string, key []byte) (string, string, error) {
			got = append([]byte(nil), key...)
			return "leaf", "chain", nil
		})
		if _, err := NewProvisioningServiceV2(NewProvisioningService(zap.NewNop(), dir)).StartACMEProvisioning(context.Background(), acmeProvisionRequest()); err != nil {
			t.Fatal(err)
		}
		if string(got) != mldsaKey {
			t.Fatal("ACME did not reuse its own leftover key")
		}
	})
	t.Run("legacy replaces ML-DSA-65", func(t *testing.T) {
		svc, dir := newTestProvisioningService(t)
		if err := WriteDeviceKey(dir, []byte(mldsaKey)); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.StartProvisioning(context.Background(), &agentpb.StartProvisioningRequest{OrganizationId: 1, CloudHost: "test.wendy.io", AssetId: 10}); err != nil {
			t.Fatal(err)
		}
		if stored, _ := os.ReadFile(filepath.Join(dir, "device-key.pem")); isMLDSA65Key(stored) {
			t.Fatal("legacy Cloud enrollment kept the ML-DSA-65 leftover")
		}
	})
}
