//go:build linux

package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"go.uber.org/zap"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	adapterIface = "org.bluez.Adapter1"
	deviceIface  = "org.bluez.Device1"
	// scanDuration is how long discovery runs before results are collected.
	scanDuration = 8 * time.Second
	// resolveDiscoveryTimeout bounds the on-demand discovery Connect runs when
	// the target device is not in BlueZ's cache (BlueZ evicts unpaired devices
	// ~30s after a scan stops, so this is the common case when connecting a
	// while after scanning). Worst case must stay well inside the CLI's 60s
	// connect timeout to leave room for pairing and connecting.
	resolveDiscoveryTimeout = 12 * time.Second
	// resolvePollInterval is how often on-demand discovery re-enumerates BlueZ
	// objects looking for the target device. There is no D-Bus signal plumbing
	// in this codebase, so polling keeps the interaction synchronous; devices
	// typically appear a few hundred ms into discovery, so a sub-second poll
	// keeps the added connect latency small.
	resolvePollInterval = 500 * time.Millisecond
)

// managedObjects is the result shape of org.freedesktop.DBus.ObjectManager's
// GetManagedObjects: object path → interface name → property name → value.
type managedObjects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

type BlueZManager struct {
	logger *zap.Logger
}

func newPlatformManager(logger *zap.Logger) Manager {
	return &BlueZManager{logger: logger}
}

// getManagedObjects enumerates every object BlueZ exposes (adapters, devices)
// with their typed properties.
func getManagedObjects(ctx context.Context, conn *dbus.Conn) (managedObjects, error) {
	var managed managedObjects
	root := conn.Object(bluezService, "/")
	if err := root.CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&managed); err != nil {
		return nil, fmt.Errorf("enumerating bluez objects: %w", err)
	}
	return managed, nil
}

// resolveAdapterPath selects the BlueZ adapter to operate on: the
// WENDY_BT_ADAPTER override verbatim when set, otherwise the lowest object
// path implementing org.bluez.Adapter1. The onboard radio is not always hci0
// (it can enumerate higher, or a USB dongle may be the only controller), so
// the path is discovered rather than assumed.
func resolveAdapterPath(managed managedObjects) (string, error) {
	if p := os.Getenv("WENDY_BT_ADAPTER"); p != "" {
		return p, nil
	}
	if p := findAdapterByInterface(managed, adapterIface); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no Bluetooth adapter found (no object implements %s)", adapterIface)
}

// powerOnAdapter powers the adapter on. The call is a no-op if it is already
// on, but it also clears Command Disallowed state left over from a previous
// BLE connection that wasn't fully torn down at the HCI level.
func powerOnAdapter(ctx context.Context, conn *dbus.Conn, adapterPath string) error {
	adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))
	call := adapter.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0,
		adapterIface, "Powered", dbus.MakeVariant(true))
	return call.Err
}

// stopDiscoveryTimeout bounds the best-effort StopDiscovery cleanup. It uses
// its own deadline because cleanup often runs after the request context has
// already been canceled, and godbus's plain Call would otherwise block
// indefinitely on an unresponsive bluetoothd.
const stopDiscoveryTimeout = 3 * time.Second

// stopDiscovery stops a discovery session this connection started (best-effort).
func (m *BlueZManager) stopDiscovery(conn *dbus.Conn, adapterPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), stopDiscoveryTimeout)
	defer cancel()
	adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))
	if call := adapter.CallWithContext(ctx, adapterIface+".StopDiscovery", 0); call.Err != nil {
		m.logger.Debug("Failed to stop Bluetooth discovery", zap.Error(call.Err))
	}
}

// findDeviceByAddress locates the org.bluez.Device1 object whose Address
// matches address (case-insensitively) across all adapters — BlueZ device
// paths are per-adapter, so a synthetic /org/bluez/hci0/dev_... guess breaks
// on multi-adapter systems and on devices BlueZ has evicted. The adapter path
// comes from the device's Adapter property, falling back to the object-path
// parent. When several adapters know the device, the lowest device path wins
// so selection is stable across Go's randomised map iteration order.
func findDeviceByAddress(managed managedObjects, address string) (devicePath dbus.ObjectPath, adapterPath string, props map[string]dbus.Variant, found bool) {
	var (
		bestPath  dbus.ObjectPath
		bestProps map[string]dbus.Variant
	)
	for path, ifaces := range managed {
		devProps, ok := ifaces[deviceIface]
		if !ok {
			continue
		}
		addr, ok := stringProp(devProps, "Address")
		if !ok || !strings.EqualFold(addr, address) {
			continue
		}
		if bestPath == "" || path < bestPath {
			bestPath, bestProps = path, devProps
		}
	}
	if bestPath == "" {
		return "", "", nil, false
	}

	var adapter string
	if v, ok := bestProps["Adapter"]; ok {
		if p, ok := v.Value().(dbus.ObjectPath); ok {
			adapter = string(p)
		}
	}
	if adapter == "" {
		if i := strings.LastIndex(string(bestPath), "/"); i > 0 {
			adapter = string(bestPath)[:i]
		}
	}
	return bestPath, adapter, bestProps, true
}

