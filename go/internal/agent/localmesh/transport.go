package localmesh

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	quic "github.com/quic-go/quic-go"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// LinkHello names both ends by device UUID. The TLS handshake already pinned the
// full principal, so the hello only has to prove the peer read the pinned
// identity correctly - there is no numeric identity to announce.
type LinkHello struct {
	Version  int       `json:"version"`
	Device   uuid.UUID `json:"device"`
	Peer     uuid.UUID `json:"peer"`
	MTU      int       `json:"mtu"`
	Datagram int       `json:"datagram"`
}

func QUICConfig() *quic.Config {
	return &quic.Config{EnableDatagrams: true, HandshakeIdleTimeout: 5 * time.Second, MaxIdleTimeout: 20 * time.Second, KeepAlivePeriod: 5 * time.Second,
		MaxIncomingStreams: 1, MaxIncomingUniStreams: -1, InitialStreamReceiveWindow: MaxControlMessage + 4, MaxStreamReceiveWindow: 2 * MaxControlMessage,
		InitialConnectionReceiveWindow: 2 * MaxControlMessage, MaxConnectionReceiveWindow: 2 * MaxControlMessage}
}

// OpenControl binds the fresh protocol to the mutually authenticated identity.
// The device with the lower UUID opens the sole bidirectional stream, retained
// for directory synchronization; both ends compute the same rule from the full
// identity, so no negotiation is needed. It cannot interoperate accidentally
// with Ethernet/BATMAN.
func OpenControl(ctx context.Context, conn *quic.Conn, self, peer meshid.Device) (*quic.Stream, error) {
	if self.Principal() == peer.Principal() {
		return nil, errors.New("self link")
	}
	if self.Tenant() != peer.Tenant() {
		return nil, errors.New("local-mesh peer is outside the tenant")
	}
	state := conn.ConnectionState()
	if state.TLS.NegotiatedProtocol != LinkALPN || !state.SupportsDatagrams.Local || !state.SupportsDatagrams.Remote {
		return nil, errors.New("incompatible local-mesh transport")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var stream *quic.Stream
	var err error
	if meshid.Compare(self, peer) < 0 {
		stream, err = conn.OpenStreamSync(ctx)
	} else {
		stream, err = conn.AcceptStream(ctx)
	}
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			stream.CancelRead(1)
			stream.CancelWrite(1)
		}
	}()
	deadline, _ := ctx.Deadline()
	_ = stream.SetDeadline(deadline)
	if err = WriteControl(stream, ControlMessage{Kind: "hello", Hello: &LinkHello{Version: 2, Device: self.UUID(), Peer: peer.UUID(), MTU: TunnelMTU, Datagram: DatagramLimit}}); err != nil {
		return nil, err
	}
	m, err := ReadControl(stream)
	if err != nil {
		return nil, err
	}
	if m.Kind != "hello" || m.Hello == nil {
		return nil, errors.New("missing local-mesh hello")
	}
	h := m.Hello
	if h.Version != 2 || h.Device != peer.UUID() || h.Peer != self.UUID() || h.MTU != TunnelMTU || h.Datagram != DatagramLimit {
		return nil, errors.New("invalid local-mesh hello")
	}
	_ = stream.SetDeadline(time.Time{})
	ok = true
	return stream, nil
}
