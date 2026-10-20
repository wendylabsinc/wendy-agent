// Package bleprovider discovers Wendy peers with BlueZ and carries authenticated
// wendy net links over LE L2CAP credit-based channels and TLS 1.3.
package bleprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

const (
	// ALPN is distinct from the QUIC carrier. A BLE link is a reliable byte
	// stream and uses TLS directly, with no QUIC framing or second encryption.
	ALPN = localmesh.StreamALPN
	// LinkCost keeps BLE available as a fallback while preferring TCP (256)
	// and NAN (512) routes when they exist.
	LinkCost           uint16 = 4096
	DefaultPSM         uint16 = 0x0081
	DefaultTargetPeers        = 3
	// Leave room for flags (3), ServiceData framing (2), and the custom
	// 128-bit tenant UUID (16) in a legacy 31-byte advertising packet.
	AdvertisementSize = 10
)

var ErrInvalidAdvertisement = errors.New("invalid Wendy BLE advertisement")

// ServiceUUID is the tenant-scoped discovery UUID: the tenant UUID itself.
// Advertisements are untrusted input; TLS authenticates both devices.
func ServiceUUID(tenant meshid.Tenant) string {
	if tenant == (meshid.Tenant{}) {
		return ""
	}
	return tenant.String()
}

// Advertisement holds the small, non-secret peer hint carried as ServiceData.
// It is exactly 10 bytes so a 128-bit ServiceData field and LE flags fit in
// a legacy 31-byte advertising packet. No trust decision uses this alone.
type Advertisement struct {
	DeviceHint  [4]byte
	NetworkHash [4]byte
	PSM         uint16
}

func NetworkNameHash(name string) [4]byte {
	sum := sha256.Sum256([]byte(name))
	return [4]byte(sum[:4])
}

func ValidPSM(psm uint16) bool { return psm >= 0x0080 && psm <= 0x00ff }

func NewAdvertisement(device uuid.UUID, networkName string, psm uint16) (Advertisement, error) {
	if device == uuid.Nil || networkName == "" || !ValidPSM(psm) {
		return Advertisement{}, ErrInvalidAdvertisement
	}
	return Advertisement{DeviceHint: deviceHint(device), NetworkHash: NetworkNameHash(networkName), PSM: psm}, nil
}

func (a Advertisement) MarshalBinary() ([]byte, error) {
	if !ValidPSM(a.PSM) {
		return nil, ErrInvalidAdvertisement
	}
	b := make([]byte, AdvertisementSize)
	copy(b[:4], a.DeviceHint[:])
	copy(b[4:8], a.NetworkHash[:])
	binary.BigEndian.PutUint16(b[8:10], a.PSM)
	return b, nil
}

func ParseAdvertisement(data []byte, networkName string) (Advertisement, error) {
	if len(data) != AdvertisementSize || networkName == "" {
		return Advertisement{}, ErrInvalidAdvertisement
	}
	a := Advertisement{PSM: binary.BigEndian.Uint16(data[8:10])}
	copy(a.DeviceHint[:], data[:4])
	copy(a.NetworkHash[:], data[4:8])
	if !ValidPSM(a.PSM) || a.NetworkHash != NetworkNameHash(networkName) {
		return Advertisement{}, ErrInvalidAdvertisement
	}
	return a, nil
}

// Equal prefixes (including UUIDv7 allocations) need TLS to resolve the full
// identity and dial ordering. A prefix is never an authorization decision.
func deviceHint(device uuid.UUID) [4]byte { return [4]byte(device[:4]) }

func mayDial(hint [4]byte, self meshid.Device) bool {
	own := deviceHint(self.UUID())
	return bytes.Compare(hint[:], own[:]) <= 0
}

func matchingServiceData(data map[string][]byte, serviceUUID, networkName string) (Advertisement, bool) {
	for key, raw := range data {
		if strings.EqualFold(key, serviceUUID) {
			a, err := ParseAdvertisement(raw, networkName)
			return a, err == nil
		}
	}
	return Advertisement{}, false
}
