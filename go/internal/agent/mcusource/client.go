package mcusource

import (
	"context"
	"fmt"
	"net"

	"github.com/wendylabsinc/wendy/go/internal/agent/sensorlink"
	sensorlinkpb "github.com/wendylabsinc/wendy/go/proto/gen/sensorlinkpb"
)

// Dialer opens a transport connection to a sensor source. The production
// implementation returns an mTLS net.Conn (see mtlsDialer); tests use plain TCP.
type Dialer interface {
	Dial(ctx context.Context, addr string) (net.Conn, error)
}

// Stream is an active sensorlink session. Frames is closed when the session ends.
type Stream struct {
	Manifest *sensorlinkpb.SensorManifest
	Frames   <-chan *sensorlinkpb.SensorFrame
	conn     net.Conn
	cancel   context.CancelFunc
}

func (s *Stream) Close() error {
	s.cancel()
	return s.conn.Close()
}

// Connect dials the source, reads its manifest, subscribes to channels, and
// streams frames until Close or a read error.
func Connect(ctx context.Context, d Dialer, addr string, channels []uint32) (*Stream, error) {
	conn, err := d.Dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("mcusource: dial %s: %w", addr, err)
	}
	env, err := sensorlink.ReadMessage(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mcusource: read manifest: %w", err)
	}
	manifest := env.GetManifest()
	if manifest == nil {
		conn.Close()
		return nil, fmt.Errorf("mcusource: first message was not a manifest")
	}
	if err := sensorlink.WriteMessage(conn, &sensorlinkpb.Envelope{Msg: &sensorlinkpb.Envelope_Subscribe{Subscribe: &sensorlinkpb.Subscribe{ChannelId: channels}}}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("mcusource: subscribe: %w", err)
	}
	sctx, cancel := context.WithCancel(ctx)
	frames := make(chan *sensorlinkpb.SensorFrame, 8)
	s := &Stream{Manifest: manifest, Frames: frames, conn: conn, cancel: cancel}
	go func() {
		defer close(frames)
		for {
			env, err := sensorlink.ReadMessage(conn)
			if err != nil {
				return
			}
			if f := env.GetFrame(); f != nil {
				select {
				case frames <- f:
				case <-sctx.Done():
					return
				default:
					// Backpressure: drop rather than block the source.
				}
			}
		}
	}()
	return s, nil
}