// restrictToAdapter narrows a managed-objects map to the adapter at
// adapterPath and the objects nested under it, so the WENDY_BT_ADAPTER
// override pins device lookups to the chosen controller. An empty adapterPath
// returns the input unchanged.
func restrictToAdapter(managed managedObjects, adapterPath string) managedObjects {
	if adapterPath == "" {
		return managed
	}
	prefix := adapterPath + "/"
	restricted := managedObjects{}
	for path, ifaces := range managed {
		if string(path) == adapterPath || strings.HasPrefix(string(path), prefix) {
			restricted[path] = ifaces
		}
	}
	return restricted
}

// includePeripheral reports whether a cached BlueZ device is worth listing:
// paired/connected/trusted devices always, otherwise only when RSSI is
// present — i.e. the device was actually seen during discovery rather than
// being a stale cache entry that would fail any connect attempt.
func includePeripheral(props map[string]dbus.Variant) bool {
	if boolProp(props, "Paired") || boolProp(props, "Connected") || boolProp(props, "Trusted") {
		return true
	}
	_, hasRSSI := props["RSSI"]
	return hasRSSI
}

// shouldListPeripheral decides whether a device belongs in scan results. A
// device object that appeared during this discovery is always listed — BlueZ
// documents RSSI as optional, so its absence must not hide a fresh device.
// Objects that predate the scan are cache entries and need a presence marker
// (paired/connected/trusted, or re-seen with RSSI) to be worth showing.
func shouldListPeripheral(props map[string]dbus.Variant, preexisting bool) bool {
	return !preexisting || includePeripheral(props)
}

// devicePathsUnder returns the set of org.bluez.Device1 object paths nested
// under the adapter, used to snapshot which devices were already cached
// before a discovery starts.
func devicePathsUnder(managed managedObjects, adapterPath string) map[dbus.ObjectPath]bool {
	prefix := adapterPath + "/"
	paths := map[dbus.ObjectPath]bool{}
	for path, ifaces := range managed {
		if _, ok := ifaces[deviceIface]; ok && strings.HasPrefix(string(path), prefix) {
			paths[path] = true
		}
	}
	return paths
}

// dbusErrorInfo unwraps err to a BlueZ/D-Bus error and returns its D-Bus error
// name and first string body element. godbus delivers error replies as a
// dbus.Error value, but pointer forms exist too (dbus.NewError), so both are
// checked.
func dbusErrorInfo(err error) (name, message string, ok bool) {
	var val dbus.Error
	if errors.As(err, &val) {
		return val.Name, firstStringBody(val.Body), true
	}
	var ptr *dbus.Error
	if errors.As(err, &ptr) && ptr != nil {
		return ptr.Name, firstStringBody(ptr.Body), true
	}
	return "", "", false
}

func firstStringBody(body []any) string {
	if len(body) > 0 {
		if s, ok := body[0].(string); ok {
			return s
		}
	}
	return ""
}

// wrapBluetoothError converts a raw D-Bus failure into a user-facing error.
// Missing-object errors wrap ErrDeviceNotFound so services can map them to a
// NotFound status; recognised BlueZ failures get actionable text; anything
// else keeps the raw error.
func (m *BlueZManager) wrapBluetoothError(op, address string, err error) error {
	name, message, ok := dbusErrorInfo(err)
	if !ok {
		return fmt.Errorf("%s %s: %w", op, address, err)
	}
	text, notFound, classified := friendlyBluetoothError(name, message)
	if !classified {
		return fmt.Errorf("%s %s: %w", op, address, err)
	}
	m.logger.Debug("BlueZ operation failed",
		zap.String("op", op),
		zap.String("address", address),
		zap.String("dbus_error", name),
		zap.String("dbus_message", message))
	if notFound {
		return fmt.Errorf("%w: device %s: %s", ErrDeviceNotFound, address, text)
	}
	return fmt.Errorf("%s %s: %s", op, address, text)
}

