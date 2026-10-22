package mcusource

import (
	"context"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// LiteMeshSources discovers WendyCom sensors using signed device presence and
// live routes. The directory has no sensor capability field, so port 5054 is
// only a candidate hint: discovery authenticates and fetches the sensor manifest
// before offering a source. No application catalogue or multicast relay is needed.
type LiteMeshSources struct {
	snapshot     func() localmesh.NodeSnapshot
	transportFor TransportFactory
	browseLAN    func(context.Context, time.Duration, func(models.LANDevice) bool) ([]models.LANDevice, error)
	resolveLAN   func(context.Context, SensorPairing) ([]string, bool)
	probes       chan struct{}
}

func NewLiteMeshSources(snapshot func() localmesh.NodeSnapshot, transportFor TransportFactory) *LiteMeshSources {
	return &LiteMeshSources{snapshot: snapshot, transportFor: transportFor,
		browseLAN: discovery.DiscoverSensorSources, resolveLAN: resolvePairingLANAddrs,
		probes: make(chan struct{}, 4)}
}

func (m *LiteMeshSources) candidates() []models.LANDevice {
	s := m.snapshot()
	if !s.Self.IsValid() {
		return nil
	}
	now := time.Now().UnixMilli()
	var out []models.LANDevice
	for _, manifest := range s.Devices {
		device := meshid.NewDevice(s.Self.Tenant(), manifest.Device)
		if device == s.Self || manifest.Withdraw || manifest.Expires <= now || manifest.AgentPort != 5054 ||
			manifest.Route == nil || !s.RouteAuthorizations[*manifest.Route] {
			continue
		}
		prefix := netip.PrefixFrom(device.MeshAddress(), 128)
		if manifest.Route.Prefix != prefix {
			continue
		}
		for _, r := range s.Routes {
			if r.Prefix != prefix || r.RouterID != manifest.Route.RouterID || r.Unreachable || r.Local {
				continue
			}
			out = append(out, models.LANDevice{ID: device.UUID().String(), DisplayName: manifest.Name,
				IPAddress: device.MeshAddress().String(), Port: int(manifest.AgentPort), IsMTLS: true,
				IsWendyDevice: true, WendyLite: true, TenantUUID: device.Tenant().String(), PrincipalURI: device.Principal()})
			break
		}
	}
	return out
}

func meshSourceAddress(d models.LANDevice) string {
	return net.JoinHostPort(d.IPAddress, strconv.Itoa(d.Port))
}

// Resolve keeps LAN preference, then adds a fresh routed endpoint. A failed LAN
// TLS handshake falls through to the mesh in the supervisor's address loop.
func (m *LiteMeshSources) Resolve(ctx context.Context, p SensorPairing) ([]string, bool) {
	addresses, _ := m.resolveLAN(ctx, p)
	if p.Transport == "wendycom" && p.SourcePrincipalURI != "" {
		for _, d := range m.candidates() {
			if d.PrincipalURI == p.SourcePrincipalURI {
				addr := meshSourceAddress(d)
				if !slices.Contains(addresses, addr) {
					addresses = append(addresses, addr)
				}
			}
		}
	}
	return addresses, len(addresses) > 0
}

// Browse shares the caller's scan budget across LAN discovery and bounded mesh
// probes. A broken LAN browse does not hide authenticated mesh sources.
func (m *LiteMeshSources) Browse(ctx context.Context, timeout time.Duration, exclude func(models.LANDevice) bool) ([]models.LANDevice, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lan, mesh []models.LANDevice
	var lanErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lan, lanErr = m.browseLAN(ctx, timeout, exclude)
	}()
	// Workers are bounded even when callers scan concurrently. Each probe closes
	// its connection without subscribing or disturbing an existing pairing.
	candidates := m.candidates()
	// A few slow/offline candidates must not consume every refresh's budget
	// ahead of the same later devices in the signed directory.
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	var mu sync.Mutex
	for range min(4, len(candidates)) {
		wg.Go(func() {
			for {
				if probeCtx.Err() != nil {
					return
				}
				mu.Lock()
				if len(candidates) == 0 {
					mu.Unlock()
					return
				}
				d := candidates[0]
				candidates = candidates[1:]
				mu.Unlock()
				if exclude != nil && exclude(d) {
					continue
				}
				select {
				case m.probes <- struct{}{}:
				case <-probeCtx.Done():
					return
				}
				ok := m.probe(probeCtx, d)
				<-m.probes
				if ok {
					d.Sensorlink, d.Caps = true, []string{"sensors"}
					mu.Lock()
					mesh = append(mesh, d)
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	// Prefer the authenticated mesh entry when LAN and mesh name the same
	// principal. An unsigned LAN hint must not shadow a verified source.
	for _, d := range mesh {
		lan = slices.DeleteFunc(lan, func(l models.LANDevice) bool { return l.PrincipalURI == d.PrincipalURI })
		lan = append(lan, d)
	}
	if ctx.Err() != nil {
		return lan, ctx.Err()
	}
	if len(lan) != 0 {
		return lan, nil
	}
	return nil, lanErr
}

func (m *LiteMeshSources) probe(ctx context.Context, d models.LANDevice) bool {
	if ctx.Err() != nil {
		return false
	}
	p := SensorPairing{SourcePrincipalURI: d.PrincipalURI, Transport: "wendycom"}
	tr, err := m.transportFor(p, meshSourceAddress(d))
	if err != nil {
		return false
	}
	defer tr.Close()
	manifest, err := tr.FetchManifest(ctx)
	return err == nil && len(manifest.GetSensors()) != 0
}
