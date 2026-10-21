package meshsession

import (
	"net/netip"

	"github.com/wendylabsinc/wendy/go/internal/agent/udpmessage"
)

const MaxUDPMessageSize = udpmessage.MaxMessageSize
const MaxUDPTransportChunk = udpmessage.MaxChunkSize

// UDPSourceClaim is immutable proof input, not a grant. The registry checks it
// again at every queued fragment admission and every final local delivery.
type UDPSourceClaim struct {
	IP, VIP netip.Addr
	IfIndex int
	Token   uint64
}
type UDPOptions struct {
	Manager  *udpmessage.Manager
	Endpoint *udpmessage.Endpoint
	Guard    udpmessage.Guard
	Done     <-chan struct{}
	Source   *UDPSourceClaim
}

func udpOptions(options []UDPOptions) UDPOptions {
	if len(options) == 0 {
		return UDPOptions{}
	}
	return options[0]
}