// connectFailureError picks which failure to report when a connect attempt
// fails after pairing also failed. A missing-device connect error wins so the
// caller still gets a NotFound status (the device vanished mid-flow); for any
// other connect failure the pairing error is the root cause.
func (m *BlueZManager) connectFailureError(address string, pairErr, connectErr error) error {
	wrapped := m.wrapBluetoothError("connecting to", address, connectErr)
	if pairErr == nil || errors.Is(wrapped, ErrDeviceNotFound) {
		return wrapped
	}
	return m.wrapBluetoothError("pairing with", address, pairErr)
}

// Scan runs a Bluetooth discovery via BlueZ over D-Bus and returns the
// discovered devices on the channel. It powers on the adapter, runs discovery
// for scanDuration, then enumerates known devices through the BlueZ
// ObjectManager so typed properties (RSSI, paired/connected/trusted, icon) are
// read directly rather than parsed from bluetoothctl text output.
func (m *BlueZManager) Scan(ctx context.Context) (<-chan []*agentpb.DiscoveredBluetoothPeripheral, error) {
	ch := make(chan []*agentpb.DiscoveredBluetoothPeripheral, 10)

	go func() {
		defer close(ch)

		conn, err := dbus.ConnectSystemBus()
		if err != nil {
			m.logger.Warn("Failed to connect to system bus for Bluetooth scan", zap.Error(err))
			return
		}
		defer conn.Close()

		managed, err := getManagedObjects(ctx, conn)
		if err != nil {
			m.logger.Warn("Failed to enumerate Bluetooth objects", zap.Error(err))
			return
		}
		adapterPath, err := resolveAdapterPath(managed)
		if err != nil {
			m.logger.Warn("No Bluetooth adapter available for scan", zap.Error(err))
			return
		}
		adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))

		// Snapshot the devices BlueZ already had cached, so results can tell a
		// freshly discovered device apart from a stale cache entry.
		preexisting := devicePathsUnder(managed, adapterPath)

		if err := powerOnAdapter(ctx, conn, adapterPath); err != nil {
			m.logger.Warn("Failed to power on Bluetooth adapter", zap.Error(err))
			return
		}

		// Start discovery.
		if call := adapter.CallWithContext(ctx, adapterIface+".StartDiscovery", 0); call.Err != nil {
			m.logger.Warn("Failed to start Bluetooth discovery", zap.Error(call.Err))
			return
		}

		// Let discovery run, then collect results while it is still active —
		// some BlueZ versions clear volatile properties (RSSI) once discovery
		// stops, which would defeat the includePeripheral presence filter.
		select {
		case <-time.After(scanDuration):
		case <-ctx.Done():
		}
		peripherals := m.collectPeripherals(ctx, conn, adapterPath, preexisting)

		m.stopDiscovery(conn, adapterPath)

		if len(peripherals) > 0 {
			select {
			case ch <- peripherals:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

// collectPeripherals enumerates devices known to BlueZ via the ObjectManager
// and returns those belonging to the given adapter that either appeared
// during this discovery or carry a presence marker (paired, connected,
// trusted, or re-seen with RSSI).
func (m *BlueZManager) collectPeripherals(ctx context.Context, conn *dbus.Conn, adapterPath string, preexisting map[dbus.ObjectPath]bool) []*agentpb.DiscoveredBluetoothPeripheral {
	managed, err := getManagedObjects(ctx, conn)
	if err != nil {
		m.logger.Warn("Failed to enumerate Bluetooth devices", zap.Error(err))
		return nil
	}

	// Device object paths are nested under the adapter, e.g.
	// /org/bluez/hci0/dev_XX_XX_XX_XX_XX_XX.
	prefix := adapterPath + "/"
	var peripherals []*agentpb.DiscoveredBluetoothPeripheral
	for path, ifaces := range managed {
		props, ok := ifaces[deviceIface]
		if !ok || !strings.HasPrefix(string(path), prefix) {
			continue
		}
		if !shouldListPeripheral(props, preexisting[path]) {
			continue
		}
		peripherals = append(peripherals, deviceFromProps(props))
	}
	return peripherals
}

// deviceFromProps maps org.bluez.Device1 properties to the proto peripheral.
func deviceFromProps(props map[string]dbus.Variant) *agentpb.DiscoveredBluetoothPeripheral {
	p := &agentpb.DiscoveredBluetoothPeripheral{}

	if s, ok := stringProp(props, "Address"); ok {
		p.Address = s
	}
	// Alias is the user-facing name (falls back to Name when unset by BlueZ).
	if s, ok := stringProp(props, "Alias"); ok && s != "" {
		p.Name = s
	} else if s, ok := stringProp(props, "Name"); ok {
		p.Name = s
	}
	if v, ok := props["RSSI"]; ok {
		if rssi, ok := v.Value().(int16); ok {
			p.Rssi = int32(rssi)
		}
	}
	// BlueZ icons for audio devices look like "audio-headset" / "audio-card".
	if icon, ok := stringProp(props, "Icon"); ok && strings.HasPrefix(icon, "audio") {
		p.DeviceType = "audio"
	}
	p.Paired = boolProp(props, "Paired")
	p.Connected = boolProp(props, "Connected")
	p.Trusted = boolProp(props, "Trusted")

	return p
}

func stringProp(props map[string]dbus.Variant, key string) (string, bool) {
	if v, ok := props[key]; ok {
		s, ok := v.Value().(string)
		return s, ok
	}
	return "", false
}

func boolProp(props map[string]dbus.Variant, key string) bool {
	if v, ok := props[key]; ok {
		b, _ := v.Value().(bool)
		return b
	}
	return false
}

// isAlreadyExists reports whether a BlueZ D-Bus error indicates the operation
// was a no-op because the resource already exists (e.g. pairing a device that
// is already paired). Such errors are safe to treat as success.
func isAlreadyExists(err error) bool {
	name, _, ok := dbusErrorInfo(err)
	return ok && name == "org.bluez.Error.AlreadyExists"
}

// lookupCachedDevice finds the device by address among BlueZ's cached objects
// (honoring the WENDY_BT_ADAPTER restriction), without running discovery —
// used by Disconnect and Forget, where an uncached device means there is
// nothing to act on.
func lookupCachedDevice(ctx context.Context, conn *dbus.Conn, address string) (devicePath dbus.ObjectPath, adapterPath string, err error) {
	managed, err := getManagedObjects(ctx, conn)
	if err != nil {
		return "", "", err
	}
	managed = restrictToAdapter(managed, os.Getenv("WENDY_BT_ADAPTER"))
	devicePath, adapterPath, _, found := findDeviceByAddress(managed, address)
	if !found {
		return "", "", fmt.Errorf("%w: device %s is not known to the Bluetooth adapter", ErrDeviceNotFound, address)
	}
	return devicePath, adapterPath, nil
}

// resolveDevice locates the device by address. When BlueZ does not know it
// (its cache evicts unpaired devices ~30s after discovery stops), it powers
// on the adapter, starts discovery, and polls until the device appears, the
// timeout elapses, or ctx is done. Discovery is stopped before returning so
// the subsequent connect does not race an active inquiry.
func (m *BlueZManager) resolveDevice(ctx context.Context, conn *dbus.Conn, address string) (dbus.ObjectPath, map[string]dbus.Variant, error) {
	managed, err := getManagedObjects(ctx, conn)
	if err != nil {
		return "", nil, err
	}
	adapterOverride := os.Getenv("WENDY_BT_ADAPTER")
	if path, _, props, found := findDeviceByAddress(restrictToAdapter(managed, adapterOverride), address); found {
		return path, props, nil
	}

	adapterPath, err := resolveAdapterPath(managed)
	if err != nil {
		return "", nil, err
	}
	if err := powerOnAdapter(ctx, conn, adapterPath); err != nil {
		m.logger.Warn("Failed to power on Bluetooth adapter before discovery", zap.Error(err))
	}

	m.logger.Info("Bluetooth device not cached; running on-demand discovery",
		zap.String("address", address), zap.String("adapter", adapterPath))
	adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))
	if call := adapter.CallWithContext(ctx, adapterIface+".StartDiscovery", 0); call.Err != nil {
		// Another client may already be scanning; that works for us too.
		if name, _, ok := dbusErrorInfo(call.Err); !ok || name != "org.bluez.Error.InProgress" {
			return "", nil, m.wrapBluetoothError("discovering", address, call.Err)
		}
	}
	defer m.stopDiscovery(conn, adapterPath)

	deadline := time.NewTimer(resolveDiscoveryTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(resolvePollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-deadline.C:
			return "", nil, fmt.Errorf("%w: device %s was not seen within %s of discovery — make sure it is powered on and in range, then rescan",
				ErrDeviceNotFound, address, resolveDiscoveryTimeout)
		case <-tick.C:
			managed, err := getManagedObjects(ctx, conn)
			if err != nil {
				return "", nil, err
			}
			if path, _, props, found := findDeviceByAddress(restrictToAdapter(managed, adapterOverride), address); found {
				return path, props, nil
			}
		}
	}
}

