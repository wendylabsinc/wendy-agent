package nanprovider

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func parseEvent(raw string) (string, map[string]string) {
	if i := strings.Index(raw, "NAN-"); i >= 0 {
		raw = raw[i:]
	}
	fields := strings.Fields(raw)
	values := map[string]string{}
	if len(fields) == 0 {
		return "", values
	}
	for _, field := range fields[1:] {
		k, v, ok := strings.Cut(field, "=")
		if ok {
			values[k] = v
		}
	}
	return fields[0], values
}

// decodePresence parses a NAN presence claim: "wendy-nan:2:<device-uuid>".
// The claim is untrusted until the peer's enrolled certificate pins the full
// device principal during the QUIC handshake.
func decodePresence(tenant meshid.Tenant, ssi string) (meshid.Device, error) {
	b, err := hex.DecodeString(ssi)
	if err != nil {
		return meshid.Device{}, err
	}
	var version int
	var text string
	if _, err = fmt.Sscanf(string(b), "wendy-nan:%d:%36s", &version, &text); err != nil || version != 2 {
		return meshid.Device{}, errors.New("invalid peer presence")
	}
	peer, err := meshid.ParseDevice(tenant, text)
	if err != nil || peer.UUID() == uuid.Nil {
		return meshid.Device{}, errors.New("invalid peer presence")
	}
	if string(b) != fmt.Sprintf("wendy-nan:2:%s", peer.UUID()) {
		return meshid.Device{}, errors.New("noncanonical peer presence")
	}
	return peer, nil
}
