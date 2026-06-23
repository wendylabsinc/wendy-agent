package roughtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

const (
	maxRadiusMicros = 2_000_000 // 2 seconds
	nonceSize       = 64
	udpBufSize      = 4096
)

// Server is a Roughtime server descriptor with an embedded public key.
type Server struct {
	Name      string
	Address   string // "host:port"
	PublicKey ed25519.PublicKey
}

// Result is a verified Roughtime result.
type Result struct {
	Midpoint    time.Time
	Radius      time.Duration
	Server      string
	RawResponse []byte // raw server bytes; used for multicast relay
}

// Query queries all servers concurrently and returns the first valid result.
// The context deadline applies to each individual UDP exchange.
func Query(ctx context.Context, servers []Server) (Result, error) {
	type outcome struct {
		r   Result
		err error
	}
	ch := make(chan outcome, len(servers))
	for _, s := range servers {
		s := s
		go func() {
			r, err := queryOne(ctx, s)
			ch <- outcome{r, err}
		}()
	}
	var lastErr error
	for range servers {
		o := <-ch
		if o.err == nil {
			return o.r, nil
		}
		lastErr = o.err
	}
	return Result{}, fmt.Errorf("all Roughtime servers failed; last: %w", lastErr)
}

func queryOne(ctx context.Context, srv Server) (Result, error) {
	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Result{}, fmt.Errorf("nonce: %w", err)
	}

	req := EncodeMessage(map[uint32][]byte{TagNONC: nonce[:]})

	conn, err := net.Dial("udp", srv.Address)
	if err != nil {
		return Result{}, fmt.Errorf("dial %s: %w", srv.Address, err)
	}
	defer conn.Close()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(req); err != nil {
		return Result{}, fmt.Errorf("send: %w", err)
	}

	raw := make([]byte, udpBufSize)
	n, err := conn.Read(raw)
	if err != nil {
		return Result{}, fmt.Errorf("recv from %s: %w", srv.Address, err)
	}
	raw = raw[:n]

	return VerifyResponse(raw, nonce[:], srv)
}

// VerifyResponse verifies a raw Roughtime server response against the
// given nonce and server public key. Exported so the agent's multicast
// listener can re-verify a relayed response without re-querying.
func VerifyResponse(rawResp, nonce []byte, srv Server) (Result, error) {
	outer, err := DecodeMessage(rawResp)
	if err != nil {
		return Result{}, fmt.Errorf("decode outer: %w", err)
	}

	sig, ok := outer[TagSIG]
	if !ok || len(sig) != ed25519.SignatureSize {
		return Result{}, fmt.Errorf("%s: missing or malformed SIG", srv.Name)
	}
	srep, ok := outer[TagSREP]
	if !ok {
		return Result{}, fmt.Errorf("%s: missing SREP", srv.Name)
	}

	toVerify := append([]byte(SigContext), srep...)
	if !ed25519.Verify(srv.PublicKey, toVerify, sig) {
		return Result{}, fmt.Errorf("%s: Ed25519 signature invalid", srv.Name)
	}

	inner, err := DecodeMessage(srep)
	if err != nil {
		return Result{}, fmt.Errorf("%s: decode SREP: %w", srv.Name, err)
	}

	midpB, ok := inner[TagMIDP]
	if !ok || len(midpB) != 8 {
		return Result{}, fmt.Errorf("%s: missing MIDP", srv.Name)
	}
	radiB, ok := inner[TagRADI]
	if !ok || len(radiB) != 4 {
		return Result{}, fmt.Errorf("%s: missing RADI", srv.Name)
	}
	root, ok := inner[TagROOT]
	if !ok || len(root) != 32 {
		return Result{}, fmt.Errorf("%s: missing ROOT", srv.Name)
	}

	// Verify the client's nonce is in the Merkle tree.
	indxB := outer[TagINDX] // may be absent for index 0
	path := outer[TagPATH]  // may be absent for a single-client batch
	index := uint32(0)
	if len(indxB) == 4 {
		index = binary.LittleEndian.Uint32(indxB)
	}
	if !verifyMerkle(nonce, root, path, index) {
		return Result{}, fmt.Errorf("%s: nonce not in Merkle tree (possible replay)", srv.Name)
	}

	midpMicros := binary.LittleEndian.Uint64(midpB)
	radiMicros := binary.LittleEndian.Uint32(radiB)
	if radiMicros > maxRadiusMicros {
		return Result{}, fmt.Errorf("%s: radius %dµs exceeds %dµs limit", srv.Name, radiMicros, maxRadiusMicros)
	}

	return Result{
		Midpoint:    time.UnixMicro(int64(midpMicros)),
		Radius:      time.Duration(radiMicros) * time.Microsecond,
		Server:      srv.Name,
		RawResponse: rawResp,
	}, nil
}

// verifyMerkle checks that nonce is a leaf in the Merkle tree with the given root.
// Leaf hash: SHA-512/256("\x00" || nonce).
// Node hash: SHA-512/256("\x01" || left || right).
func verifyMerkle(nonce, root, path []byte, index uint32) bool {
	if len(root) != 32 {
		return false
	}
	h := sha512.Sum512_256(append([]byte{0x00}, nonce...))
	current := h[:]

	for i := 0; i+32 <= len(path); i += 32 {
		sibling := path[i : i+32]
		var combined []byte
		if index&1 == 0 {
			combined = append(append([]byte{0x01}, current...), sibling...)
		} else {
			combined = append(append([]byte{0x01}, sibling...), current...)
		}
		h = sha512.Sum512_256(combined)
		current = h[:]
		index >>= 1
	}
	return constEqual(current, root)
}

// constEqual is a constant-time byte comparison.
func constEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
