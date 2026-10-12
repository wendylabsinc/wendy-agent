package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/unitree"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

const unitreeTestKey = "00112233445566778899aabbccddeeff"

type testUnitreeLink struct {
	online atomic.Bool
	done   chan struct{}
	once   sync.Once
}

func newTestUnitreeLink() *testUnitreeLink {
	c := &testUnitreeLink{done: make(chan struct{})}
	c.online.Store(true)
	return c
}
func (c *testUnitreeLink) Connected() bool       { return c.online.Load() }
func (c *testUnitreeLink) Done() <-chan struct{} { return c.done }
func (c *testUnitreeLink) Close() error {
	c.once.Do(func() { c.online.Store(false); close(c.done) })
	return nil
}

func unitreeTestContext() context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.UnixAddr{Name: "test", Net: "unix"}})
}

func testUnitreeService(t *testing.T) *UnitreeService {
	t.Helper()
	s := NewUnitreeService(context.Background(), filepath.Join(t.TempDir(), "pairings.json"))
	s.retry = time.Hour
	t.Cleanup(s.Close)
	return s
}

func unitreeTestRequest() *agentpbv2.PairUnitreeRequest {
	return &agentpbv2.PairUnitreeRequest{Serial: "GO2TEST1", Name: "My Go2", Address: "192.0.2.20", EncryptionKey: unitreeTestKey, Token: "cloud-token-secret"}
}

func TestUnitreePairRequiresAuthenticatedAgentTransport(t *testing.T) {
	s := testUnitreeService(t)
	s.dial = func(context.Context, string, string, string) (unitreeLink, error) {
		t.Fatal("unauthenticated request reached robot")
		return nil, nil
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 50051}})
	if _, err := s.PairUnitree(ctx, unitreeTestRequest()); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("plaintext pairing: %v", err)
	}
	if _, err := s.ListUnitreePairings(ctx, nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("plaintext list: %v", err)
	}
	if _, err := s.DiscoverUnitreeRobots(ctx, nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("plaintext discovery: %v", err)
	}
	if _, err := s.ForgetUnitree(ctx, nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("plaintext forget: %v", err)
	}
}

func TestUnitreeTransportRequiresCompletedMutualTLS(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state tls.ConnectionState
		ok    bool
	}{
		{"mutual TLS", tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{{}}}, true},
		{"server TLS only", tls.ConnectionState{HandshakeComplete: true}, false},
		{"incomplete handshake", tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := peer.NewContext(context.Background(), &peer.Peer{
				Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1)}, AuthInfo: credentials.TLSInfo{State: tc.state},
			})
			err := requireUnitreeTransport(ctx)
			if (err == nil) != tc.ok {
				t.Fatalf("transport authorization: %v", err)
			}
		})
	}
}

func TestUnitreePairValidatesBeforeSavingAndRetainsSession(t *testing.T) {
	s := testUnitreeService(t)
	link := newTestUnitreeLink()
	calls := 0
	s.dial = func(ctx context.Context, address, token, key string) (unitreeLink, error) {
		calls++
		if address != "192.0.2.20" || token != "cloud-token-secret" || key != unitreeTestKey {
			t.Fatal("pairing credentials or address were lost")
		}
		if saved, _ := s.store.List(); len(saved) != 0 {
			t.Fatal("saved before validation")
		}
		return link, nil
	}
	ctx, cancel := context.WithCancel(unitreeTestContext())
	resp, err := s.PairUnitree(ctx, unitreeTestRequest())
	if err != nil || !resp.GetRobot().GetConnected() {
		t.Fatalf("pair: %v, %v", resp, err)
	}
	cancel()
	if !link.Connected() {
		t.Fatal("RPC cancellation closed retained connection")
	}
	saved, err := s.store.List()
	if err != nil || len(saved) != 1 || saved[0].Device.EncryptionKey != unitreeTestKey {
		t.Fatalf("saved pairing: %v", err)
	}
	listed, err := s.ListUnitreePairings(unitreeTestContext(), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := protojson.Marshal(listed)
	if strings.Contains(string(raw), unitreeTestKey) || strings.Contains(string(raw), "cloud-token-secret") {
		t.Fatal("RPC exposed secrets")
	}
	req := unitreeTestRequest()
	req.Token, req.EncryptionKey = "", ""
	if _, err := s.PairUnitree(unitreeTestContext(), req); err != nil || calls != 1 {
		t.Fatalf("existing connection was not reused: calls=%d err=%v", calls, err)
	}
	if _, err := s.ForgetUnitree(unitreeTestContext(), &agentpbv2.ForgetUnitreeRequest{Serial: " " + req.Serial + " "}); err != nil {
		t.Fatal(err)
	}
	if link.Connected() {
		t.Fatal("forget left connection open")
	}
	if saved, _ := s.store.List(); len(saved) != 0 {
		t.Fatal("forget retained robot key")
	}
}

func TestUnitreeFailedPairDoesNotSaveOrExposeSecrets(t *testing.T) {
	s := testUnitreeService(t)
	s.dial = func(context.Context, string, string, string) (unitreeLink, error) {
		return nil, errors.New("remote error " + unitreeTestKey + " cloud-token-secret")
	}
	_, err := s.PairUnitree(unitreeTestContext(), unitreeTestRequest())
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), unitreeTestKey) || strings.Contains(err.Error(), "cloud-token-secret") {
		t.Fatalf("unsafe pairing error: %v", err)
	}
	if saved, _ := s.store.List(); len(saved) != 0 {
		t.Fatal("failed pairing was saved")
	}
}

