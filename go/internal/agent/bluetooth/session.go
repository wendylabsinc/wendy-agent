package bluetooth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

const (
	maxFrameSize      = 65535
	maxBLEConnections = 8
)

type bleSessionLimits struct{ handshake, idle, command, write, lifetime time.Duration }

var defaultBLESessionLimits = bleSessionLimits{
	handshake: 15 * time.Second, idle: 2 * time.Minute, command: 45 * time.Second,
	write: 15 * time.Second, lifetime: 10 * time.Minute,
}

type bleDispatch func(context.Context, *agentpb.BluetoothCommand) *agentpb.BluetoothResponse

func serveBLESession(parent context.Context, raw net.Conn, logger *zap.Logger, dispatch bleDispatch, cfg *tls.Config, expected certs.Scope, mode interceptor.OrgMode, limits bleSessionLimits) {
	ctx, cancel := context.WithTimeout(parent, limits.lifetime)
	defer cancel()
	// Closing the underlying connection interrupts the handshake, reads and
	// writes. TLS Close may itself write close_notify, so close raw directly.
	stopClose := context.AfterFunc(ctx, func() { raw.Close() })
	defer stopClose()
	defer raw.Close()
	conn := tls.Server(raw, cfg)
	if err := conn.SetDeadline(time.Now().Add(limits.handshake)); err != nil {
		return
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		logger.Debug("BLE mTLS handshake failed", zap.Error(err))
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}
	state := conn.ConnectionState()
	ownLeaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		return
	}
	ctx = peer.NewContext(ctx, &peer.Peer{Addr: raw.RemoteAddr(), LocalAddr: raw.LocalAddr(), AuthInfo: credentials.TLSInfo{State: state}})
	if err := interceptor.CheckMTLS(ctx, logger, expected, mode); err != nil {
		return
	}
	for ctx.Err() == nil {
		if err := conn.SetReadDeadline(time.Now().Add(limits.idle)); err != nil {
			return
		}
		payload, err := readMessageFromConn(conn)
		// Every framing error closes the session. Retrying after a partial
		// header/body timeout would interpret its remainder as a new frame.
		if err != nil {
			return
		}
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			return
		}
		if time.Now().After(state.PeerCertificates[0].NotAfter) || time.Now().After(ownLeaf.NotAfter) {
			return
		}
		if err := interceptor.CheckMTLS(ctx, logger, expected, mode); err != nil {
			return
		}
		if err := conn.SetWriteDeadline(time.Now().Add(limits.write)); err != nil {
			return
		}
		var cmd agentpb.BluetoothCommand
		if err := proto.Unmarshal(payload, &cmd); err != nil {
			writeErrFrameToConn(conn, logger, "malformed protobuf")
			return
		}
		commandCtx, commandCancel := context.WithTimeout(ctx, limits.command)
		resp := dispatch(commandCtx, &cmd)
		commandCancel()
		if ctx.Err() != nil {
			return
		}
		if err := conn.SetWriteDeadline(time.Now().Add(limits.write)); err != nil {
			return
		}
		respBytes, err := proto.Marshal(resp)
		if err != nil {
			writeErrFrameToConn(conn, logger, "marshal error")
			return
		}
		if err := writeFrameToConn(conn, respBytes); err != nil {
			return
		}
	}
}

func readMessageFromConn(r io.Reader) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(header[:]))
	if n == 0 || n > maxFrameSize {
		return nil, fmt.Errorf("invalid BLE frame size: %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeFrameToConn(w io.Writer, data []byte) error {
	if len(data) == 0 || len(data) > maxFrameSize {
		return fmt.Errorf("invalid BLE frame size: %d", len(data))
	}
	frame := make([]byte, 2+len(data))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(data)))
	copy(frame[2:], data)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func writeErrFrameToConn(w io.Writer, logger *zap.Logger, msg string) {
	if b, err := proto.Marshal(errResp(msg)); err == nil {
		if err := writeFrameToConn(w, b); err != nil {
			logger.Debug("BLE error response failed", zap.Error(err))
		}
	}
}
