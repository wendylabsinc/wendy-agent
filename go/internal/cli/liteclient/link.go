package liteclient

import (
	"time"

	wendypb "github.com/wendylabsinc/wendy/go/proto/gen/litepb"
)

// wcomLink transports whole WendyComMessages; implementations own framing.
// directLink frames messages itself over TCP-TLS or serial; tunnelLink
// exchanges bare message bodies through a cloud gRPC tunnel, where the
// broker owns framing and channels.
type wcomLink interface {
	// linkHandshake performs any transport-level handshake needed before
	// WendyCom messages can flow. Called once, before the protocol handshake.
	linkHandshake() error
	// send marshals and transmits one message; safe for concurrent use.
	send(*wendypb.WendyComMessage) error
	// recv blocks for the next message; timeout <= 0 means no deadline.
	// Called only by the client's single reader (handshake, then readLoop).
	recv(timeout time.Duration) (*wendypb.WendyComMessage, error)
	// maxChunk is the transport's preferred app-push chunk size.
	maxChunk() int
	close() error
}
