package mcusource

import (
	"context"
	"strconv"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/sensorlink"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// TestTransportSelectionAndResolve asserts resolveLANAddr picks the right
// port for each transport: "grpc" pairings (agent-hosted sources) dial the
// source's mTLS agent gRPC port — the discovered d.Port — while "tcp"/empty
// pairings (MCU raw-TCP sources) dial the well-known sensorlink.Port instead,
// since d.Port there is the agent's own gRPC port, not where the source's
// SensorPairing/sensorlink service actually listens.
func TestTransportSelectionAndResolve(t *testing.T) {
	orig := discoverFn
	t.Cleanup(func() { discoverFn = orig })

	discoverFn = func(_ context.Context, _ discovery.DiscoveryOptions) (*models.DevicesCollection, error) {
		return &models.DevicesCollection{
			LANDevices: []models.LANDevice{{
				AssetID:   42,
				IsMTLS:    true,
				IPAddress: "10.0.0.5",
				Port:      50051,
			}},
		}, nil
	}

	grpcAddr, ok := resolveLANAddr(context.Background(), 42, "grpc")
	if !ok || grpcAddr != "10.0.0.5:50051" {
		t.Fatalf("grpc resolve: got %q, ok=%v, want 10.0.0.5:50051", grpcAddr, ok)
	}

	wantTCP := "10.0.0.5:" + strconv.Itoa(sensorlink.Port)
	tcpAddr, ok := resolveLANAddr(context.Background(), 42, "tcp")
	if !ok || tcpAddr != wantTCP {
		t.Fatalf("tcp resolve: got %q, ok=%v, want %s", tcpAddr, ok, wantTCP)
	}

	emptyAddr, ok := resolveLANAddr(context.Background(), 42, "")
	if !ok || emptyAddr != wantTCP {
		t.Fatalf("empty-transport resolve: got %q, ok=%v, want %s", emptyAddr, ok, wantTCP)
	}
}
