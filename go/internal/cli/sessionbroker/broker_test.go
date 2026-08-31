//go:build !windows

package sessionbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/stats"
)

type testAgentServer struct {
	agentpb.UnimplementedWendyAgentServiceServer
}

func (testAgentServer) GetAgentVersion(context.Context, *agentpb.GetAgentVersionRequest) (*agentpb.GetAgentVersionResponse, error) {
	return &agentpb.GetAgentVersionResponse{Version: "broker-test"}, nil
}

type testContainerServer struct {
	agentpb.UnimplementedWendyContainerServiceServer
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wsb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func (testContainerServer) RunContainer(_ *agentpb.RunContainerLayersRequest, stream grpc.ServerStreamingServer[agentpb.RunContainerLayersResponse]) error {
	if err := stream.Send(&agentpb.RunContainerLayersResponse{}); err != nil {
		return err
	}
	return stream.Send(&agentpb.RunContainerLayersResponse{})
}

// WriteChunks completes after a single message while the client is typically
// still sending — the shape of a real agent finishing (or refusing) a chunk
// upload early, which is exactly when the proxy must not replace the upstream's
// terminal status with the send side's io.EOF.
func (testContainerServer) WriteChunks(stream grpc.ClientStreamingServer[agentpb.WriteChunksRequest, agentpb.WriteChunksResponse]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return stream.SendAndClose(&agentpb.WriteChunksResponse{})
}

// compressionRecorder records the grpc-encoding each inbound RPC arrived with,
// in call order.
type compressionRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *compressionRecorder) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

func (r *compressionRecorder) HandleRPC(_ context.Context, s stats.RPCStats) {
	if ih, ok := s.(*stats.InHeader); ok {
		r.mu.Lock()
		r.seen = append(r.seen, ih.Compression)
		r.mu.Unlock()
	}
}

func (r *compressionRecorder) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (r *compressionRecorder) HandleConn(context.Context, stats.ConnStats) {}

func (r *compressionRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func startUpstream(t *testing.T, opts ...grpc.ServerOption) (*grpc.ClientConn, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(opts...)
	agentpb.RegisterWendyAgentServiceServer(server, testAgentServer{})
	agentpb.RegisterWendyContainerServiceServer(server, testContainerServer{})
	go server.Serve(lis) //nolint:errcheck
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		server.Stop()
		lis.Close()
		t.Fatal(err)
	}
	return conn, func() {
		conn.Close()
		server.Stop()
		lis.Close()
	}
}