// Connect connects to a Bluetooth peripheral by address via BlueZ over D-Bus,
// discovering the device first if BlueZ no longer has it cached. When pair is
// set it registers a headless pairing agent and pairs first (skipped if the
// device is already paired); when trust is set it marks the device trusted so
// BlueZ reconnects it automatically. The returned bool reports whether the
// device is paired after the connect — success does not imply pairing because
// pairing failures fall back to a direct connect.
func (m *BlueZManager) Connect(ctx context.Context, address string, pair, trust bool) (bool, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return false, fmt.Errorf("connecting to system bus: %w", err)
	}
	defer conn.Close()

	devicePath, props, err := m.resolveDevice(ctx, conn, address)
	if err != nil {
		return false, err
	}
	device := conn.Object(bluezService, devicePath)

	if trust {
		if call := device.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0,
			deviceIface, "Trusted", dbus.MakeVariant(true)); call.Err != nil {
			// Trust is best-effort: it improves reconnection but is not required
			// for the connection itself to succeed.
			m.logger.Warn("Failed to trust device", zap.String("address", address), zap.Error(call.Err))
		}
	}

	// Pairing failures fall through to a direct connect attempt: some BLE
	// devices reject SMP pairing yet accept connections, and only the connect
	// result tells the two cases apart. If the connect also fails, the pairing
	// error is the root cause and is the one reported.
	var pairErr error
	if pair && !boolProp(props, "Paired") {
		// BlueZ rejects authenticated pairing unless an agent is registered.
		// Register a headless "just works" agent on this connection; it is
		// unregistered automatically when the connection closes. Best-effort:
		// devices needing no authentication pair without an agent.
		if err := registerPairingAgent(conn, m.logger, devicePath); err != nil {
			m.logger.Warn("Failed to register pairing agent; pairing may fail", zap.Error(err))
		}
		if call := device.CallWithContext(ctx, deviceIface+".Pair", 0); call.Err != nil && !isAlreadyExists(call.Err) {
			pairErr = call.Err
			m.logger.Warn("Pairing failed; attempting direct connect",
				zap.String("address", address), zap.Error(call.Err))
		}
	}

	if call := device.CallWithContext(ctx, deviceIface+".Connect", 0); call.Err != nil {
		return false, m.connectFailureError(address, pairErr, call.Err)
	}

	// The device's live Paired property is the source of truth: pairing may
	// have completed implicitly during the connect, or been skipped by the
	// fallback above. Fall back to the computed state if the read fails.
	paired := boolProp(props, "Paired") || (pair && pairErr == nil)
	if call := device.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, deviceIface, "Paired"); call.Err == nil {
		var v dbus.Variant
		if call.Store(&v) == nil {
			if b, ok := v.Value().(bool); ok {
				paired = b
			}
		}
	}

	if pairErr != nil {
		m.logger.Info("Connected without pairing", zap.String("address", address), zap.Error(pairErr))
	}
	m.logger.Info("Connected to Bluetooth device", zap.String("address", address), zap.Bool("paired", paired))
	return paired, nil
}

