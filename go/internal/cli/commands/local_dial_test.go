package commands

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/agent/mtls"
	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type localVersionAgent struct {
	agentpb.UnimplementedWendyAgentServiceServer
}

func (localVersionAgent) GetAgentVersion(context.Context, *agentpb.GetAgentVersionRequest) (*agentpb.GetAgentVersionResponse, error) {
	return &agentpb.GetAgentVersionResponse{Version: "test"}, nil
}

// startLocalOnlyAgent runs the agent's own local-only mTLS server: a
// certificate self-signed over the device key and owner as the only trusted
// key, behind the default-deny Authorizer.
func startLocalOnlyAgent(t *testing.T, owner crypto.Signer) string {
	t.Helper()
	return startLocalDevice(t, map[crypto.Signer]trustedkeys.Role{owner: trustedkeys.RoleOwner}).grpcAddr
}

type localDevice struct {
	grpcAddr     string
	registryPort int
	keys         *trustedkeys.Store
	deviceKeyPEM string
}

// startLocalDevice runs a device that is not enrolled, as #2366's agent
// starts it: the mTLS gRPC listener and the image registry, both serving a
// certificate self-signed over the same device key and admitting the given
// trusted keys (the registry operator and above).
func startLocalDevice(t *testing.T, trusted map[crypto.Signer]trustedkeys.Role) localDevice {
	t.Helper()
	keys, err := trustedkeys.Open(filepath.Join(t.TempDir(), "trusted-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	for key, role := range trusted {
		spki, err := localkey.PublicKeyDER(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := keys.Add(spki, role, role.String(), trustedkeys.AddedByInstall, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	deviceKeyPEM, err := certs.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := mtls.SelfSignedCert(deviceKeyPEM, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mtls.NewServer(certPEM, "", deviceKeyPEM, zap.NewNop(), keys.Floor(),
		&interceptor.Authorizer{Logger: zap.NewNop(), Listener: interceptor.ListenerMTLS, Keys: keys, OrgMode: interceptor.OrgModeOff})
	if err != nil {
		t.Fatal(err)
	}
	agentpb.RegisterWendyAgentServiceServer(srv, localVersionAgent{})
	dev := localDevice{grpcAddr: serveTest(t, srv), keys: keys, deviceKeyPEM: deviceKeyPEM}
	dev.registryPort = startLocalRegistry(t, certPEM, deviceKeyPEM, keys)
	return dev
}

// startLocalRegistry serves a stand-in registry (200 on /v2/) behind the
// agent's own registry TLS (mtls.NewRegistryTLSConfig).
func startLocalRegistry(t *testing.T, certPEM, keyPEM string, keys *trustedkeys.Store) int {
	t.Helper()
	cfg, err := mtls.NewRegistryTLSConfig(certPEM, "", keyPEM, nil, keys.Floor(), keys)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// startEnrolledAgent serves a certificate naming a tenant identity, as a
// cloud-enrolled device does.
func startEnrolledAgent(t *testing.T) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	u, _ := url.Parse("urn:wendy:org:7:asset:9")
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "enrolled"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		URIs:         []*url.URL{u},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ClientAuth:   tls.RequireAnyClientCert,
	})))
	agentpb.RegisterWendyAgentServiceServer(srv, localVersionAgent{})
	return serveTest(t, srv)
}

func serveTest(t *testing.T, srv *grpc.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

// useLocalKey installs this computer's local operator key for the test.
func useLocalKey(t *testing.T) crypto.Signer {
	t.Helper()
	key, err := localkey.LoadOrCreate(filepath.Join(t.TempDir(), localkey.FileName))
	if err != nil {
		t.Fatal(err)
	}
	orig := localOperatorKeyFn
	localOperatorKeyFn = func() (crypto.Signer, error) { return key, nil }
	t.Cleanup(func() { localOperatorKeyFn = orig })
	return key
}

func forbidPlaintext(t *testing.T) {
	t.Helper()
	orig := plaintextConnectFn
	plaintextConnectFn = func(context.Context, string) (*grpcclient.AgentConnection, error) {
		t.Error("ladder fell back to plaintext")
		return nil, errors.New("plaintext forbidden")
	}
	t.Cleanup(func() { plaintextConnectFn = orig })
}

func ladderCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLadderDialsLocalOnlyDeviceWithLocalKey(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	owner := useLocalKey(t)
	addr := startLocalOnlyAgent(t, owner)

	// With and without cloud certificates: the cloud rung fails against the
	// device's self-signed certificate and the local key carries the dial.
	for _, certs := range [][]config.CertificateInfo{nil, {selfSignedCLICert(t, 7)}} {
		conn, _, err := dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", addr), certs)
		if err != nil {
			t.Fatalf("certs=%d: ladder: %v", len(certs), err)
		}
		if !conn.IsLocalOperator || !conn.IsMTLS {
			t.Fatalf("certs=%d: connection is not the local-operator one", len(certs))
		}
		conn.Close()
	}
	store, err := openLocalPinStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	if !store.Has(devicepin.LocalPinKey("box")) {
		t.Fatal("device key not pinned on first contact")
	}
}

func TestLadderRefusesWhenLocalKeyNotTrusted(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	other, _ := localkey.LoadOrCreate(filepath.Join(t.TempDir(), localkey.FileName))
	addr := startLocalOnlyAgent(t, other)
	mine := useLocalKey(t)

	_, _, err := dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", addr), nil)
	if !errors.Is(err, errProvisionedAgentUnauthorized) {
		t.Fatalf("err = %v, want an unauthorized refusal", err)
	}
	fp, _ := localkey.Fingerprint(mine)
	for _, want := range []string{fp, "wendy device keys add", "wendy auth local-key show"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
}

// Enrolled: the cloud identity is used; the local rung never runs.
func TestLadderUsesCloudIdentityFirst(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	cloudConn := grpcclient.NewFromConn(nil)
	stubMTLSCandidate(t, func(context.Context, *mtlsWalk, string, bool) (*grpcclient.AgentConnection, error) {
		return cloudConn, nil
	})
	orig := dialLocalOperatorFn
	dialLocalOperatorFn = func(context.Context, dialTarget, []string) (*grpcclient.AgentConnection, localDialResult, error) {
		t.Error("local rung ran although the cloud identity connected")
		return nil, localDialResult{}, nil
	}
	t.Cleanup(func() { dialLocalOperatorFn = orig })

	conn, _, err := dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", "127.0.0.1:50051"), []config.CertificateInfo{selfSignedCLICert(t, 7)})
	if err != nil || conn != cloudConn {
		t.Fatalf("ladder = (%v, %v), want the cloud connection", conn, err)
	}
}

func TestLadderEnrolledDeviceWithoutCloudLoginRefuses(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	useLocalKey(t)
	addr := startEnrolledAgent(t)

	_, _, err := dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", addr), nil)
	if !errors.Is(err, errProvisionedAgentUnauthorized) || !strings.Contains(err.Error(), "wendy auth login") {
		t.Fatalf("err = %v, want a refusal naming wendy auth login", err)
	}
}

func TestLadderNeverReachesLocallyPinnedDeviceInTheClear(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	owner := useLocalKey(t)
	addr := startLocalOnlyAgent(t, owner)
	conn, _, err := dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", addr), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// Same name, nothing listening now.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	_, _, err = dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", dead), nil)
	if !errors.Is(err, errNoAuthenticatedEndpoint) {
		t.Fatalf("err = %v, want no-authenticated-endpoint refusal", err)
	}
}

func TestLadderRefusesChangedLocalDeviceKey(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	owner := useLocalKey(t)
	conn, _, err := dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", startLocalOnlyAgent(t, owner)), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()

	_, _, err = dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", startLocalOnlyAgent(t, owner)), nil)
	if !errors.Is(err, errDeviceIdentityRefused) || !strings.Contains(err.Error(), "wendy device unpin box") {
		t.Fatalf("err = %v, want an identity refusal naming wendy device unpin", err)
	}
}

// A name pinned to a cloud-enrolled identity that now answers as a locally
// owned device is the identity change the pin exists to catch.
func TestLadderRefusesCloudPinnedHostAnsweringAsLocalDevice(t *testing.T) {
	owner := useLocalKey(t)
	addr := startLocalOnlyAgent(t, owner)
	host, _, _ := net.SplitHostPort(addr)
	setTempConfig(t, &config.Config{DevicePins: map[string]config.DevicePin{
		host: {OrgID: 7, CloudGRPC: "cloud.example:443", AssetID: "9"},
	}})
	setPinCache(t)
	forbidPlaintext(t)

	_, _, err := dialAgentLadderWithCerts(ladderCtx(t), newDialTarget(host, addr), nil)
	if !errors.Is(err, errDeviceIdentityRefused) {
		t.Fatalf("err = %v, want an identity refusal", err)
	}
}

// `wendy device unpin <name>`, the step the changed-key refusal names, clears
// the local device key pin.
func TestUnpinClearsLocalDeviceKeyPin(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	store, err := openLocalPinStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := localkey.Certificate(useLocalKey(t), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckLocal("box", cert.Leaf); err != nil {
		t.Fatal(err)
	}
	cleared := clearPinsGoverning(&config.Config{}, "box")
	if len(cleared) != 1 || cleared[0].key != devicepin.LocalPinKey("box") {
		t.Fatalf("cleared = %+v", cleared)
	}
	if store, _ = openLocalPinStoreFn(); store.Has(devicepin.LocalPinKey("box")) {
		t.Fatal("local pin survived unpin")
	}
}
