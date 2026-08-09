package services

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// stubBuildStream is a fake BuildImage server stream for unit tests.
type stubBuildStream struct {
	agentpbv2.WendyBuildService_BuildImageServer
	spec  *agentpbv2.BuildSpec
	sent  []*agentpbv2.BuildImageProgress
	recvd bool
}

func (s *stubBuildStream) Context() context.Context { return context.Background() }

func (s *stubBuildStream) Recv() (*agentpbv2.BuildImageRequest, error) {
	if s.recvd {
		return nil, io.EOF
	}
	s.recvd = true
	return &agentpbv2.BuildImageRequest{Spec: s.spec}, nil
}

func (s *stubBuildStream) Send(p *agentpbv2.BuildImageProgress) error {
	s.sent = append(s.sent, p)
	return nil
}

// enabledConfigDir returns a config dir with the builder opt-in marker present.
func enabledConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, buildHostEnabledFile), nil, 0o600); err != nil {
		t.Fatalf("writing opt-in marker: %v", err)
	}
	return dir
}

func TestGetBuildCapabilities_DisabledByDefault(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: t.TempDir()})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if resp.GetBuilderEnabled() {
		t.Fatal("a device must not report itself as a builder unless explicitly opted in: being reachable is not volunteering")
	}
}

func TestGetBuildCapabilities_EnabledByMarkerFile(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: enabledConfigDir(t)})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if !resp.GetBuilderEnabled() {
		t.Fatal("the opt-in marker file must enable the builder role")
	}
}

func TestGetBuildCapabilities_ReportsNoBuildkitWhenSocketAbsent(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath:      enabledConfigDir(t),
		BuildkitAddress: "unix:///nonexistent/buildkitd.sock",
	})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if resp.GetBuildkitAvailable() {
		t.Fatal("buildkit must not be reported available when its socket does not exist")
	}
	if len(resp.GetNativePlatforms()) != 0 {
		t.Fatal("a host with no buildkit must advertise no buildable platforms")
	}
}

func TestGetBuildCapabilities_ReportsPlatformsWhenBuildkitPresent(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "buildkitd.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatalf("creating fake socket: %v", err)
	}
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath:      enabledConfigDir(t),
		BuildkitAddress: "unix://" + sock,
	})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if !resp.GetBuildkitAvailable() {
		t.Fatal("buildkit must be reported available when its socket exists")
	}
	if len(resp.GetNativePlatforms()) == 0 {
		t.Fatal("a buildkit host must advertise at least its own platform")
	}
	if resp.GetOs() == "" || resp.GetCpuArchitecture() == "" {
		t.Fatal("os and architecture must be reported so the CLI can check the target platform")
	}
}

func TestBuildImage_RejectedWhenNotEnabled(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: t.TempDir()})

	err := svc.BuildImage(&stubBuildStream{
		spec: &agentpbv2.BuildSpec{AppId: "app", Platform: "linux/arm64"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v, want FailedPrecondition when the device is not a builder", err)
	}
}
