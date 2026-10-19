//go:build !linux

package bluetooth

import (
	"context"
	"crypto/tls"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"

	"go.uber.org/zap"
)

func startAdvertising(_ context.Context, _ *zap.Logger, _ agentinfo.Info) (func() error, error) {
	return nil, errUnsupported
}

func startL2CAPServer(_ context.Context, _ *zap.Logger, _ *Dispatcher, _ *tls.Config, _ certs.Scope, _ interceptor.OrgMode) (func() error, <-chan struct{}, error) {
	return nil, nil, errUnsupported
}
