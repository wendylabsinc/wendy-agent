package unitree

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
)

func TestUnitreeDiscoveryDatagrams(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		src  string
		want bool
	}{
		{"normal", `{"sn":"B42TEST","ip":"192.168.1.5"}`, "192.168.1.5", true},
		{"sender fallback", `{"sn":"B42TEST"}`, "192.168.1.5", true},
		{"foreign packet", `{"name":"something"}`, "192.168.1.5", false},
		{"broken JSON", `{`, "192.168.1.5", false},
		{"redirect", `{"sn":"B42TEST","ip":"192.168.1.8"}`, "192.168.1.5", false},
		{"invalid IP", `{"sn":"B42TEST","ip":"example.com"}`, "192.168.1.5", false},
		{"loopback", `{"sn":"B42TEST"}`, "127.0.0.1", false},
		{"multicast", `{"sn":"B42TEST"}`, "231.1.1.1", false},
		{"terminal escape", `{"sn":"\u001b[31m"}`, "192.168.1.5", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device, ok := deviceFromDatagram([]byte(tc.body), net.ParseIP(tc.src))
			if ok != tc.want {
				t.Fatalf("accepted=%v want %v", ok, tc.want)
			}
			if ok && (device.Serial != "B42TEST" || device.Address != tc.src) {
				t.Errorf("unexpected discovery identity: %+v", device)
			}
		})
	}
}

func TestMDNSOnlyMatchesKnownAccountSerial(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry discovery.MDNSService
		want  bool
	}{
		{"serial TXT", discovery.MDNSService{TXTRecords: map[string]string{"sn": "B42TEST"}}, true},
		{"serial hostname", discovery.MDNSService{Hostname: "go2-B42TEST.local."}, true},
		{"serial instance", discovery.MDNSService{InstanceName: "Unitree B42TEST"}, true},
		{"unrelated SSH", discovery.MDNSService{Hostname: "my-laptop.local"}, false},
		{"ambiguous Go2 name", discovery.MDNSService{Hostname: "go2.local"}, false},
		{"partial serial", discovery.MDNSService{Hostname: "go2-B42TEST999.local"}, false},
		{"unknown serial", discovery.MDNSService{TXTRecords: map[string]string{"serial": "OTHER"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.entry.IPAddress = "192.168.1.5"
			_, ok := deviceFromMDNS(tc.entry, []string{"B42TEST"})
			if ok != tc.want {
				t.Fatalf("accepted=%v want %v", ok, tc.want)
			}
		})
	}
}

func TestDiscoveryCombinesMDNSAndUnitreeMulticast(t *testing.T) {
	udp := func(context.Context, []string) ([]Device, error) {
		return []Device{{Serial: "B42TEST", Address: "192.168.1.5"}}, nil
	}
	browse := func(_ context.Context, service string, _ time.Duration) ([]discovery.MDNSService, error) {
		if service != "_ssh._tcp" && service != "_http._tcp" {
			t.Errorf("invented an unsupported Unitree DNS-SD service %s", service)
		}
		return []discovery.MDNSService{
			{IPAddress: "192.168.1.6", Hostname: "B42TEST.local"},
			{IPAddress: "192.168.1.7", TXTRecords: map[string]string{"sn": "B43TEST"}},
			{IPAddress: "192.168.1.8", Hostname: "not-a-robot.local"},
		}, nil
	}
	devices, err := discover(context.Background(), []string{"B42TEST", "B43TEST"}, udp, browse)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0].Serial != "B42TEST" || devices[0].Address != "192.168.1.5" || devices[1].Address != "192.168.1.7" {
		t.Fatalf("discovery did not merge and prefer Unitree UDP: %+v", devices)
	}
}

func TestDiscoveryCancellationAndMDNSFallback(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		udp := func(ctx context.Context, _ []string) ([]Device, error) { <-ctx.Done(); return nil, ctx.Err() }
		browse := func(ctx context.Context, _ string, _ time.Duration) ([]discovery.MDNSService, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		_, err := discover(ctx, []string{"B42TEST"}, udp, browse)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline error, got %v", err)
		}
	})
	t.Run("UDP unavailable", func(t *testing.T) {
		udp := func(context.Context, []string) ([]Device, error) { return nil, errors.New("address in use") }
		browse := func(context.Context, string, time.Duration) ([]discovery.MDNSService, error) {
			return []discovery.MDNSService{{IPAddress: "192.168.1.7", Hostname: "B42TEST.local"}}, nil
		}
		devices, err := discover(context.Background(), []string{"B42TEST"}, udp, browse)
		if err != nil || len(devices) != 1 {
			t.Fatalf("mDNS fallback failed: devices=%v error=%v", devices, err)
		}
	})
}
