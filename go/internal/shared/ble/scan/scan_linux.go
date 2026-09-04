//go:build linux

package scan

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// BlueZ D-Bus names.
const (
	bluezService = "org.bluez"
	adapterIface = "org.bluez.Adapter1"
	deviceIface  = "org.bluez.Device1"
)

// managedObjects is the shape org.freedesktop.DBus.ObjectManager returns.
type managedObjects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// RunBLECheck is a no-op on Linux. The CoreBluetooth entitlement problem it
// exists for is macOS-only, and BlueZ reports adapter trouble as an ordinary
// error from newScanner instead of aborting the process.
func RunBLECheck() int { return 0 }

// linuxScanner holds a BlueZ discovery session.
//
// The D-Bus plumbing below deliberately mirrors
// internal/agent/bluetooth/manager_linux.go, which solved the same problems on
// the device side (the onboard radio is not always hci0; BlueZ synthesizes
// placeholder aliases). Those helpers are unexported in a device-side package,
// so this duplicates roughly sixty lines rather than exporting agent internals
// into a CLI package. If a third caller appears, extract them.
type linuxScanner struct {
	mu          sync.Mutex
	conn        *dbus.Conn
	adapterPath string
	closed      bool
}

// newScanner powers on an adapter and starts LE discovery, filtered to the
// requested services where BlueZ can do it natively.
func newScanner(ctx context.Context, services []string) (scanner, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connecting to system bus: %w", err)
	}

	managed, err := getManagedObjects(ctx, conn)
	if err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("enumerating BlueZ objects: %w", err)
	}

	adapterPath, err := resolveAdapterPath(managed)
	if err != nil {
		conn.Close() //nolint:errcheck
		return nil, err
	}

	if err := powerOnAdapter(ctx, conn, adapterPath); err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("powering on adapter %s: %w", adapterPath, err)
	}

	adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))

	// Ask BlueZ to filter for us. Transport "le" keeps classic Bluetooth out,
	// DuplicateData keeps advertisement fields refreshing rather than being
	// reported once, and UUIDs is BlueZ's own service filter. The engine still
	// matches in Go so behavior is identical on every platform.
	filter := map[string]dbus.Variant{
		"Transport":     dbus.MakeVariant("le"),
		"DuplicateData": dbus.MakeVariant(true),
	}
	if len(services) > 0 {
		filter["UUIDs"] = dbus.MakeVariant(services)
	}
	if call := adapter.CallWithContext(ctx, adapterIface+".SetDiscoveryFilter", 0, filter); call.Err != nil {
		// Not fatal: an older BlueZ may reject a key, and an unfiltered scan
		// still produces correct results once the engine filters in Go.
		_ = call.Err
	}

	if call := adapter.CallWithContext(ctx, adapterIface+".StartDiscovery", 0); call.Err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("starting discovery on %s: %w", adapterPath, call.Err)
	}

	return &linuxScanner{conn: conn, adapterPath: adapterPath}, nil
}

// Snapshot re-reads the object tree. Device properties are read as typed D-Bus
// values rather than parsed out of bluetoothctl's text output, which is what
// makes service UUIDs and RSSI available at all.
func (s *linuxScanner) Snapshot() ([]BLEDeviceInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("BLE scan session is closed")
	}

	managed, err := getManagedObjects(context.Background(), s.conn)
	if err != nil {
		return nil, fmt.Errorf("enumerating BlueZ devices: %w", err)
	}

	// Device objects are nested under the adapter, e.g.
	// /org/bluez/hci0/dev_XX_XX_XX_XX_XX_XX.
	prefix := s.adapterPath + "/"
	var devices []BLEDeviceInfo
	for path, ifaces := range managed {
		props, ok := ifaces[deviceIface]
		if !ok || !strings.HasPrefix(string(path), prefix) {
			continue
		}
		address, ok := stringProp(props, "Address")
		if !ok || address == "" {
			continue
		}
		devices = append(devices, BLEDeviceInfo{
			Address:      address,
			Name:         deviceName(props, address),
			ServiceUUIDs: stringsProp(props, "UUIDs"),
			RSSI:         rssiProp(props),
		})
	}
	return devices, nil
}

