package mcusource

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/wendylabsinc/WendyOS/go/babel"
	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/agent/ros2camera"
	"github.com/wendylabsinc/wendy/go/internal/agent/sensorlink"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	sensorlinkpb "github.com/wendylabsinc/wendy/go/proto/gen/sensorlinkpb"
	"go.uber.org/zap"
)

func sensorMeshFixture(t *testing.T) (localmesh.NodeSnapshot, SensorPairing) {
	t.Helper()
	tenant, err := meshid.ParseTenant("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	self := meshid.NewDevice(tenant, uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	peer := meshid.NewDevice(tenant, uuid.MustParse("33333333-3333-4333-8333-333333333333"))
	origin := localmesh.RouteOrigin{Prefix: netip.PrefixFrom(peer.MeshAddress(), 128), RouterID: 9, Seqno: 1}
	return localmesh.NodeSnapshot{Self: self, CapturedAt: time.Now(),
		Devices: []localmesh.Manifest{{Version: 3, Device: peer.UUID(), Name: "remote-lite", AgentPort: 5054,
			Expires: time.Now().Add(time.Minute).UnixMilli(), Route: &origin}},
		RouteAuthorizations: map[localmesh.RouteOrigin]bool{origin: true},
		Routes:              []babel.Route{{Prefix: origin.Prefix, RouterID: origin.RouterID, Seqno: origin.Seqno, Link: 2}},
	}, SensorPairing{SourcePrincipalURI: peer.Principal(), Transport: "wendycom"}
}

func TestMeshSensorResolutionRequiresCurrentAuthorizedRoute(t *testing.T) {
	cases := []struct {
		name   string
		change func(*localmesh.NodeSnapshot)
		want   bool
	}{
		{"multihop route", func(*localmesh.NodeSnapshot) {}, true},
		{"withdrawn", func(s *localmesh.NodeSnapshot) { s.Devices[0].Withdraw = true }, false},
		{"expired", func(s *localmesh.NodeSnapshot) { s.Devices[0].Expires = time.Now().Add(-time.Second).UnixMilli() }, false},
		{"route lost", func(s *localmesh.NodeSnapshot) { s.Routes = nil }, false},
		{"unreachable", func(s *localmesh.NodeSnapshot) { s.Routes[0].Unreachable = true }, false},
		{"wrong route origin", func(s *localmesh.NodeSnapshot) { s.Routes[0].RouterID++ }, false},
		{"unauthorized", func(s *localmesh.NodeSnapshot) { s.RouteAuthorizations = nil }, false},
		{"other service", func(s *localmesh.NodeSnapshot) { s.Devices[0].AgentPort = 50052 }, false},
		{"self", func(s *localmesh.NodeSnapshot) { s.Devices[0].Device = s.Self.UUID() }, false},
		{"not enrolled", func(s *localmesh.NodeSnapshot) { s.Self = meshid.Device{} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, p := sensorMeshFixture(t)
			tc.change(&snapshot)
			m := NewLiteMeshSources(func() localmesh.NodeSnapshot { return snapshot }, nil)
			m.resolveLAN = func(context.Context, SensorPairing) ([]string, bool) { return nil, false }
			got, ok := m.Resolve(context.Background(), p)
			if ok != tc.want || len(got) != boolInt(tc.want) {
				t.Fatalf("got %v, %v", got, ok)
			}
		})
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestMeshSensorResolutionLANPreferenceAndRecovery(t *testing.T) {
	s, p := sensorMeshFixture(t)
	m := NewLiteMeshSources(func() localmesh.NodeSnapshot { return s }, nil)
	m.resolveLAN = func(context.Context, SensorPairing) ([]string, bool) { return []string{"192.0.2.1:5054"}, true }
	got, ok := m.Resolve(context.Background(), p)
	if !ok || len(got) != 2 || got[0] != "192.0.2.1:5054" {
		t.Fatalf("got %v", got)
	}
	mesh := got[1]
	// The next resolution must observe a partition rather than reuse a cached IP.
	s.Routes[0].Unreachable = true
	got, _ = m.Resolve(context.Background(), p)
	if len(got) != 1 {
		t.Fatalf("kept dead route: %v", got)
	}
	s.Routes[0].Unreachable = false
	m.resolveLAN = func(ctx context.Context, _ SensorPairing) ([]string, bool) { <-ctx.Done(); return nil, false }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, ok = m.Resolve(ctx, p) // LAN browse budget can expire before mesh lookup.
	if !ok || !slices.Equal(got, []string{mesh}) {
		t.Fatalf("lost routed fallback: %v", got)
	}
	p.SourcePrincipalURI = "spiffe://wendy.sh/tenant/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/device/33333333-3333-4333-8333-333333333333"
	if got, ok = m.Resolve(ctx, p); ok {
		t.Fatalf("cross-tenant fallback: %v", got)
	}
	p = SensorPairing{SourceAssetID: 1, Transport: "wendycom"}
	if got, ok = m.Resolve(ctx, p); ok {
		t.Fatalf("legacy identity fallback: %v", got)
	}
}

type meshProbeTransport struct {
	fetch  func(context.Context) (*sensorlinkpb.SensorManifest, error)
	closed atomic.Int32
}

func (p *meshProbeTransport) Close() error { p.closed.Add(1); return nil }
func (p *meshProbeTransport) FetchManifest(ctx context.Context) (*sensorlinkpb.SensorManifest, error) {
	return p.fetch(ctx)
}
func (*meshProbeTransport) Stream(context.Context, []uint32) (<-chan *sensorlink.SensorFrame, func() error, error) {
	panic("discovery must never subscribe")
}

func TestMeshSensorBrowseAuthenticatesAndDeduplicates(t *testing.T) {
	s, p := sensorMeshFixture(t)
	tr := &meshProbeTransport{fetch: func(context.Context) (*sensorlinkpb.SensorManifest, error) {
		return &sensorlinkpb.SensorManifest{Sensors: []*sensorlinkpb.SensorDescriptor{{Name: "camera"}}}, nil
	}}
	m := NewLiteMeshSources(func() localmesh.NodeSnapshot { return s }, func(pair SensorPairing, addr string) (SensorTransport, error) {
		if pair.SourcePrincipalURI != p.SourcePrincipalURI || pair.Transport != "wendycom" || addr != "[fd45:b891:567f:4333:8333:3333:3333:3333]:5054" {
			t.Errorf("incorrect authenticated target: %+v %s", pair, addr)
		}
		return tr, nil
	})
	m.browseLAN = func(context.Context, time.Duration, func(models.LANDevice) bool) ([]models.LANDevice, error) {
		return []models.LANDevice{{PrincipalURI: p.SourcePrincipalURI, DisplayName: "unsigned hint"}, {ID: "legacy", Sensorlink: true}}, nil
	}
	got, err := m.Browse(context.Background(), time.Second, nil)
	if err != nil || len(got) != 2 || got[0].ID != "legacy" || got[1].DisplayName != "remote-lite" || !got[1].Sensorlink || !got[1].IsMTLS || !got[1].WendyLite {
		t.Fatalf("got %+v, %v", got, err)
	}
	if tr.closed.Load() != 1 {
		t.Fatal("probe connection leaked")
	}
	m.browseLAN = func(context.Context, time.Duration, func(models.LANDevice) bool) ([]models.LANDevice, error) {
		return nil, errors.New("mdns unavailable")
	}
	got, err = m.Browse(context.Background(), time.Second, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("LAN error hid mesh source: %v %v", got, err)
	}
	tr.fetch = func(context.Context) (*sensorlinkpb.SensorManifest, error) {
		return nil, errors.New("wrong peer certificate")
	}
	got, err = m.Browse(context.Background(), time.Second, nil)
	if len(got) != 0 || err == nil {
		t.Fatalf("unauthenticated source: %v %v", got, err)
	}
	tr.fetch = func(context.Context) (*sensorlinkpb.SensorManifest, error) {
		return &sensorlinkpb.SensorManifest{}, nil
	}
	got, _ = m.Browse(context.Background(), time.Second, nil)
	if len(got) != 0 {
		t.Fatal("offered source with no sensors")
	}
}

func TestMeshSensorProbesBoundedAcrossConcurrentScans(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := sensorMeshFixture(t)
		// Each entry has its own authenticated UUID and route.
		for i := 0; i < 7; i++ {
			d := s.Devices[0]
			d.Device[15] = byte(i)
			peer := meshid.NewDevice(s.Self.Tenant(), d.Device)
			origin := *d.Route
			origin.Prefix = netip.PrefixFrom(peer.MeshAddress(), 128)
			d.Route = &origin
			s.Devices = append(s.Devices, d)
			s.RouteAuthorizations[origin] = true
			r := s.Routes[0]
			r.Prefix = origin.Prefix
			s.Routes = append(s.Routes, r)
		}
		var active, opened, closed atomic.Int32
		m := NewLiteMeshSources(func() localmesh.NodeSnapshot { return s }, func(SensorPairing, string) (SensorTransport, error) {
			opened.Add(1)
			return &meshProbeTransport{fetch: func(ctx context.Context) (*sensorlinkpb.SensorManifest, error) {
				if active.Add(1) > 4 {
					t.Error("too many concurrent probes")
				}
				defer active.Add(-1)
				defer closed.Add(1)
				<-ctx.Done()
				return nil, ctx.Err()
			}}, nil
		})
		m.browseLAN = func(context.Context, time.Duration, func(models.LANDevice) bool) ([]models.LANDevice, error) {
			return nil, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = m.Browse(ctx, time.Minute, nil) })
		wg.Go(func() { _, _ = m.Browse(ctx, time.Minute, nil) })
		synctest.Wait()
		if active.Load() != 4 {
			t.Fatalf("active %d", active.Load())
		}
		cancel()
		wg.Wait()
		if active.Load() != 0 || opened.Load() != closed.Load() {
			t.Fatal("probe cancellation leaked work")
		}
	})
}

