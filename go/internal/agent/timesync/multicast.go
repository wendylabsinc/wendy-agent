package timesync

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
	"go.uber.org/zap"
)

const (
	multicastGroup = "239.255.87.84"
	multicastPort  = 5887
	udpMaxPktSize  = 65536
)

// RunMulticast joins the WendyDatagram multicast group and dispatches incoming
// packets to the Manager. On listen failure or socket error it reconnects after
// 5 s (polling approach — no netlink). Blocks until ctx is done.
func (m *Manager) RunMulticast(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		m.listenMulticast(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (m *Manager) listenMulticast(ctx context.Context) {
	group := &net.UDPAddr{IP: net.ParseIP(multicastGroup), Port: multicastPort}
	conn, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		if m.logger != nil {
			m.logger.Debug("timesync: multicast listen failed", zap.Error(err))
		}
		return
	}
	defer conn.Close()

	buf := make([]byte, udpMaxPktSize)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return
		}
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Timeout or transient error — loop back and check context.
			continue
		}

		t, err := safeProcessPacket(buf[:n])
		if err != nil {
			if m.logger != nil {
				m.logger.Debug("timesync: invalid multicast packet", zap.Error(err))
			}
			continue
		}
		if t.IsZero() {
			continue // unknown msg_type — forward-compat, silently ignore
		}

		if m.logger != nil {
			m.logger.Info("timesync: synced via multicast relay", zap.Time("midpoint", t))
		}
		m.Apply(t)
	}
}

// safeProcessPacket wraps ProcessMulticastPacket with a recover() so that any
// unexpected panic in the untrusted-input parser cannot crash the agent process.
// Time-sync failures are always non-fatal; a panic here is treated as a parse error.
func safeProcessPacket(pkt []byte) (t time.Time, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic processing multicast packet: %v", r)
		}
	}()
	return ProcessMulticastPacket(pkt)
}

// ProcessMulticastPacket parses and verifies a WendyDatagram UDP packet.
//
// Returns the verified midpoint time, a zero time for unknown msg_types (not
// an error — forward compatibility), or an error for malformed/invalid packets.
//
// In relay mode the Mac does not transmit the nonce it used to query the server,
// but the server embeds it in SREP as TagNONC. We extract that nonce so that the
// Merkle-tree proof in VerifyResponse can be validated. The Ed25519 signature
// over the entire SREP (which includes the nonce) guarantees the response was
// issued by the trusted server; an attacker cannot forge a nonce inclusion.
func ProcessMulticastPacket(pkt []byte) (time.Time, error) {
	dg, err := roughtime.Decode(pkt)
	if err != nil {
		return time.Time{}, fmt.Errorf("datagram: %w", err)
	}

	if dg.MsgType != roughtime.MsgTypeRoughtime {
		return time.Time{}, nil // unknown type: silently ignore for forward compat
	}

	rp, err := roughtime.DecodeRoughtimePayload(dg.Payload)
	if err != nil {
		return time.Time{}, fmt.Errorf("roughtime payload: %w", err)
	}

	if int(rp.ServerIndex) >= len(Servers) {
		return time.Time{}, fmt.Errorf("server_index %d out of range (have %d servers)", rp.ServerIndex, len(Servers))
	}
	srv := Servers[rp.ServerIndex]

	// Extract the nonce from the SREP so that verifyMerkle in VerifyResponse
	// can validate the Merkle-tree proof. The signature covers the full SREP
	// (including the nonce), so extracting it from a verified-signature message
	// is safe — an attacker cannot substitute a different nonce without
	// invalidating the Ed25519 signature.
	nonce, err := extractNonceFromResponse(rp.Response)
	if err != nil {
		return time.Time{}, fmt.Errorf("extract nonce: %w", err)
	}

	result, err := roughtime.VerifyResponse(rp.Response, nonce, srv)
	if err != nil {
		return time.Time{}, fmt.Errorf("verify: %w", err)
	}

	return result.Midpoint, nil
}

// extractNonceFromResponse parses the raw Roughtime server response and returns
// the TagNONC bytes from the SREP (inner) message. Returns an error if the
// response cannot be decoded or the nonce is absent/malformed.
func extractNonceFromResponse(rawResp []byte) ([]byte, error) {
	outer, err := roughtime.DecodeMessage(rawResp)
	if err != nil {
		return nil, fmt.Errorf("decode outer: %w", err)
	}
	srep, ok := outer[roughtime.TagSREP]
	if !ok {
		return nil, fmt.Errorf("missing SREP")
	}
	inner, err := roughtime.DecodeMessage(srep)
	if err != nil {
		return nil, fmt.Errorf("decode SREP: %w", err)
	}
	nonce, ok := inner[roughtime.TagNONC]
	if !ok || len(nonce) != 64 {
		return nil, fmt.Errorf("missing or malformed NONC in SREP (len %d)", len(nonce))
	}
	return nonce, nil
}
