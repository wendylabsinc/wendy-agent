//go:build linux

package bleprovider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"go.uber.org/zap"
)

// RunLeaves dials only configured BLE endpoints, with one worker per device.
// Signed leaf streams use a separate PSM and ALPN from router-to-router BLE.
func RunLeaves(ctx context.Context, node *localmesh.Node, peers []localmesh.LeafBLEPeer,
	timeURL string, logger *zap.Logger, status func(localmesh.CarrierStatus)) error {
	if len(peers) == 0 || len(peers) > 8 {
		return fmt.Errorf("configure 1..8 BLE leaves")
	}
	local, err := leafAdapterAddress(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	for _, config := range peers {
		peer, err := meshid.ParseDevice(node.Credentials.Device.Tenant(), config.Device)
		if err != nil {
			return err
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				conn, err := dialL2CAP(dialCtx, local, config.Address, config.AddressType, config.PSM)
				cancel()
				if err == nil {
					err = node.ServeLeafRadio(ctx, peer, conn, timeURL)
					_ = conn.Close()
				}
				if ctx.Err() != nil {
					return
				}
				if logger != nil {
					logger.Warn("BLE leaf disconnected", zap.String("device", config.Device), zap.Error(err))
				}
				timer := time.NewTimer(5 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	// Ready means the configured dial workers are running, not that peers have
	// authenticated. Live links remain visible in the node snapshot.
	if status != nil {
		status(localmesh.CarrierStatus{Ready: true})
	}
	<-ctx.Done()
	return nil
}

// Leaf sockets also bind a concrete local controller. The leaf PSM is a remote
// endpoint; the local dynamic-PSM bind uses the selected adapter's identity.
func leafAdapterAddress(ctx context.Context) (l2addr, error) {
	query, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	bus, closeTransport, err := connectProviderBus(query)
	if err != nil {
		return l2addr{}, err
	}
	stop := guardProviderBusShutdown(query, 0, closeTransport)
	defer func() { _ = closeTransport(); _ = bus.Close(); stop() }()
	adapter, err := selectAdapter(query, bus, "")
	if err != nil {
		return l2addr{}, err
	}
	return adapterL2Address(query, bus, adapter, DefaultPSM)
}
