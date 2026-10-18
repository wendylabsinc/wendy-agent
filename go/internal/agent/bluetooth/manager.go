// Package bluetooth provides Bluetooth peripheral management using BlueZ D-Bus on Linux.
// On non-Linux platforms, a stub implementation is returned.
package bluetooth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"

	"go.uber.org/zap"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// Manager defines the interface for Bluetooth operations.
type Manager interface {
	Scan(ctx context.Context) (<-chan []*agentpb.DiscoveredBluetoothPeripheral, error)
	// Connect connects to the peripheral and reports whether it is paired
	// once the connection is established — a successful connect does not
	// imply pairing (some BLE devices reject pairing yet accept connections).
	Connect(ctx context.Context, address string, pair, trust bool) (paired bool, err error)
	Disconnect(ctx context.Context, address string) error
	Forget(ctx context.Context, address string) error
	// ReconnectTrusted restores connections to trusted audio peripherals that
	// were paired before the agent restarted. It blocks until it has filled
	// what it can, so callers run it in the background.
	ReconnectTrusted(ctx context.Context)
}

// ManagerOption configures NewManager.
type ManagerOption func(*managerOptions)

type managerOptions struct {
	links LinkReporter
}

// WithLinkReporter makes Scan report each connected peripheral's BLE link
// supervision timeout.
func WithLinkReporter(r LinkReporter) ManagerOption {
	return func(o *managerOptions) { o.links = r }
}

func NewManager(logger *zap.Logger, opts ...ManagerOption) Manager {
	var o managerOptions
	for _, opt := range opts {
		opt(&o)
	}
	return newPlatformManager(logger, o)
}

// Peripheral owns one enrolled BLE endpoint and all its sessions. Close must
// finish before starting a replacement with rotated certificates.
type Peripheral struct {
	once              sync.Once
	cancel            context.CancelFunc
	stopAdvertisement func() error
	stopServer        func() error
	err               error
}

func (p *Peripheral) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		p.cancel()
		p.err = errors.Join(p.stopAdvertisement(), p.stopServer())
	})
	return p.err
}

func peripheralInfo(cfg *tls.Config, expected certs.Scope) (agentinfo.Info, error) {
	if cfg == nil || len(cfg.Certificates) == 0 || len(cfg.Certificates[0].Certificate) == 0 {
		return agentinfo.Info{}, fmt.Errorf("BLE requires an enrolled device certificate")
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert && (cfg.ClientAuth != tls.RequireAnyClientCert || cfg.VerifyPeerCertificate == nil && cfg.VerifyConnection == nil) {
		return agentinfo.Info{}, fmt.Errorf("BLE requires verified client certificates")
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		return agentinfo.Info{}, fmt.Errorf("BLE device certificate: %w", err)
	}
	if time.Now().After(leaf.NotAfter) {
		return agentinfo.Info{}, fmt.Errorf("BLE device certificate has expired")
	}
	identity, found, err := certs.IdentityFromCert(leaf)
	if err != nil || !found || identity.EntityType != certs.EntityAsset {
		return agentinfo.Info{}, fmt.Errorf("BLE requires a device asset identity")
	}
	if !expected.Known() || !expected.Matches(identity.Scope()) {
		return agentinfo.Info{}, fmt.Errorf("BLE authorization scope does not match device certificate")
	}
	hostname, _ := os.Hostname()
	info := agentinfo.Info{ProtocolVersion: agentinfo.ProtocolVersion, PSM: agentinfo.DefaultPSM,
		Identity: identity.IdentityKey(), Hostname: hostname, Enrolled: true,
		Capabilities: []string{"wifi", "apps", "hardware", "bluetooth"}}
	return info, info.Validate()
}

// StartBLEPeripheral publishes metadata only after its authenticated command
// listener is ready. Failures leave no advertisement or listener behind.
func StartBLEPeripheral(parent context.Context, logger *zap.Logger, d *Dispatcher, tlsConfig *tls.Config, expected certs.Scope, mode interceptor.OrgMode) (*Peripheral, error) {
	return startBLEPeripheralWith(parent, logger, d, tlsConfig, expected, mode, startL2CAPServer, startAdvertising)
}

type peripheralListen func(context.Context, *zap.Logger, *Dispatcher, *tls.Config, certs.Scope, interceptor.OrgMode) (func() error, <-chan struct{}, error)
type peripheralAdvertise func(context.Context, *zap.Logger, agentinfo.Info) (func() error, error)

func startBLEPeripheralWith(parent context.Context, logger *zap.Logger, d *Dispatcher, tlsConfig *tls.Config, expected certs.Scope, mode interceptor.OrgMode, listen peripheralListen, advertise peripheralAdvertise) (*Peripheral, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	info, err := peripheralInfo(tlsConfig, expected)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("BLE command dispatcher unavailable")
	}
	ctx, cancel := context.WithCancel(parent)
	stopServer, stopped, err := listen(ctx, logger, d, tlsConfig.Clone(), expected, mode)
	if err != nil {
		cancel()
		return nil, err
	}
	// A failed listener must withdraw its advertisement as well as sessions.
	go func() {
		select {
		case <-stopped:
			cancel()
		case <-ctx.Done():
		}
	}()
	stopAdvertisement, err := advertise(ctx, logger, info)
	if err != nil {
		cancel()
		return nil, errors.Join(err, stopServer())
	}
	p := &Peripheral{cancel: cancel, stopAdvertisement: stopAdvertisement, stopServer: stopServer}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	context.AfterFunc(ctx, func() { p.Close() })
	return p, nil
}
