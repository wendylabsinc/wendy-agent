// Package unitree connects to a Go2 using the Unitree mobile app LAN protocol.
package unitree

import "encoding/hex"

// Device identifies a robot. EncryptionKey must not appear in public output.
type Device struct {
	Serial        string `json:"serial"`
	Name          string `json:"name"`
	Address       string `json:"address,omitempty"`
	Model         string `json:"model,omitempty"`
	EncryptionKey string `json:"-"`
}

// Account is an ephemeral login result. Neither the password nor the cloud
// token is saved: LAN reconnections use the stored per-device encryption key.
type Account struct {
	Token   string   `json:"-"`
	Devices []Device `json:"devices"`
}

// ValidEncryptionKey accepts the vendor's 16-byte hexadecimal robot key.
func ValidEncryptionKey(key string) bool {
	decoded, err := hex.DecodeString(key)
	return err == nil && len(decoded) == 16
}

// ValidSerial accepts a nonempty, bounded printable-ASCII robot identifier.
// Use the same constraint before discovery, connection setup and persistence.
func ValidSerial(serial string) bool {
	if len(serial) == 0 || len(serial) > 128 {
		return false
	}
	for _, r := range serial {
		if r < '!' || r > '~' {
			return false
		}
	}
	return true
}
