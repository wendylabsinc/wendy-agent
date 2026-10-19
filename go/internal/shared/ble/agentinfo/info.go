// Package agentinfo defines the Wendy agent's public BLE discovery metadata.
// Metadata is a connection hint. Only a verified TLS certificate proves identity.
package agentinfo

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

const (
	ServiceUUID               = "7565e9eb-4c20-4b67-9272-d708b397b631"
	CharacteristicUUID        = "7565e9ec-4c20-4b67-9272-d708b397b631"
	ProtocolVersion    uint16 = 1
	DefaultPSM         uint16 = 128
	MaxSize                   = 512
)

// Info is UTF-8 JSON in a single read-only GATT characteristic. Version 1 uses
// uint16 big-endian length-prefixed protobuf commands over mutual TLS over LE CoC.
// Unknown JSON fields are ignored so compatible metadata can be extended.
type Info struct {
	ProtocolVersion uint16   `json:"protocolVersion"`
	PSM             uint16   `json:"psm"`
	Identity        string   `json:"identity"`
	Hostname        string   `json:"hostname"`
	Enrolled        bool     `json:"enrolled"`
	Capabilities    []string `json:"capabilities"`
}

func (i Info) Validate() error {
	if i.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported agent BLE protocol version %d", i.ProtocolVersion)
	}
	if i.PSM < 0x80 || i.PSM > 0xff {
		return fmt.Errorf("invalid LE dynamic PSM %d", i.PSM)
	}
	if !i.Enrolled {
		return fmt.Errorf("agent BLE recovery requires an enrolled device")
	}
	id, err := certs.ParseIdentityURN(i.Identity)
	if err != nil || id.EntityType != certs.EntityAsset || id.IdentityKey() != i.Identity {
		return fmt.Errorf("invalid agent BLE device identity")
	}
	if len(i.Hostname) == 0 || len(i.Hostname) > 253 || strings.ContainsFunc(i.Hostname, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("invalid agent BLE hostname")
	}
	if len(i.Capabilities) == 0 || len(i.Capabilities) > 16 {
		return fmt.Errorf("invalid agent BLE capabilities")
	}
	for _, capability := range i.Capabilities {
		if capability == "" || len(capability) > 32 || strings.ContainsFunc(capability, func(r rune) bool { return r < 'a' || r > 'z' }) {
			return fmt.Errorf("invalid agent BLE capability")
		}
	}
	return nil
}

func Encode(info Info) ([]byte, error) {
	if err := info.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("agent BLE metadata exceeds %d bytes", MaxSize)
	}
	return data, nil
}

func Decode(data []byte) (*Info, error) {
	if len(data) == 0 || len(data) > MaxSize {
		return nil, fmt.Errorf("invalid agent BLE metadata length %d", len(data))
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("decoding agent BLE metadata: %w", err)
	}
	if err := info.Validate(); err != nil {
		return nil, err
	}
	return &info, nil
}
