//go:build !windows

package sessionbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

func startUpstream(t *testing.T) (*grpc.ClientConn, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
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