// startServing runs serve() for spec against upstream and waits until the
// broker's socket and state exist, returning the proxy paths and the serve
// result channel.
func startServing(t *testing.T, ctx context.Context, dir string, spec Spec, upstream *grpc.ClientConn, idleTTL time.Duration) (socketPath, statePath string, done chan error) {
	t.Helper()
	done = make(chan error, 1)
	go func() { done <- serve(ctx, dir, spec, upstream, idleTTL) }()
	socketPath, statePath = paths(dir, spec.Key, spec.Expected)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(statePath); err == nil {
			return socketPath, statePath, done
		}
		if time.Now().After(deadline) {
			t.Fatal("broker state did not appear")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestServePreservesRequestCompression(t *testing.T) {
	dir := shortTempDir(t)
	rec := &compressionRecorder{}
	upstream, cleanup := startUpstream(t, grpc.StatsHandler(rec))
	defer cleanup()
	spec := Spec{
		Key:             "gzip.local",
		Host:            "gzip.local",
		Addr:            "127.0.0.1:50052",
		CertFingerprint: "public-cert-hash",
		Expected:        certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "45"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketPath, _, _ := startServing(t, ctx, dir, spec, upstream, time.Hour)

	proxy, err := grpcclient.ConnectSessionProxy(context.Background(), socketPath, spec.Host, spec.Addr, &config.CertificateInfo{OrganizationID: 7}, spec.Expected)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	// WriteChunks is opened with the gzip compressor specifically because raw
	// chunk payloads have stalled USB-NCM links (see chunkpush.go). A broker in
	// the middle must keep that property on the real device link.
	if _, err := proxy.AgentService.GetAgentVersion(context.Background(), &agentpb.GetAgentVersionRequest{}, grpc.UseCompressor(grpcgzip.Name)); err != nil {
		t.Fatalf("compressed unary RPC through broker: %v", err)
	}
	if _, err := proxy.AgentService.GetAgentVersion(context.Background(), &agentpb.GetAgentVersionRequest{}); err != nil {
		t.Fatalf("uncompressed unary RPC through broker: %v", err)
	}

	seen := rec.snapshot()
	if len(seen) != 2 || seen[0] != grpcgzip.Name || seen[1] != "" {
		t.Fatalf("upstream saw request compression %q, want [%q \"\"]", seen, grpcgzip.Name)
	}
}

func TestServeRelaysTerminalStatusWhenUpstreamEndsWhileSending(t *testing.T) {
	dir := shortTempDir(t)
	upstream, cleanup := startUpstream(t)
	defer cleanup()
	spec := Spec{
		Key:             "earlyclose.local",
		Host:            "earlyclose.local",
		Addr:            "127.0.0.1:50052",
		CertFingerprint: "public-cert-hash",
		Expected:        certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "46"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketPath, _, _ := startServing(t, ctx, dir, spec, upstream, time.Hour)

	proxy, err := grpcclient.ConnectSessionProxy(context.Background(), socketPath, spec.Host, spec.Addr, &config.CertificateInfo{OrganizationID: 7}, spec.Expected)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	wc, err := proxy.ContainerService.WriteChunks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Keep sending until the completed upstream surfaces: gRPC reports io.EOF
	// from Send once the server has closed, and CloseAndRecv must then return
	// the upstream's real terminal state — not the proxy's internal io.EOF.
	payload := bytes.Repeat([]byte{0xAB}, 32*1024)
	for i := 0; i < 4096; i++ {
		if err := wc.Send(&agentpb.WriteChunksRequest{Data: payload}); err != nil {
			break
		}
	}
	if _, err := wc.CloseAndRecv(); err != nil {
		t.Fatalf("terminal status through broker = %v, want the upstream's success response", err)
	}
}

func TestServeProxiesUnaryAndStreamingRPCsAndExpires(t *testing.T) {
	dir := shortTempDir(t)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	upstream, cleanup := startUpstream(t)
	defer cleanup()
	spec := Spec{
		Key:             "orin.local",
		Host:            "orin.local",
		Addr:            "127.0.0.1:50052",
		CertFingerprint: "public-cert-hash",
		Expected:        certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, dir, spec, upstream, 80*time.Millisecond) }()

	socketPath, statePath := paths(dir, spec.Key, spec.Expected)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(statePath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("broker state did not appear")
		}
		time.Sleep(time.Millisecond)
	}
	for _, path := range []string{socketPath, statePath} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("%s mode = %o, want owner-only", path, got)
		}
	}

	proxy, err := grpcclient.ConnectSessionProxy(context.Background(), socketPath, spec.Host, spec.Addr, &config.CertificateInfo{OrganizationID: 7}, spec.Expected)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := proxy.ObservedServerIdentity(); !ok || got != spec.Expected {
		t.Fatalf("proxy verified identity = (%v, %v), want %v", got, ok, spec.Expected)
	}
	if !proxy.IsMTLS || !proxy.IsSessionProxy || proxy.Host != spec.Host || proxy.Addr != spec.Addr {
		t.Fatalf("proxy metadata not retained: %+v", proxy)
	}
	resp, err := proxy.AgentService.GetAgentVersion(context.Background(), &agentpb.GetAgentVersionRequest{})
	if err != nil {
		t.Fatalf("unary RPC through broker: %v", err)
	}
	if resp.GetVersion() != "broker-test" {
		t.Fatalf("version = %q", resp.GetVersion())
	}
	// A real run can spend minutes building locally between device RPCs. The
	// broker must not apply its idle TTL while that invocation still owns the
	// local channel.
	time.Sleep(100 * time.Millisecond)
	if _, err := proxy.AgentService.GetAgentVersion(context.Background(), &agentpb.GetAgentVersionRequest{}); err != nil {
		t.Fatalf("RPC after an idle interval on an open client: %v", err)
	}
	stream, err := proxy.ContainerService.RunContainer(context.Background(), &agentpb.RunContainerLayersRequest{})
	if err != nil {
		t.Fatalf("open streaming RPC: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first stream response: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("second stream response: %v", err)
	}
	proxy.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker did not exit after idle TTL")
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket remains after broker exit: %v", err)
	}
}