// Close stops discovery and drops the bus connection. Idempotent.
func (s *linuxScanner) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true

	adapter := s.conn.Object(bluezService, dbus.ObjectPath(s.adapterPath))
	// Best-effort: the adapter may already have gone away.
	_ = adapter.Call(adapterIface+".StopDiscovery", 0).Err
	s.conn.Close() //nolint:errcheck
}

// deviceName resolves the display name. Alias is the user-facing value and
// falls back to Name, but BlueZ synthesizes an Alias for devices advertising no
// name — the address with ':' replaced by '-' — which must not count as a name
// or every anonymous device would appear named.
func deviceName(props map[string]dbus.Variant, address string) string {
	if alias, ok := stringProp(props, "Alias"); ok && alias != "" && !isDefaultAlias(alias, address) {
		return alias
	}
	if name, ok := stringProp(props, "Name"); ok {
		return name
	}
	return ""
}

func isDefaultAlias(alias, address string) bool {
	if address == "" {
		return false
	}
	return strings.EqualFold(alias, strings.ReplaceAll(address, ":", "-"))
}

func getManagedObjects(ctx context.Context, conn *dbus.Conn) (managedObjects, error) {
	var managed managedObjects
	root := conn.Object(bluezService, "/")
	call := root.CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0)
	if call.Err != nil {
		return nil, call.Err
	}
	if err := call.Store(&managed); err != nil {
		return nil, err
	}
	return managed, nil
}

// resolveAdapterPath finds the adapter to scan with. The onboard radio is not
// always hci0, so the tree is searched for an org.bluez.Adapter1 rather than
// assuming a path. WENDY_BT_ADAPTER pins a specific controller, matching the
// override the agent honors.
func resolveAdapterPath(managed managedObjects) (string, error) {
	if want := os.Getenv("WENDY_BT_ADAPTER"); want != "" {
		for path, ifaces := range managed {
			if _, ok := ifaces[adapterIface]; !ok {
				continue
			}
			if string(path) == want || strings.HasSuffix(string(path), "/"+want) {
				return string(path), nil
			}
		}
		return "", fmt.Errorf("no Bluetooth adapter matching WENDY_BT_ADAPTER=%q", want)
	}

	// Lowest path wins so repeated runs pick the same controller on a host with
	// several; map iteration order alone would be arbitrary.
	best := ""
	for path, ifaces := range managed {
		if _, ok := ifaces[adapterIface]; !ok {
			continue
		}
		if best == "" || string(path) < best {
			best = string(path)
		}
	}
	if best == "" {
		return "", fmt.Errorf("no Bluetooth adapter available")
	}
	return best, nil
}

func powerOnAdapter(ctx context.Context, conn *dbus.Conn, adapterPath string) error {
	adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))
	return adapter.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0,
		adapterIface, "Powered", dbus.MakeVariant(true)).Err
}

func stringProp(props map[string]dbus.Variant, key string) (string, bool) {
	v, ok := props[key]
	if !ok {
		return "", false
	}
	s, ok := v.Value().(string)
	return s, ok
}

func stringsProp(props map[string]dbus.Variant, key string) []string {
	v, ok := props[key]
	if !ok {
		return nil
	}
	ss, ok := v.Value().([]string)
	if !ok {
		return nil
	}
	return ss
}

// rssiProp reads the RSSI property, which BlueZ types as int16 and omits
// entirely for a device it is not currently seeing.
func rssiProp(props map[string]dbus.Variant) int {
	v, ok := props["RSSI"]
	if !ok {
		return 0
	}
	rssi, ok := v.Value().(int16)
	if !ok {
		return 0
	}
	return int(rssi)
}