type failingUnitreeStore struct{ unitreePairingStore }

func (s failingUnitreeStore) Save(unitree.Pairing) error { return errors.New("disk full") }

func TestUnitreeSaveFailureClosesNewConnection(t *testing.T) {
	s := testUnitreeService(t)
	s.store = failingUnitreeStore{s.store}
	link := newTestUnitreeLink()
	s.dial = func(context.Context, string, string, string) (unitreeLink, error) { return link, nil }
	if _, err := s.PairUnitree(unitreeTestContext(), unitreeTestRequest()); status.Code(err) != codes.Internal {
		t.Fatalf("save: %v", err)
	}
	if link.Connected() {
		t.Fatal("unsaved connection leaked")
	}
}

func TestUnitreeRestoreUsesRobotKeyWithoutCloudToken(t *testing.T) {
	s := testUnitreeService(t)
	if err := s.store.Save(unitree.Pairing{Device: unitree.Device{Serial: "GO2TEST1", Address: "192.0.2.20", EncryptionKey: unitreeTestKey}, PairedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	type attempt struct{ address, token, key string }
	attempted := make(chan attempt, 2)
	link := newTestUnitreeLink()
	s.dial = func(ctx context.Context, address, token, key string) (unitreeLink, error) {
		attempted <- attempt{address, token, key}
		return link, nil
	}
	if err := s.Restore(); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-attempted:
		if a.address != "192.0.2.20" || a.token != "" || a.key != unitreeTestKey {
			t.Fatal("restore did not use saved robot key")
		}
	case <-time.After(time.Second):
		t.Fatal("restore did not connect")
	}
	s.Close()
	if link.Connected() {
		t.Fatal("shutdown did not close robot connection")
	}
}

func TestUnitreePairRejectsInvalidAddressAndMissingKey(t *testing.T) {
	s := testUnitreeService(t)
	s.dial = func(context.Context, string, string, string) (unitreeLink, error) {
		t.Fatal("invalid input reached network")
		return nil, nil
	}
	for _, address := range []string{"", "go2.local", "127.0.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "192.0.2.2:9991", "http://192.0.2.2", "::1"} {
		req := unitreeTestRequest()
		req.Address = address
		if _, err := s.PairUnitree(unitreeTestContext(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("address %q: %v", address, err)
		}
	}
	req := unitreeTestRequest()
	req.EncryptionKey = ""
	req.Token = ""
	if _, err := s.PairUnitree(unitreeTestContext(), req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing key: %v", err)
	}
	req.EncryptionKey = "invalid-nonempty-key"
	req.Token = "cloud-token-secret"
	if _, err := s.PairUnitree(unitreeTestContext(), req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed key: %v", err)
	}
}

func TestUnitreeLegacyPairUsesAccountTokenWithoutDeviceKey(t *testing.T) {
	s := testUnitreeService(t)
	link := newTestUnitreeLink()
	s.dial = func(_ context.Context, _ string, token, key string) (unitreeLink, error) {
		if token != "cloud-token-secret" || key != "" {
			t.Fatal("legacy pairing lost its token or invented a key")
		}
		return link, nil
	}
	req := unitreeTestRequest()
	req.EncryptionKey = ""
	if _, err := s.PairUnitree(unitreeTestContext(), req); err != nil {
		t.Fatal(err)
	}
	saved, err := s.store.List()
	if err != nil || len(saved) != 1 || saved[0].Device.EncryptionKey != "" {
		t.Fatalf("legacy pairing was not saved: %v", err)
	}
	req.Token = ""
	if _, err := s.PairUnitree(unitreeTestContext(), req); err != nil {
		t.Fatalf("existing legacy pairing was not reusable: %v", err)
	}
}

func TestUnitreeManualPairWaitsForBackgroundDialCleanup(t *testing.T) {
	s := testUnitreeService(t)
	previous := unitree.Pairing{Device: unitree.Device{Serial: "GO2TEST1", Address: "192.0.2.20", EncryptionKey: unitreeTestKey}, PairedAt: time.Now()}
	if err := s.store.Save(previous); err != nil {
		t.Fatal(err)
	}
	started, canceled, cleaned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	manualStarted := make(chan struct{}, 1)
	link := newTestUnitreeLink()
	s.dial = func(ctx context.Context, _ string, token, _ string) (unitreeLink, error) {
		if token == "" {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			close(cleaned)
			return nil, ctx.Err()
		}
		select {
		case <-cleaned:
		default:
			t.Error("manual pairing overlapped a background dial")
			return nil, errors.New("client slot still occupied")
		}
		manualStarted <- struct{}{}
		return link, nil
	}
	if err := s.Restore(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background reconnect did not start")
	}
	paired := make(chan error, 1)
	go func() {
		_, err := s.PairUnitree(unitreeTestContext(), unitreeTestRequest())
		paired <- err
	}()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("manual pairing did not cancel the background dial")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-paired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual pairing did not finish")
	}
	select {
	case <-manualStarted:
	default:
		t.Fatal("manual connection was not established")
	}
}

func TestUnitreeFailedManualPairRestoresPreviousSession(t *testing.T) {
	s := testUnitreeService(t)
	oldLink, restoredLink := newTestUnitreeLink(), newTestUnitreeLink()
	restored := make(chan struct{}, 1)
	s.dial = func(_ context.Context, address, token, key string) (unitreeLink, error) {
		if token == "" {
			if address != "192.0.2.20" || key != unitreeTestKey {
				t.Error("recovery did not use the previous pairing")
			}
			restored <- struct{}{}
			return restoredLink, nil
		}
		if address == "192.0.2.20" {
			return oldLink, nil
		}
		if oldLink.Connected() {
			t.Error("manual connection started before the old client slot was released")
		}
		return nil, errors.New("replacement endpoint unreachable")
	}
	req := unitreeTestRequest()
	if _, err := s.PairUnitree(unitreeTestContext(), req); err != nil {
		t.Fatal(err)
	}
	req.Address = "192.0.2.21"
	if _, err := s.PairUnitree(unitreeTestContext(), req); status.Code(err) != codes.Unavailable {
		t.Fatalf("replacement pairing: %v", err)
	}
	select {
	case <-restored:
	case <-time.After(2 * time.Second):
		t.Fatal("failed manual pairing did not resume the previous session")
	}
	saved, err := s.store.List()
	if err != nil || len(saved) != 1 || saved[0].Device.Address != "192.0.2.20" {
		t.Fatalf("failed pairing replaced the saved endpoint: %v", err)
	}
	s.Close()
	if restoredLink.Connected() {
		t.Fatal("restored connection was not owned by the service")
	}
}

func TestUnitreeDiscoveryRunsOnAgentAndReturnsNoSecrets(t *testing.T) {
	s := testUnitreeService(t)
	s.discover = func(ctx context.Context, serials ...string) ([]unitree.Device, error) {
		if len(serials) != 1 || serials[0] != "GO2TEST1" {
			t.Fatal("known serial missing")
		}
		return []unitree.Device{{Serial: "GO2TEST1", Address: "192.0.2.20", EncryptionKey: unitreeTestKey}}, nil
	}
	resp, err := s.DiscoverUnitreeRobots(unitreeTestContext(), &agentpbv2.DiscoverUnitreeRobotsRequest{KnownSerials: []string{"GO2TEST1"}})
	if err != nil || len(resp.GetRobots()) != 1 || resp.Robots[0].Connected || resp.Robots[0].Paired {
		t.Fatalf("discovery: %v %v", resp, err)
	}
	raw, _ := protojson.Marshal(resp)
	if strings.Contains(string(raw), unitreeTestKey) {
		t.Fatal("discovery exposed key")
	}
}
