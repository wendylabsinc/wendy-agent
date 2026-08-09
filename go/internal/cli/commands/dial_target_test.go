package commands

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
	"net"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestExpectedIdentityFor(t *testing.T) {
	cases := []struct {
		name string
		pin  *config.DevicePin // nil = unpinned
		want *certs.WendyIdentity
	}{
		{name: "unpinned host is unconstrained", pin: nil, want: nil},
		{
			name: "pinned with asset constrains exactly",
			pin:  &config.DevicePin{OrgID: 7, AssetID: "42"},
			want: &certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"},
		},
		{
			name: "pinned without an asset stays unconstrained",
			pin:  &config.DevicePin{OrgID: 7},
			want: nil,
		},
	}
	// Drive expectedIdentityFor through a config injected via the
	// loadConfigForPinFn seam so the test never touches the real config file.
	// The pin is stored under the normalised key ("orin") and looked up by the
	// name a user would type ("orin.local"), so this also covers the key
	// normalisation the ladder depends on.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tc.pin != nil {
				cfg.DevicePins = map[string]config.DevicePin{"orin": *tc.pin}
			}
			orig := loadConfigForPinFn
			loadConfigForPinFn = func() (*config.Config, error) { return cfg, nil }
			t.Cleanup(func() { loadConfigForPinFn = orig })

			got := expectedIdentityFor("orin.local")
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("expectedIdentityFor = %+v, want nil (an unconstrained dial)", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("expectedIdentityFor = nil, want %+v", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("expectedIdentityFor = %+v, want %+v", *got, *tc.want)
			}

			// newDialTarget must carry exactly that constraint, and the pin's
			// mere existence — asset or not — is what forbids the plaintext
			// rung. A pin with no asset still means "this host has answered
			// mTLS before".
			target := newDialTarget("orin.local", "10.0.0.9:50051")
			if (target.Expected == nil) != (tc.want == nil) {
				t.Fatalf("newDialTarget Expected = %v, want the same constraint as expectedIdentityFor (%v)", target.Expected, tc.want)
			}
			if want := tc.pin != nil; isPinned("orin.local") != want {
				t.Fatalf("isPinned = %v, want %v", !want, want)
			}
			if target.Addr != "10.0.0.9:50051" || target.PinKey != "orin.local" {
				t.Fatalf("newDialTarget = %+v, want the requested name and the dialled address", target)
			}
		})
	}
}

// TestPinnedHostSkipsPlaintextRung is the security-critical case: a host we
// have seen over mTLS must never be reached unauthenticated, no matter what
// the TXT records or the cache claim.
//
// The mTLS rungs here fail with a plain transport error (nothing is listening),
// which is precisely the shape that today falls through to the plaintext rung —
// isCertRejectionError does not, and must not, match it. The refusal therefore
// has to come from the pin itself, not from error-string matching.
func TestPinnedHostSkipsPlaintextRung(t *testing.T) {
	setTempConfig(t, &config.Config{DevicePins: map[string]config.DevicePin{
		"orin": {OrgID: 7, CloudGRPC: "grpc.wendy.dev:443", AssetID: "42", Source: config.PinSourceLAN},
	}})

	addr := deadAgentAddr(t)

	plaintextCalls := 0
	origPlaintext := plaintextConnectFn
	plaintextConnectFn = func(ctx context.Context, address string) (*grpcclient.AgentConnection, error) {
		plaintextCalls++
		return grpcclient.NewFromConn(nil), nil
	}
	t.Cleanup(func() { plaintextConnectFn = origPlaintext })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	target := newDialTarget("orin.local", addr)
	conn, mtlsErr, err := dialAgentLadderWithCerts(ctx, target, []config.CertificateInfo{selfSignedCLICert(t, 7)})
	if conn != nil {
		conn.Close()
	}

	if plaintextCalls != 0 {
		t.Fatalf("plaintext rung attempted %d times for a pinned host; a host already reached over mTLS must never be dialled unauthenticated", plaintextCalls)
	}
	if conn != nil {
		t.Fatalf("ladder returned a connection (%v) for a pinned host whose mTLS rungs all failed", conn)
	}
	if err == nil {
		t.Fatal("ladder returned no error for a pinned host whose mTLS rungs all failed")
	}
	if !strings.Contains(err.Error(), "refusing to fall back to an unauthenticated connection") ||
		!strings.Contains(err.Error(), "wendy device unpin orin.local") {
		t.Fatalf("err = %v, want the pinned-host refusal naming the unpin escape hatch", err)
	}
	// The mTLS diagnostic must survive the refusal — it is the only clue to why
	// no authenticated endpoint answered.
	if mtlsErr == nil {
		t.Fatal("mtlsErr = nil, want the last mTLS probe failure preserved for diagnostics")
	}
	// The load-bearing assertion for the guard's independence: this failure is
	// NOT a cert rejection, so the pre-existing isCertRejectionError branch
	// would have fallen straight through to plaintext.
	if isCertRejectionError(mtlsErr) {
		t.Fatalf("mtlsErr = %v is a cert rejection; this test must exercise the transport-error shape that otherwise reaches the plaintext rung", mtlsErr)
	}
}

// deadAgentAddr returns a 127.0.0.1 address whose port AND port+1 both refuse
// connections, so every rung of the ladder (which tries both) fails with a
// transport error rather than anything cert-shaped.
func deadAgentAddr(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		first, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserving a port: %v", err)
		}
		port := first.Addr().(*net.TCPAddr).Port
		second, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port+1))
		first.Close()
		if err != nil {
			continue // port+1 is in use; try another pair
		}
		second.Close()
		return fmt.Sprintf("127.0.0.1:%d", port)
	}
	t.Fatal("could not find two consecutive free ports")
	return ""
}

// selfSignedCLICert builds a usable client CertificateInfo (loadable keypair
// plus a chain PEM, both of which ConnectWithTLSExpecting requires before it
// will dial) so the mTLS rungs fail at the transport, not while assembling
// their TLS config.
func selfSignedCLICert(t *testing.T, orgID int) config.CertificateInfo {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "wendy/test/cli"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling key: %v", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return config.CertificateInfo{
		OrganizationID:      orgID,
		PemCertificate:      certPEM,
		PemPrivateKey:       keyPEM,
		PemCertificateChain: certPEM,
	}
}
