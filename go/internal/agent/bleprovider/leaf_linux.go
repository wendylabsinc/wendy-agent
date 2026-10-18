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
				conn, err := dialL2CAP(dialCtx, config.Address, config.AddressType, config.PSM)
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