func TestSensorSupervisorFallsBackAndReresolves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var attempts []string
		resolutions := 0
		sup := NewSupervisor(zap.NewNop(), statusLoopback{}, func(_ SensorPairing, addr string) (SensorTransport, error) {
			attempts = append(attempts, addr)
			if addr == "mesh-after-reconnect" {
				cancel()
			}
			return nil, errors.New("connection or authentication failed")
		}, func(string) ros2camera.CameraWriter { return statusWriter{} }, nil)
		sup.SetAddressResolver(func(context.Context, SensorPairing) ([]string, bool) {
			resolutions++
			if resolutions == 1 {
				return []string{"lan", "mesh"}, true
			}
			return []string{"mesh-after-reconnect"}, true
		})
		_ = sup.RunPairing(ctx, SensorPairing{SourceAssetID: 1}, "")
		if !slices.Equal(attempts, []string{"lan", "mesh", "mesh-after-reconnect"}) {
			t.Fatalf("attempts %v", attempts)
		}
	})
}

func TestSensorSupervisorStreamsAfterLANAuthenticationFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, p := sensorMeshFixture(t)
		tr := &statusTransport{frames: make(chan *sensorlink.SensorFrame)}
		var attempts []string
		sup := NewSupervisor(zap.NewNop(), statusLoopback{}, func(_ SensorPairing, addr string) (SensorTransport, error) {
			attempts = append(attempts, addr)
			if addr == "lan" {
				return nil, errors.New("wrong source certificate")
			}
			return tr, nil
		}, func(string) ros2camera.CameraWriter { return statusWriter{} }, nil)
		sup.SetAddressResolver(func(context.Context, SensorPairing) ([]string, bool) { return []string{"lan", "mesh"}, true })
		done := make(chan struct{})
		go func() { defer close(done); _ = sup.RunPairing(ctx, p, "") }()
		synctest.Wait()
		if sup.IsPairingConnected(p) {
			t.Fatal("connected before receiving data")
		}
		tr.frames <- &sensorlink.SensorFrame{ChannelID: 1}
		tr.frames <- &sensorlink.SensorFrame{ChannelID: 1}
		if !sup.IsPairingConnected(p) {
			t.Fatal("fallback did not deliver frames")
		}
		cancel()
		close(tr.frames) // Real transports close their stream when ctx is cancelled.
		<-done
		if sup.IsPairingConnected(p) {
			t.Fatal("connected after cancellation")
		}
		if !slices.Equal(attempts, []string{"lan", "mesh"}) {
			t.Fatalf("unexpected attempts %v", attempts)
		}
	})
}
