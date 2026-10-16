// Package bleprovider discovers Wendy peers with BlueZ and carries authenticated
// local-mesh links over LE L2CAP credit-based channels and TLS 1.3.
package bleprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

const (
	// ALPN is distinct from the QUIC carrier. A BLE link is a reliable byte
	// stream and uses TLS directly, with no QUIC framing or second encryption.
	ALPN = "wendy-local-mesh-ble/1"
	// LinkCost keeps BLE available as a fallback while preferring TCP (256)
	// and NAN (512) routes when they exist.
	LinkCost           uint16 = 4096
	DefaultPSM         uint16 = 0x0081
	DefaultTargetPeers        = 3
	// AdvertisementSize leaves room for flags (3), ServiceData header (2),
	// and the 128-bit tenant service UUID (16) in a legacy 31-byte packet.
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
	DeviceHint [4]byte
	MeshHash   [4]byte
	PSM        uint16
}

func MeshNameHash(name string) [4]byte {
	sum := sha256.Sum256([]byte(name))
	return [4]byte(sum[:4])
}

func ValidPSM(psm uint16) bool { return psm >= 0x0080 && psm <= 0x00ff }

func NewAdvertisement(device uuid.UUID, meshName string, psm uint16) (Advertisement, error) {
	if meshName == "" || !ValidPSM(psm) {
		return Advertisement{}, ErrInvalidAdvertisement
	}
	return Advertisement{DeviceHint: deviceHint(device), MeshHash: MeshNameHash(meshName), PSM: psm}, nil
}

func (a Advertisement) MarshalBinary() ([]byte, error) {
	if !ValidPSM(a.PSM) {
		return nil, ErrInvalidAdvertisement
	}
	b := make([]byte, AdvertisementSize)
	copy(b[:4], a.DeviceHint[:])
	copy(b[4:8], a.MeshHash[:])
	binary.BigEndian.PutUint16(b[8:10], a.PSM)
	return b, nil
}

func ParseAdvertisement(data []byte, meshName string) (Advertisement, error) {
	if len(data) != AdvertisementSize || meshName == "" {
		return Advertisement{}, ErrInvalidAdvertisement
	}
	a := Advertisement{PSM: binary.BigEndian.Uint16(data[8:10])}
	copy(a.DeviceHint[:], data[:4])
	copy(a.MeshHash[:], data[4:8])
	if !ValidPSM(a.PSM) || a.MeshHash != MeshNameHash(meshName) {
		return Advertisement{}, ErrInvalidAdvertisement
	}
	return a, nil
}

func matchingServiceData(data map[string][]byte, serviceUUID, meshName string) (Advertisement, bool) {
	for key, raw := range data {
		if strings.EqualFold(key, serviceUUID) {
			a, err := ParseAdvertisement(raw, meshName)
			return a, err == nil
		}
	}
	return Advertisement{}, false
}

// The leading bytes preserve UUID ordering only when hints differ. Equal
// hints (common with UUIDv7) must reach TLS to compare the complete UUIDs.
func deviceHint(device uuid.UUID) [4]byte { return [4]byte(device[:4]) }
func mayDial(hint [4]byte, self meshid.Device) bool {
	own := deviceHint(self.UUID())
	return bytes.Compare(hint[:], own[:]) <= 0
}
