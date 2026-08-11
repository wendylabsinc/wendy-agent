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
	// proofFromDisk records that the proof in use came from the on-disk cache
	// rather than a live query, so callers can say so.
	proofFromDisk bool
)

// ProofFromCache reports whether the proof this run broadcast came from the
// on-disk cache instead of a live Roughtime query.
func ProofFromCache() bool {
	proofMu.Lock()
	defer proofMu.Unlock()
	return proofFromDisk
}

// resetProofCache clears the per-process proof cache (test helper).
func resetProofCache() {
	proofMu.Lock()
	defer proofMu.Unlock()
	proofPkt, proofResult, proofCached, proofFromDisk = nil, roughtime.Result{}, false, false
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
		// No route to a Roughtime server. Fall back to the proof kept from a run
		// that did have one: it is signed by the same servers, and the agent only
		// ever advances its clock, so an out-of-date proof cannot make things
		// worse than having none.
		if pkt, midpoint, server, cacheErr := loadProof(); cacheErr == nil {
			proofPkt = pkt
			proofResult = roughtime.Result{Midpoint: midpoint, Server: server}
			proofCached, proofFromDisk = true, true
			return proofPkt, proofResult, nil
		}
		return nil, roughtime.Result{}, fmt.Errorf("roughtime query: %w", err)
	}
	proofPkt = encodeProofPacket(result)
	proofResult = result
	proofCached = true
	// Best-effort: keep it for a future run that cannot reach a server.
	_ = storeProof(proofPkt, result.Midpoint, result.Server)
	return proofPkt, proofResult, nil
}

// CacheProof fetches a proof and stores it for later offline use, discarding any
// error. Called where the host is known to have working cloud access — notably
// straight after login, which pairs the proof with the certificate just issued.
func CacheProof(ctx context.Context) {
	_, _, _ = FetchProofPacket(ctx)
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
