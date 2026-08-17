package rcm

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrUSBAccess reports that a Jetson in recovery mode is present but the OS
// refused to open it (LIBUSB_ERROR_ACCESS). On Linux this means the current
// user lacks permission on the /dev/bus/usb node — fixed by a udev rule or
// sudo; the caller turns this into actionable guidance. Declared here (not in
// the gousb-tagged files) so the shared install flow can classify it on every
// OS, including Windows.
var ErrUSBAccess = errors.New("USB device access denied")

const (
	// MaxRecoveryDevices bounds the structured recovery-device set accepted by
	// selectors. A larger set is almost certainly a broken/hostile USB view, and
	// must not be partially filtered into a seemingly unambiguous flash target.
	MaxRecoveryDevices = 32

	recoveryECIDDigestDomain = "wendyos-recovery-ecid-v1\n"
	maxRecoveryPathBytes     = 512
)

// RecoverySelector pins a recovery target without placing its raw ECID in the
// process list, shell history, errors, picker labels, or flash logs. The digest
// is SHA-256 over recoveryECIDDigestDomain followed by the normalized (lowercase,
// 32-hex-character) ECID.
type RecoverySelector struct {
	PathKey            string
	ExpectedECIDDigest string
}

// NewRecoverySelector validates the two controller-supplied target selectors.
// Both may be empty for the backward-compatible interactive picker; an explicit
// automation target must provide both identity and physical-location pins.
func NewRecoverySelector(pathKey, expectedECIDDigest string) (RecoverySelector, error) {
	if pathKey != "" {
		if len(pathKey) > maxRecoveryPathBytes || strings.TrimSpace(pathKey) != pathKey {
			return RecoverySelector{}, fmt.Errorf("recovery USB path must be a non-whitespace ASCII value of at most %d bytes", maxRecoveryPathBytes)
		}
		for i := 0; i < len(pathKey); i++ {
			if pathKey[i] < 0x21 || pathKey[i] > 0x7e {
				return RecoverySelector{}, fmt.Errorf("recovery USB path must be a non-whitespace ASCII value of at most %d bytes", maxRecoveryPathBytes)
			}
		}
	}
	digest, err := normalizeRecoveryECIDDigest(expectedECIDDigest)
	if err != nil {
		return RecoverySelector{}, err
	}
	if (pathKey == "") != (digest == "") {
		return RecoverySelector{}, fmt.Errorf("expected recovery ECID digest and recovery USB path must be supplied together")
	}
	return RecoverySelector{PathKey: pathKey, ExpectedECIDDigest: digest}, nil
}

// IsZero reports whether no explicit target constraint was supplied.
func (s RecoverySelector) IsZero() bool {
	return s.PathKey == "" && s.ExpectedECIDDigest == ""
}

// ValidateRecoveryDeviceCount fails before family/selector filtering so an
// oversized discovery result cannot be collapsed into one apparently safe row.
func ValidateRecoveryDeviceCount(devs []RecoveryDevice) error {
	if len(devs) > MaxRecoveryDevices {
		return fmt.Errorf("refusing recovery discovery with %d devices (maximum %d)", len(devs), MaxRecoveryDevices)
	}
	return nil
}

// SelectRecoveryDevice applies an explicit selector to an already family-
// filtered, bounded discovery result. It never includes ECIDs or their digests
// in errors. Zero, multiple, missing-identity, and mismatch cases all fail
// closed rather than falling back to the first device.
func SelectRecoveryDevice(devs []RecoveryDevice, selector RecoverySelector) (RecoveryDevice, error) {
	normalized, err := NewRecoverySelector(selector.PathKey, selector.ExpectedECIDDigest)
	if err != nil {
		return RecoveryDevice{}, err
	}
	selector = normalized
	if selector.IsZero() {
		return RecoveryDevice{}, fmt.Errorf("an explicit recovery selector is required")
	}
	if err := ValidateRecoveryDeviceCount(devs); err != nil {
		return RecoveryDevice{}, err
	}
	if len(devs) == 0 {
		return RecoveryDevice{}, fmt.Errorf("no recovery device is present for the requested target family")
	}

	matches := make([]RecoveryDevice, 0, 1)
	identityUnavailable := false
	for _, dev := range devs {
		if selector.PathKey != "" && dev.PathKey != selector.PathKey {
			continue
		}
		if selector.ExpectedECIDDigest != "" {
			got := dev.ECIDDigest()
			if got == "" {
				identityUnavailable = true
				continue
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(selector.ExpectedECIDDigest)) != 1 {
				continue
			}
		}
		matches = append(matches, dev)
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if selector.ExpectedECIDDigest != "" && identityUnavailable {
			return RecoveryDevice{}, fmt.Errorf("a recovery device did not expose a valid ECID; refusing an identity-unverified flash")
		}
		return RecoveryDevice{}, fmt.Errorf("no recovery device matched the expected identity and USB path")
	default:
		return RecoveryDevice{}, fmt.Errorf("multiple recovery devices matched the expected identity and USB path")
	}
}