func TestConnectFallsBackForDeadBroker(t *testing.T) {
	root := shortTempDir(t)
	dir := filepath.Join(root, "sessions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	expected := certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"}
	cert := config.CertificateInfo{PemCertificate: "public cert", OrganizationID: 7}
	spec := Spec{Key: "orin.local", Host: "orin.local", Addr: "127.0.0.1:50052", CertFingerprint: certificateFingerprint(cert), Expected: expected}
	socketPath, statePath := paths(dir, spec.Key, expected)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	lis.Close() // leave a correctly-owned but dead socket inode behind
	state, _ := json.Marshal(stateFile{Spec: spec})
	if err := os.WriteFile(statePath, state, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDir, oldLoad := configDir, loadConfig
	configDir = func() (string, error) { return root, nil }
	loadConfig = func() (*config.Config, error) {
		return &config.Config{Auth: []config.AuthConfig{{Certificates: []config.CertificateInfo{cert}}}}, nil
	}
	t.Cleanup(func() { configDir, loadConfig = oldDir, oldLoad })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	conn, err := Connect(ctx, spec.Key, expected)
	if conn != nil {
		conn.Close()
		t.Fatal("dead broker returned a connection")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestPreparedSessionScopeIsExactPinnedDirectMTLS(t *testing.T) {
	id := certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"}
	newConn := func(t *testing.T) *grpcclient.AgentConnection {
		t.Helper()
		conn, err := grpcclient.ConnectSessionProxy(context.Background(), filepath.Join(shortTempDir(t), "unused.sock"), "orin.local", "orin.local:50052", &config.CertificateInfo{OrganizationID: 7, PemCertificate: "cert"}, id)
		if err != nil {
			t.Fatal(err)
		}
		conn.IsSessionProxy = false
		t.Cleanup(func() { conn.Close() })
		return conn
	}

	if spec, ok := specForConnection("orin.local", newConn(t)); !ok || spec.Expected != id || spec.ParentPID != os.Getpid() {
		t.Fatalf("verified direct mTLS connection was not eligible: (%+v, %v)", spec, ok)
	}
	for name, mutate := range map[string]func(*grpcclient.AgentConnection){
		"plaintext": func(conn *grpcclient.AgentConnection) { conn.IsMTLS = false },
		"proxy":     func(conn *grpcclient.AgentConnection) { conn.IsSessionProxy = true },
		"cloud reconnect": func(conn *grpcclient.AgentConnection) {
			conn.Reconnect = func(context.Context) (*grpcclient.AgentConnection, error) { return nil, nil }
		},
		"tunnel registry dialer": func(conn *grpcclient.AgentConnection) {
			conn.RegistryDialer = func(context.Context, int) (net.Conn, error) { return nil, errors.New("unused") }
		},
		"credential identity mismatch": func(conn *grpcclient.AgentConnection) {
			conn.CertInfo.OrganizationID = 8
		},
	} {
		t.Run(name, func(t *testing.T) {
			conn := newConn(t)
			mutate(conn)
			if spec, ok := specForConnection("orin.local", conn); ok {
				t.Fatalf("out-of-scope connection was eligible: %+v", spec)
			}
		})
	}
}

func TestFindCertificateAlsoBindsOrganization(t *testing.T) {
	cert := config.CertificateInfo{OrganizationID: 7, PemCertificate: "same-public-cert"}
	wrongOrg := cert
	wrongOrg.OrganizationID = 8
	oldLoad := loadConfig
	loadConfig = func() (*config.Config, error) {
		return &config.Config{Auth: []config.AuthConfig{{Certificates: []config.CertificateInfo{wrongOrg, cert}}}}, nil
	}
	t.Cleanup(func() { loadConfig = oldLoad })

	got, err := findCertificate(certificateFingerprint(cert), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.OrganizationID != 7 {
		t.Fatalf("certificate org = %d, want 7", got.OrganizationID)
	}
	if _, err := findCertificate(certificateFingerprint(cert), 9); err == nil {
		t.Fatal("same certificate fingerprint from a different organization must not match")
	}
}

func TestServeExitsPromptlyWhenRetainedTransportDies(t *testing.T) {
	dir := shortTempDir(t)
	upstream, stopUpstream := startUpstream(t)
	defer stopUpstream()
	spec := Spec{
		Key:             "stale.local",
		Host:            "stale.local",
		Addr:            "127.0.0.1:50052",
		CertFingerprint: "public-cert-hash",
		Expected:        certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "44"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, dir, spec, upstream, time.Hour) }()

	socketPath, statePath := paths(dir, spec.Key, spec.Expected)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(statePath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("broker state did not appear")
		}
		time.Sleep(time.Millisecond)
	}
	proxy, err := grpcclient.ConnectSessionProxy(context.Background(), socketPath, spec.Host, spec.Addr, &config.CertificateInfo{OrganizationID: 7}, spec.Expected)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	stopUpstream()
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	_, err = proxy.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
	probeCancel()
	if err == nil {
		t.Fatal("probe unexpectedly succeeded after retained upstream was closed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker kept its identity lock after the retained transport failed")
	}
}

func TestServeKeepsPreparedBrokerWhileParentInvocationLives(t *testing.T) {
	dir := shortTempDir(t)
	upstream, cleanup := startUpstream(t)
	defer cleanup()
	spec := Spec{
		Key:             "parent-lease.local",
		Host:            "parent-lease.local",
		Addr:            "127.0.0.1:50052",
		CertFingerprint: "public-cert-hash",
		Expected:        certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "43"},
		ParentPID:       os.Getpid(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, dir, spec, upstream, 30*time.Millisecond) }()

	_, statePath := paths(dir, spec.Key, spec.Expected)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(statePath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("broker state did not appear")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("broker expired while preparing parent was alive: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker did not stop on cancellation")
	}
}

// blockingAgentServer simulates a black-holed device link at the RPC level:
// the transport stays connected (and Ready from the broker's point of view)
// while GetAgentVersion never answers within any client's budget.
type blockingAgentServer struct {
	agentpb.UnimplementedWendyAgentServiceServer
}

func (blockingAgentServer) GetAgentVersion(ctx context.Context, _ *agentpb.GetAgentVersionRequest) (*agentpb.GetAgentVersionResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestServeEvictsBrokerThatStopsAnswering(t *testing.T) {
	dir := shortTempDir(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentpb.RegisterWendyAgentServiceServer(server, blockingAgentServer{})
	go server.Serve(lis) //nolint:errcheck
	t.Cleanup(func() { server.Stop(); lis.Close() })
	upstream, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })

	spec := Spec{
		Key:             "blackhole.local",
		Host:            "blackhole.local",
		Addr:            "127.0.0.1:50052",
		CertFingerprint: "public-cert-hash",
		Expected:        certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "47"},
		ParentPID:       os.Getpid(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A live parent plus stats-event touches used to renew the zombie's TTL off
	// its own failed probes indefinitely; the hour-long TTL proves the exit
	// below comes from failure eviction, not idleness.
	socketPath, _, done := startServing(t, ctx, dir, spec, upstream, time.Hour)

	proxy, err := grpcclient.ConnectSessionProxy(context.Background(), socketPath, spec.Host, spec.Addr, &config.CertificateInfo{OrganizationID: 7}, spec.Expected)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	for i := 0; i < 3; i++ {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := proxy.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
		probeCancel()
		if err == nil {
			t.Fatal("probe against a black-holed upstream unexpectedly succeeded")
		}
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker kept serving (and holding its identity lock) after repeatedly failing to answer")
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket remains after eviction: %v", err)
	}
}

func TestServeExitsWhenRetainedTransportLeavesReadyWithoutTraffic(t *testing.T) {
	dir := shortTempDir(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentpb.RegisterWendyAgentServiceServer(server, testAgentServer{})
	go server.Serve(lis) //nolint:errcheck
	upstream, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		server.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	// Establish the transport so the broker starts with a Ready upstream, the
	// state Run() hands serve() after its verification probe.
	if _, err := agentpb.NewWendyAgentServiceClient(upstream).GetAgentVersion(context.Background(), &agentpb.GetAgentVersionRequest{}); err != nil {
		server.Stop()
		t.Fatal(err)
	}

	spec := Spec{
		Key:             "stateloss.local",
		Host:            "stateloss.local",
		Addr:            "127.0.0.1:50052",
		CertFingerprint: "public-cert-hash",
		Expected:        certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "48"},
		ParentPID:       os.Getpid(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, done := startServing(t, ctx, dir, spec, upstream, time.Hour)

	// Kill only the server: the client conn object stays open but its transport
	// drops, so any later hit would trigger a re-dial verified against this
	// process's startup pin snapshot — the stale-trust window the broker must
	// never serve. No RPC is made: the broker has to notice on its own.
	server.Stop()
	lis.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker kept its identity lock after the retained transport left Ready")
	}
}

func TestConnectRejectsNonPrivateSessionDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir := configDir
	configDir = func() (string, error) { return root, nil }
	t.Cleanup(func() { configDir = oldDir })

	_, err := Connect(context.Background(), "orin.local", certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}
