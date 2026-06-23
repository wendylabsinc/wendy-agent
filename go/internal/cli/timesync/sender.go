package clitimesync

import (
	"context"
	"fmt"
	"net"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

const (
	multicastAddr = "239.255.87.84:5887"
	multicastTTL  = 1
)

// BroadcastTime fetches a Roughtime proof and multicasts it as a WendyDatagram
// on all active network interfaces. Best-effort: interface errors are skipped.
// Returns an error only if all Roughtime servers are unreachable.
func BroadcastTime(ctx context.Context) (roughtime.Result, error) {
	result, err := roughtime.Query(ctx, timesync.Servers)
	if err != nil {
		return roughtime.Result{}, fmt.Errorf("roughtime query: %w", err)
	}

	// Find server index for the result so the agent can look up the public key.
	serverIdx := uint8(0)
	for i, s := range timesync.Servers {
		if s.Name == result.Server {
			serverIdx = uint8(i)
			break
		}
	}

	payload := roughtime.EncodeRoughtimePayload(roughtime.RoughtimePayload{
		ServerIndex: serverIdx,
		Nonce:       result.Nonce,
		Response:    result.RawResponse,
	})
	pkt := roughtime.Encode(roughtime.Datagram{
		MsgType: roughtime.MsgTypeRoughtime,
		Payload: payload,
	})

	sendMulticast(pkt) // best-effort
	return result, nil
}

func sendMulticast(pkt []byte) {
	dst, err := net.ResolveUDPAddr("udp4", multicastAddr)
	if err != nil {
		return
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		conn, err := net.DialUDP("udp4", &net.UDPAddr{}, dst)
		if err != nil {
			continue
		}
		// Set TTL=1 (link-local).
		if rc, err := conn.SyscallConn(); err == nil {
			rc.Control(func(fd uintptr) { //nolint:errcheck
				setMulticastTTL(fd, multicastTTL)
			})
		}
		conn.Write(pkt) //nolint:errcheck
		conn.Close()
	}
}
