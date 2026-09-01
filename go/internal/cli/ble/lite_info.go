package ble

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/ble/central"
)

// Wendy Lite device info service. Base 4E57454E-4459-0002-xxxx-000000000000
// ("NWENDY" + service 0002) — sibling of the 0001 provisioning service in
// lite_client.go. The device publishes this before any TLS work happens, so it
// is untrusted: treat it as a label for picking a device, never as proof of
// identity. The mTLS handshake is what authenticates.
const (
	liteInfoServiceUUID     = "4E57454E-4459-0002-0000-000000000000"
	liteInfoPSMCharUUID     = "4E57454E-4459-0002-0001-000000000000"
	liteInfoDeviceIDUUID    = "4E57454E-4459-0002-0002-000000000000"
	liteInfoDeviceNameUUID  = "4E57454E-4459-0002-0003-000000000000"
	liteInfoDisplayNameUUID = "4E57454E-4459-0002-0004-000000000000"
	liteInfoMTLSUUID        = "4E57454E-4459-0002-0005-000000000000"
)

// LiteInfo is the content of a Wendy Lite device's GATT info service.
type LiteInfo struct {
	PSM         uint16
	DeviceID    string
	DeviceName  string
	DisplayName string
	MTLSEnabled bool
}

// ErrLiteInfoUnavailable reports that the device does not publish the info
// service, or that this platform cannot read GATT at all — Linux and Windows
// have no GATT client, so they always take this path. Callers fall back to
// liteclient.DefaultL2CAPPSM rather than failing.
var ErrLiteInfoUnavailable = errors.New("Wendy Lite info service unavailable")

// ReadLiteInfo reads the device's GATT info service, which carries the L2CAP
// PSM to open along with the identity the device advertises for itself.
//
// It is a best-effort lookup: every failure is wrapped in
// ErrLiteInfoUnavailable so a caller can fall back to its own default PSM —
// liteclient.DefaultL2CAPPSM for the Lite path. Only the PSM characteristic is
// required; the identity and mTLS characteristics are read opportunistically
// and left zero when a device omits them.
func ReadLiteInfo(conn *central.Connection, timeout time.Duration) (*LiteInfo, error) {
	// Required before any characteristic op: the darwin lookup walks the
	// peripheral's discovered services and reports "not found" against an empty
	// list. This is also where Linux and Windows bow out.
	if err := conn.DiscoverServices(central.TimeoutSeconds(timeout)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLiteInfoUnavailable, err)
	}
	if !conn.HasService(liteInfoServiceUUID) {
		return nil, fmt.Errorf("%w: device exposes [%s]", ErrLiteInfoUnavailable, conn.ListServices())
	}

	raw, err := conn.ReadCharacteristic(liteInfoServiceUUID, liteInfoPSMCharUUID)
	if err != nil {
		return nil, fmt.Errorf("%w: reading PSM: %w", ErrLiteInfoUnavailable, err)
	}
	if len(raw) < 2 {
		return nil, fmt.Errorf("%w: PSM characteristic is %d bytes, want 2", ErrLiteInfoUnavailable, len(raw))
	}
	// Little-endian, matching what the firmware publishes.
	psm := binary.LittleEndian.Uint16(raw[:2])
	if psm == 0 {
		return nil, fmt.Errorf("%w: device published PSM 0", ErrLiteInfoUnavailable)
	}

	info := &LiteInfo{PSM: psm}
	// Best-effort from here: a device that answers with the PSM but not its
	// name is still perfectly reachable, and the PSM is the only field any
	// caller currently needs.
	info.DeviceID = readLiteInfoString(conn, liteInfoDeviceIDUUID)
	info.DeviceName = readLiteInfoString(conn, liteInfoDeviceNameUUID)
	info.DisplayName = readLiteInfoString(conn, liteInfoDisplayNameUUID)
	if mtls, err := conn.ReadCharacteristic(liteInfoServiceUUID, liteInfoMTLSUUID); err == nil && len(mtls) > 0 {
		info.MTLSEnabled = mtls[0] != 0
	}
	return info, nil
}

// readLiteInfoString reads one UTF-8 characteristic, yielding "" for both a
// read failure and an empty value (ReadCharacteristic returns (nil, nil) when
// the characteristic holds no bytes).
func readLiteInfoString(conn *central.Connection, charUUID string) string {
	data, err := conn.ReadCharacteristic(liteInfoServiceUUID, charUUID)
	if err != nil {
		return ""
	}
	return string(data)
}
