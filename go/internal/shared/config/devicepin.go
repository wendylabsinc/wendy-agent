package config

import "strings"

// DevicePin binds a device hostname to the organisation and cloud host its TLS
// identity must belong to (WDY-1149). It is deliberately NOT a certificate
// fingerprint: a device legitimately rotates or re-enrolls its cert, which we
// must not treat as an attack. What we anchor is the device's org + cloud — so
// only a change of organisation or cloud host (a different trust domain
// answering at this hostname) trips the pin.
type DevicePin struct {
	OrgID     int    `json:"orgId"`
	CloudGRPC string `json:"cloudGRPC"`
}

// PinVerdict is the result of comparing an observed device identity against the
// stored pin for its hostname.
type PinVerdict int

const (
	// PinFirstUse means no pin is recorded for the hostname yet.
	PinFirstUse PinVerdict = iota
	// PinMatch means the observed org + cloud host match the stored pin.
	PinMatch
	// PinMismatch means the observed org or cloud host differs from the pin.
	PinMismatch
)

// normalizePinHost lowercases, trims whitespace, and strips a trailing dot and
// ".local" suffix so cosmetic variants of the same hostname key the same pin.
func normalizePinHost(host string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return strings.TrimSuffix(h, ".local")
}

// DevicePinFor returns the stored pin for a hostname, if any.
func (c *Config) DevicePinFor(hostname string) (DevicePin, bool) {
	p, ok := c.DevicePins[normalizePinHost(hostname)]
	return p, ok
}

// EvaluateDevicePin compares an observed (orgID, cloudGRPC) for a hostname
// against the stored pin without mutating the config.
func (c *Config) EvaluateDevicePin(hostname string, orgID int, cloudGRPC string) PinVerdict {
	pin, ok := c.DevicePinFor(hostname)
	if !ok {
		return PinFirstUse
	}
	if pin.OrgID == orgID && pin.CloudGRPC == cloudGRPC {
		return PinMatch
	}
	return PinMismatch
}

// SetDevicePin records (or replaces) the pin for a hostname.
func (c *Config) SetDevicePin(hostname string, orgID int, cloudGRPC string) {
	if c.DevicePins == nil {
		c.DevicePins = make(map[string]DevicePin)
	}
	c.DevicePins[normalizePinHost(hostname)] = DevicePin{OrgID: orgID, CloudGRPC: cloudGRPC}
}
