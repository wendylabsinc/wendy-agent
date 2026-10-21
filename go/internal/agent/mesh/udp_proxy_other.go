//go:build !linux

package mesh

import (
	"context"
	"errors"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"net"

	"github.com/wendylabsinc/wendy/go/internal/agent/meshingress"
)

const UDPProxyPort = 50059

type UDPFlow interface {
	Send([]byte) error
	Receive(context.Context) ([]byte, error)
	Close() error
}

type UDPDialFunc func(context.Context, meshid.Device, uint16) (UDPFlow, error)
type UDPProxy struct{}

func NewUDPProxy(UDPDialFunc, *meshingress.Registry, *VIPTable) (*UDPProxy, error) {
	return nil, errors.New("net UDP VIP proxy requires Linux")
}
func (*UDPProxy) Start(string) error { return errors.New("net UDP VIP proxy requires Linux") }
func (*UDPProxy) Close() error       { return nil }
func (*UDPProxy) Addr() net.Addr     { return nil }
