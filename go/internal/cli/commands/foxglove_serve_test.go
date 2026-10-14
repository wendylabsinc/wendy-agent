package commands

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

type foxgloveSelectedClient struct {
	agentpb.WendyContainerServiceClient
	deleted *agentpb.DeleteContainerRequest
	execs   int
	cancel  context.CancelFunc
}

func (c *foxgloveSelectedClient) DeleteContainer(_ context.Context, req *agentpb.DeleteContainerRequest, _ ...grpc.CallOption) (*agentpb.DeleteContainerResponse, error) {
	c.deleted = req
	return &agentpb.DeleteContainerResponse{}, nil
}

func (c *foxgloveSelectedClient) ExecContainer(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[agentpb.ExecContainerRequest, agentpb.ExecContainerResponse], error) {
	c.execs++
	c.cancel()
	return nil, status.Error(codes.Canceled, "test stopped after forwarding used selected agent")
}

func foxgloveTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func TestFoxgloveServeUsesOneAgentForDeployAndForward(t *testing.T) {
	for _, selector := range []string{"", "192.168.100.126", "vm:robot", "cloud://cloud.example:443/org/2/asset/42"} {
		t.Run(selector, func(t *testing.T) {
			oldResolve, oldDeploy := resolveFoxgloveTarget, deployFoxgloveApp
			t.Cleanup(func() { resolveFoxgloveTarget, deployFoxgloveApp = oldResolve, oldDeploy })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &foxgloveSelectedClient{cancel: cancel}
			conn := &grpcclient.AgentConnection{ContainerService: client}
			resolutions := 0
			resolveFoxgloveTarget = func(_ context.Context, device string) (*SelectedDevice, error) {
				resolutions++
				if device != selector {
					t.Fatalf("selector = %q, want %q", device, selector)
				}
				return &SelectedDevice{Agent: conn}, nil
			}
			deployed := false
			var appDir string
			deployFoxgloveApp = func(_ context.Context, got *grpcclient.AgentConnection, dir string, cfg *appconfig.AppConfig) error {
				deployed = true
				appDir = dir
				if got != conn {
					t.Fatal("deployment used a different agent")
				}
				if cfg.AppID != foxgloveAppID || cfg.Services["foxglove"] == nil {
					t.Fatalf("wrong bridge config: %+v", cfg)
				}
				data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), "      socat \\") {
					t.Fatal("generated bridge must install its forwarding dependency")
				}
				return nil
			}
			err := foxgloveServe(ctx, foxgloveServeOpts{device: selector, localPort: foxgloveTestPort(t), distro: "humble", rmw: "rmw_cyclonedds_cpp"})
			if err != nil {
				t.Fatal(err)
			}
			if resolutions != 1 || !deployed || client.execs != 1 {
				t.Fatalf("resolve=%d deploy=%v exec=%d", resolutions, deployed, client.execs)
			}
			if client.deleted == nil || client.deleted.AppName != foxgloveAppID || !client.deleted.DeleteImage || client.deleted.DeleteVolumes {
				t.Fatalf("unexpected cleanup: %v", client.deleted)
			}
			if _, err := os.Stat(appDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary app was not removed: %v", err)
			}
		})
	}
}

func TestFoxgloveServeRejectsLocalPortBeforeDeviceChanges(t *testing.T) {
	oldResolve := resolveFoxgloveTarget
	t.Cleanup(func() { resolveFoxgloveTarget = oldResolve })
	resolveFoxgloveTarget = func(context.Context, string) (*SelectedDevice, error) {
		t.Fatal("must reject unusable local ports before contacting a device")
		return nil, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, port := range []int{-1, 0, 65536, listener.Addr().(*net.TCPAddr).Port} {
		err := foxgloveServe(context.Background(), foxgloveServeOpts{localPort: port, distro: "humble", rmw: "rmw_cyclonedds_cpp"})
		if err == nil {
			t.Fatalf("port %d: wanted error", port)
		}
	}
}
