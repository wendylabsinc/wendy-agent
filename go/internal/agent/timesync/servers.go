package timesync

import (
	"crypto/ed25519"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

// Servers is the baked-in set of Roughtime servers queried on startup and on
// network-up events. Query uses the first valid response — one honest server
// suffices for Roughtime's security guarantee.
//
// Keys must be fetched from each operator's published ecosystem JSON and
// encoded as 32-byte ed25519.PublicKey. See design doc for retrieval steps.
var Servers = []roughtime.Server{
	{
		Name:    "cloudflare",
		Address: "roughtime.cloudflare.com:2002",
		// Fetch from https://roughtime.cloudflare.com (Client Config section).
		PublicKey: ed25519.PublicKey(mustDecodeKey(cloudflarePublicKeyHex)),
	},
	{
		Name:      "chainpoint",
		Address:   "roughtime.int.chainpoint.org:2002",
		PublicKey: ed25519.PublicKey(mustDecodeKey(chainpointPublicKeyHex)),
	},
	{
		Name:      "google",
		Address:   "roughtime.sandbox.google.com:2002",
		PublicKey: ed25519.PublicKey(mustDecodeKey(googlePublicKeyHex)),
	},
}

// Replace these hex strings with the 64-character (32-byte) hex-encoded
// Ed25519 public keys from each server's published ecosystem configuration.
const (
	cloudflarePublicKeyHex = "0000000000000000000000000000000000000000000000000000000000000000"
	chainpointPublicKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	googlePublicKeyHex     = "0000000000000000000000000000000000000000000000000000000000000002"
)

func mustDecodeKey(hex64 string) []byte {
	b, err := hexDecode32(hex64)
	if err != nil {
		panic("timesync: invalid server public key: " + err.Error())
	}
	return b
}

func hexDecode32(s string) ([]byte, error) {
	// encoding/hex is not imported to avoid adding an import just for init.
	// Inline a minimal hex decoder for 32-byte keys (64 hex chars).
	if len(s) != 64 {
		return nil, fmt.Errorf("expected 64 hex chars, got %d", len(s))
	}
	b := make([]byte, 32)
	for i := 0; i < 32; i++ {
		hi, ok1 := hexNibble(s[i*2])
		lo, ok2 := hexNibble(s[i*2+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("invalid hex at position %d", i*2)
		}
		b[i] = hi<<4 | lo
	}
	return b, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
