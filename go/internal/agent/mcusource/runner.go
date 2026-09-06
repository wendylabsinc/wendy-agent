package mcusource

import (
	"context"
	"net"
	"strconv"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/agent/sensorlink"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"go.uber.org/zap"
)

// resolveLANAddr is a seam over discovery.Discover so tests can stub LAN
// resolution without a real mDNS browse.
//
// transport selects which port to dial: for "grpc" pairings (agent-hosted
// sources) the source is reached through its own mTLS agent gRPC port, i.e.
// the mDNS-advertised d.Port. For "tcp"/empty pairings (MCU raw-TCP sources)
// d.Port is NOT the right port — it's the agent's own gRPC port, not the
// sensorlink port the source's SensorPairing service listens on — so dial
// the well-known sensorlink.Port instead, agreeing with the address the CLI
// builds on `device pair`.
// discoverFn is a seam over discovery.Discover so resolveLANAddr's own
// transport→port selection logic can be exercised with a fake device list,
// without a real mDNS browse.
var discoverFn = discovery.Discover

var resolveLANAddr = func(ctx context.Context, sourceAssetID int32, transport string) (string, bool) {
	devices, err := discoverFn(ctx, discovery.DiscoveryOptions{})
	if err != nil {
		return "", false
	}
	for _, d := range devices.LANDevices {
		if d.AssetID == sourceAssetID && d.IsMTLS && d.IPAddress != "" {
			port := sensorlink.Port
			if transport == "grpc" {
				port = d.Port
			}
			return net.JoinHostPort(d.IPAddress, strconv.Itoa(port)), true
		}
	}
	return "", false
}

// Runner owns one cancelable goroutine per active pairing, all driven through
// the single shared Supervisor (its node-id allocator must stay shared across
// every pairing on the agent).
type Runner struct {
	logger *zap.Logger
	sup    *Supervisor

	mu      sync.Mutex
	cancels map[int32]context.CancelFunc
}

func NewRunner(logger *zap.Logger, sup *Supervisor) *Runner {
	return &Runner{logger: logger, sup: sup, cancels: make(map[int32]context.CancelFunc)}
}

// Start (re)launches the supervisor goroutine for p. If a goroutine is
// already running for this source asset id, it is stopped first. addr == ""
// means the source's LAN address is resolved by asset id (used on boot-resume
// and the common `device pair` path, where the pairing store has no address
// on file); RunPairing then owns that resolution and RE-resolves on every
// reconnect so a source that changes IP is still found. A non-empty addr is a
// pinned target RunPairing reuses unchanged.
func (r *Runner) Start(p SensorPairing, addr string) {
	r.Stop(p.SourceAssetID)

	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancels[p.SourceAssetID] = cancel
	r.mu.Unlock()

	go func() {
		if err := r.sup.RunPairing(ctx, p, addr); err != nil && ctx.Err() == nil {
			r.logger.Warn("sensor pairing supervisor exited", zap.Int32("source", p.SourceAssetID), zap.Error(err))
		}
	}()
}

// IsRunning reports whether a supervisor goroutine is currently active for
// sourceAssetID.
func (r *Runner) IsRunning(sourceAssetID int32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.cancels[sourceAssetID]
	return ok
}

// Stop cancels the running supervisor goroutine for sourceAssetID, if any.
func (r *Runner) Stop(sourceAssetID int32) {
	r.mu.Lock()
	cancel, ok := r.cancels[sourceAssetID]
	delete(r.cancels, sourceAssetID)
	r.mu.Unlock()
	if ok {
		cancel()
	}
}