// Disconnect disconnects from a Bluetooth peripheral via BlueZ over D-Bus.
func (m *BlueZManager) Disconnect(ctx context.Context, address string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connecting to system bus: %w", err)
	}
	defer conn.Close()

	devicePath, _, err := lookupCachedDevice(ctx, conn, address)
	if err != nil {
		return err
	}

	device := conn.Object(bluezService, devicePath)
	if call := device.CallWithContext(ctx, deviceIface+".Disconnect", 0); call.Err != nil {
		return m.wrapBluetoothError("disconnecting from", address, call.Err)
	}

	m.logger.Info("Disconnected from Bluetooth device", zap.String("address", address))
	return nil
}

// Forget removes a paired Bluetooth peripheral via BlueZ's Adapter1.RemoveDevice.
func (m *BlueZManager) Forget(ctx context.Context, address string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connecting to system bus: %w", err)
	}
	defer conn.Close()

	devicePath, adapterPath, err := lookupCachedDevice(ctx, conn, address)
	if err != nil {
		return err
	}

	adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))
	if call := adapter.CallWithContext(ctx, adapterIface+".RemoveDevice", 0, devicePath); call.Err != nil {
		return m.wrapBluetoothError("removing device", address, call.Err)
	}

	m.logger.Info("Forgot Bluetooth device", zap.String("address", address))
	return nil
}
