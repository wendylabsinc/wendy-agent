package services

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/meshsession"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestMeshAppDialerNeverUsesLegacyOrCloudFallback(t *testing.T) {
	d, probes := testDialer()
	peer := testMeshDevice(t)
	d.SetLocalMeshDialer(func(context.Context, meshid.Device, uint16) (net.Conn, error) {
		return nil, meshsession.ErrDenied
	})
	app := NewMeshAppDialer(d)
	if _, _, err := app.DialDevice(context.Background(), peer, 8080); !errors.Is(err, meshsession.ErrDenied) {
		t.Fatalf("denial = %v", err)
	}
	if probes.lookups != 0 || probes.lanDials != 0 || probes.brokerDials != 0 {
		t.Fatalf("app dial used legacy transport: %+v", probes)
	}
	d.SetLocalMeshDialer(func(context.Context, meshid.Device, uint16) (net.Conn, error) {
		conn, _ := net.Pipe()
		return conn, nil
	})
	conn, mode, err := app.DialDevice(context.Background(), peer, 8080)
	if err != nil || mode != "local-mesh" {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	_ = conn.Close()
	if probes.lookups != 0 || probes.lanDials != 0 || probes.brokerDials != 0 {
		t.Fatalf("app dial used legacy transport: %+v", probes)
	}
}

func TestMeshAppDialerRetriesTransientKernelRouteFailure(t *testing.T) {
	d, probes := testDialer()
	peer := testMeshDevice(t)
	attempts := 0
	d.SetLocalMeshDialer(func(context.Context, meshid.Device, uint16) (net.Conn, error) {
		attempts++
		if attempts == 1 {
			return nil, &net.OpError{Op: "write", Net: "udp", Err: syscall.EHOSTUNREACH}
		}
		conn, other := net.Pipe()
		_ = other.Close()
		return conn, nil
	})
	conn, mode, err := NewMeshAppDialer(d).DialDevice(context.Background(), peer, 8080)
	if err != nil || mode != "local-mesh" || attempts != 2 {
		t.Fatalf("mode=%q err=%v attempts=%d", mode, err, attempts)
	}
	_ = conn.Close()
	if probes.lookups != 0 || probes.lanDials != 0 || probes.brokerDials != 0 {
		t.Fatalf("app dial used legacy transport: %+v", probes)
	}
}

// testMeshDevice is the fixed device principal used by the mesh app-path
// tests.
func testMeshDevice(t *testing.T) meshid.Device {
	t.Helper()
	device, err := meshid.DeviceFromPrincipal("spiffe://wendy.sh/tenant/11111111-1111-4111-8111-111111111111/device/22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	return device
}