// RecoveryECIDDigest returns the domain-separated digest used by
// --expected-recovery-ecid-sha256. Invalid/raw descriptor values return an
// empty string so callers can fail closed without reflecting them in errors.
func RecoveryECIDDigest(ecid string) string {
	normalized, ok := normalizeRecoveryECID(ecid)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte(recoveryECIDDigestDomain + normalized))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RecoveryECIDFromUSBSerial converts the reversed 32-hex USB serial exposed by
// NVIDIA's bootROM into the canonical ECID used by tegrarcm. It is primarily
// used by Windows SetupAPI enumeration; Linux/macOS parse the same descriptor
// directly over EP0.
func RecoveryECIDFromUSBSerial(serial string) string {
	normalized, ok := normalizeRecoveryECID(serial)
	if !ok {
		return ""
	}
	return reverseASCII(normalized)
}

func normalizeRecoveryECID(ecid string) (string, bool) {
	if len(ecid) != 32 {
		return "", false
	}
	for i := 0; i < len(ecid); i++ {
		if !isHexDigit(ecid[i]) {
			return "", false
		}
	}
	return strings.ToLower(ecid), true
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')
}

func reverseASCII(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func normalizeRecoveryECIDDigest(digest string) (string, error) {
	if digest == "" {
		return "", nil
	}
	if len(digest) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("expected recovery ECID digest must be sha256:<64 hexadecimal characters>")
	}
	hexPart := digest[len("sha256:"):]
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", fmt.Errorf("expected recovery ECID digest must be sha256:<64 hexadecimal characters>")
	}
	return "sha256:" + strings.ToLower(hexPart), nil
}

// RecoveryDevice identifies a Jetson sitting in USB recovery mode. PathKey is the
// physical USB location (bus + parent-port chain); it is stable across the
// re-enumeration the device undergoes between RCM boot and the ADB flashing gadget,
// so it is the right handle for "flash this specific board".
type RecoveryDevice struct {
	PathKey string // e.g. "20-1.2" (bus 20, hub port 1 → port 2)
	Product uint16 // USB PID: 0x7<module>23 for T234 modules, 0x7026 Thor
	ECID    string // chip BR_CID read over EP0 (may be empty if unreadable)
	// Instance is a platform-specific exact device handle for hosts where
	// PathKey alone cannot single out the devnode: the Windows PnP instance ID
	// (redirected/virtualized USB may report no location path at all). Empty on
	// gousb platforms, where PathKey (bus + port chain) is always present.
	Instance string
}

// ECIDDigest returns a stable, non-raw identity for exact selection. Empty
// means the device did not expose a valid canonical ECID.
func (r RecoveryDevice) ECIDDigest() string { return RecoveryECIDDigest(r.ECID) }

// PinnedSelector captures both selectors observable for this device. It never
// silently degrades to a path-only or identity-only pin.
func (r RecoveryDevice) PinnedSelector() (RecoverySelector, error) {
	if r.PathKey == "" {
		return RecoverySelector{}, fmt.Errorf("recovery device has no stable controller USB path")
	}
	digest := r.ECIDDigest()
	if digest == "" {
		return RecoverySelector{}, fmt.Errorf("recovery device did not expose a valid ECID")
	}
	return NewRecoverySelector(r.PathKey, digest)
}

// IsThor reports whether the device is a T264 (AGX Thor).
func (r RecoveryDevice) IsThor() bool { return r.Product == uint16(ProductThor) }

// IsOrin reports whether the device is a T234 (Orin family: AGX Orin, Orin NX,
// Orin Nano — each module SKU has its own recovery PID).
func (r RecoveryDevice) IsOrin() bool { return IsT234RecoveryPID(r.Product) }

// IsOrinAGX reports whether the device is an AGX Orin module.
func (r RecoveryDevice) IsOrinAGX() bool {
	return r.Product == uint16(ProductOrinAGX32) || r.Product == uint16(ProductOrinAGX64)
}

// IsOrinNano reports whether the device is an Orin Nano module.
func (r RecoveryDevice) IsOrinNano() bool {
	return r.Product == uint16(ProductOrinNano8) || r.Product == uint16(ProductOrinNano4)
}

// Describe returns a one-line human label for pickers/logs. Raw ECIDs and their
// stable digests are deliberately omitted: both are durable hardware identifiers.
func (r RecoveryDevice) Describe() string {
	chip := "Jetson"
	if r.IsThor() {
		chip = "AGX Thor (T264)"
	} else if r.IsOrin() {
		// A recovery PID is a transport/product continuity check, not physical
		// module/carrier attestation. The recovery initrd verifies those later.
		chip = fmt.Sprintf("Jetson T234 recovery (USB product 0x%04x)", r.Product)
	}
	identity := "ECID unavailable"
	if r.ECIDDigest() != "" {
		identity = "ECID available"
	}
	return fmt.Sprintf("%s  [usb %s, %s]", chip, r.PathKey, identity)
}
