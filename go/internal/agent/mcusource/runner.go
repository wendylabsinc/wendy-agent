package mcusource

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"go.uber.org/zap"
)

// resolveLANAddr is a seam over discovery.Discover so tests can stub LAN
// resolution without a real mDNS browse.
var resolveLANAddr = func(ctx context.Context, sourceAssetID int32) (string, bool) {
	devices, err := discovery.Discover(ctx, discovery.DiscoveryOptions{})
	if err != nil {
		return "", false
	}
	for _, d := range devices.LANDevices {
		if d.AssetID == sourceAssetID && d.IsMTLS && d.IPAddress != "" {
			return net.JoinHostPort(d.IPAddress, strconv.Itoa(d.Port)), true
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
// means resolve the source's LAN address by asset id (used on boot-resume,
// where the pairing store has no address on file) before RunPairing takes
// over reconnect/backoff for good.
func (r *Runner) Start(p SensorPairing, addr string) {
	r.Stop(p.SourceAssetID)

	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancels[p.SourceAssetID] = cancel
	r.mu.Unlock()

	go func() {
		a, ok := r.resolveAddr(ctx, p, addr)
		if !ok {
			return // ctx was cancelled (Stop/re-Start) while still resolving
		}
		if err := r.sup.RunPairing(ctx, p, a); err != nil && ctx.Err() == nil {
			r.logger.Warn("sensor pairing supervisor exited", zap.Int32("source", p.SourceAssetID), zap.Error(err))
		}
	}()
}

// resolveAddr returns addr unchanged when non-empty; otherwise it repeatedly
// browses the LAN for sourceAssetID (boot-resume has no address on file)
// until it finds one or ctx is cancelled. ok is false only when ctx ended
// first.
func (r *Runner) resolveAddr(ctx context.Context, p SensorPairing, addr string) (string, bool) {
	if addr != "" {
		return addr, true
	}
	level := 0
	for {
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		resolved, ok := resolveLANAddr(rctx, p.SourceAssetID)
		rcancel()
		if ok {
			return resolved, true
		}
		r.logger.Warn("sensor source not found on LAN, retrying", zap.Int32("source", p.SourceAssetID))
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(backoffDelay(level)):
		}
		if level < 5 {
			level++
		}
	}
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
