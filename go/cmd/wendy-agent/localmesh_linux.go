//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/bleprovider"
	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/agent/mesh"
	"github.com/wendylabsinc/wendy/go/internal/agent/meshsharing"
	"github.com/wendylabsinc/wendy/go/internal/agent/nanprovider"
	"go.uber.org/zap"
)

// runConfiguredNetCarriers runs one Babel node for every enabled carrier.
// Its observer is cleared only after the radio provider has drained its links.
func runConfiguredNetCarriers(ctx context.Context, configDir string, id localmesh.TCPIdentity, logger *zap.Logger, catalog *meshCatalogManager, vips *mesh.VIPTable, sharing *meshSharingManager, observe func(func() localmesh.NodeSnapshot)) (runErr error) {
	if err := sharing.Deactivate(); err != nil {
		return err
	}
	if err := errors.Join(meshsharing.CleanupStale(ctx), localmesh.CleanupStaleResolver(ctx)); err != nil {
		return fmt.Errorf("recover Wendy Net Internet sharing: %w", err)
	}
	path := filepath.Join(configDir, "local-mesh.json")
	initial, err := localmesh.ReadTCPConfigFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				current, err := localmesh.ReadTCPConfigFile(path)
				if err != nil || !bytes.Equal(current, initial) {
					cancelRun()
					return
				}
			}
		}
	}()
	defer func() { cancelRun(); <-watchDone }()
	var stopProviders context.CancelFunc
	var providersDone sync.WaitGroup
	defer func() { runErr = errors.Join(runErr, sharing.Deactivate()) }()
	return localmesh.RunConfiguredWithNode(runCtx, configDir, id, func(node *localmesh.Node, cfg *localmesh.TCPConfig) {
		if node == nil {
			_ = sharing.Deactivate()
			catalog.Deactivate()
			if stopProviders != nil {
				stopProviders()
				providersDone.Wait()
			}
			observe(nil)
			return
		}
		observe(node.Snapshot)
		if err := catalog.Activate(runCtx, node.Credentials, node.Snapshot, vips, node.ControlHub()); err != nil {
			logger.Error("net service catalog unavailable", zap.Error(err))
		}
		sharing.Activate(runCtx, node)
		if !cfg.NAN && !cfg.BLE {
			return
		}
		providerCtx, cancel := context.WithCancel(runCtx)
		stopProviders = cancel
		start := func(name string, run func(context.Context) error) {
			providersDone.Add(1)
			go func() {
				defer providersDone.Done()
				for providerCtx.Err() == nil {
					if err := run(providerCtx); err != nil && !errors.Is(err, context.Canceled) && providerCtx.Err() == nil {
						logger.Warn(name+" carrier stopped", zap.Error(err))
					}
					select {
					case <-providerCtx.Done():
						return
					case <-time.After(5 * time.Second):
					}
				}
			}()
		}
		if cfg.NAN {
			provider := nanprovider.Provider{Credentials: node.Credentials, Node: node, Logger: logger}
			start("NAN", provider.Run)
		}
		if cfg.BLE {
			ble := bleprovider.Config{Credentials: node.Credentials, Node: node, NetworkName: fmt.Sprintf("tenant:%s:default", node.Credentials.Device.Tenant()), Logger: logger}
			start("BLE", func(ctx context.Context) error { return bleprovider.Run(ctx, ble) })
		}
	})
}
