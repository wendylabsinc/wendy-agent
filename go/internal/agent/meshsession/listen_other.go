//go:build !linux

package meshsession

import (
	"context"
	"net"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
)

func listenAppPackets(ctx context.Context, _ *localmesh.Credentials, addr string) (net.PacketConn, error) {
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp", addr)
}
