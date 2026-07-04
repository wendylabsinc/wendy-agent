package clitimesync

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

const (
	multicastAddr = "239.255.87.84:5887"
	multicastTTL  = 1
)

// roughtimeQueryFn is indirected for tests.
var roughtimeQueryFn = roughtime.Query

// Process-lifetime cache (no TTL): the CLI is one-shot, so one proof per run.
var (
	proofMu     sync.Mutex
	proofPkt    []byte
	proofResult roughtime.Result
	proofCached bool
)

// resetProofCache clears the per-process proof cache (test helper).
func resetProofCache() {
	proofMu.Lock()
	defer proofMu.Unlock()
	proofPkt, proofResult, proofCached = nil, roughtime.Result{}, false
}

// FetchProofPacket queries a Roughtime server and returns the encoded
// WendyDatagram packet plus the raw result. The result is memoized for the
// life of the process so fixing several devices in one CLI run issues at most
// one Roughtime query.
func FetchProofPacket(ctx context.Context) ([]byte, roughtime.Result, error) {
	proofMu.Lock()
	defer proofMu.Unlock()
	if proofCached {
		return proofPkt, proofResult, nil
	}
	result, err := roughtimeQueryFn(ctx, timesync.Servers)
	if err != nil {
		return nil, roughtime.Result{}, fmt.Errorf("roughtime query: %w", err)
	}
	proofPkt = encodeProofPacket(result)
	proofResult = result
	proofCached = true
	return proofPkt, proofResult, nil
}

// encodeProofPacket builds the WendyDatagram packet the agent verifies.
func encodeProofPacket(result roughtime.Result) []byte {
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
	return roughtime.Encode(roughtime.Datagram{
		MsgType: roughtime.MsgTypeRoughtime,
		Payload: payload,
	})
}

// BroadcastTime fetches a Roughtime proof and multicasts it as a WendyDatagram
// on all active network interfaces. Best-effort: interface errors are skipped.
// Returns an error only if all Roughtime servers are unreachable.
func BroadcastTime(ctx context.Context) (roughtime.Result, error) {
	pkt, result, err := FetchProofPacket(ctx)
	if err != nil {
		return roughtime.Result{}, err
	}
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
