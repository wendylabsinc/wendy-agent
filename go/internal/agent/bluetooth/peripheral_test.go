package bluetooth

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"go.uber.org/zap"
)

func TestPeripheralRollbackAndListenerFailure(t *testing.T) {
	pki := newSessionPKI(t, "urn:wendy:org:7:user:test")
	t.Run("advertisement failure closes listener", func(t *testing.T) {
		var closed atomic.Int32
		stopped := make(chan struct{})
		listen := func(context.Context, *zap.Logger, *Dispatcher, *tls.Config, certs.Scope, interceptor.OrgMode) (func() error, <-chan struct{}, error) {
			return func() error { closed.Add(1); close(stopped); return nil }, stopped, nil
		}
		failed := errors.New("BlueZ unavailable")
		advertise := func(context.Context, *zap.Logger, agentinfo.Info) (func() error, error) { return nil, failed }
		p, err := startBLEPeripheralWith(context.Background(), zap.NewNop(), &Dispatcher{}, pki.server, certs.Scope{OrgID: 7}, interceptor.OrgModeStrict, listen, advertise)
		if p != nil || !errors.Is(err, failed) || closed.Load() != 1 {
			t.Fatalf("peripheral=%v,error=%v,listener closes=%d", p, err, closed.Load())
		}
	})
	t.Run("listener failure withdraws advertisement", func(t *testing.T) {
		stopped := make(chan struct{})
		withdrawn := make(chan struct{})
		var listening atomic.Bool
		listen := func(context.Context, *zap.Logger, *Dispatcher, *tls.Config, certs.Scope, interceptor.OrgMode) (func() error, <-chan struct{}, error) {
			listening.Store(true)
			return func() error { return nil }, stopped, nil
		}
		advertise := func(context.Context, *zap.Logger, agentinfo.Info) (func() error, error) {
			if !listening.Load() {
				t.Fatal("advertised before listener ready")
			}
			return func() error { close(withdrawn); return nil }, nil
		}
		p, err := startBLEPeripheralWith(context.Background(), zap.NewNop(), &Dispatcher{}, pki.server, certs.Scope{OrgID: 7}, interceptor.OrgModeStrict, listen, advertise)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		close(stopped)
		select {
		case <-withdrawn:
		case <-time.After(time.Second):
			t.Fatal("failed listener remained advertised")
		}
	})
}

func TestPeripheralCloseConcurrentAndIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var advertisement, server atomic.Int32
	cleanupErr := errors.New("BlueZ already restarted")
	p := &Peripheral{cancel: cancel, stopAdvertisement: func() error { advertisement.Add(1); return cleanupErr }, stopServer: func() error { server.Add(1); return nil }}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := p.Close(); !errors.Is(err, cleanupErr) {
				t.Errorf("Close=%v", err)
			}
		})
	}
	wg.Wait()
	if ctx.Err() == nil || advertisement.Load() != 1 || server.Load() != 1 {
		t.Fatal("Close did not cancel and release both resources exactly once")
	}
}
