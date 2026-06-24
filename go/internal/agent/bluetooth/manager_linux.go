//go:build linux

package bluetooth

import (
	"context"
	"errors"
	"fmt"
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
)

type BlueZManager struct {
	logger *zap.Logger
}

func newPlatformManager(logger *zap.Logger) Manager {
	return &BlueZManager{logger: logger}
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

		adapterPath := bluezAdapterPath()
		adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))

		// Power on the adapter. The call is a no-op if it is already on, but it
		// also clears Command Disallowed state left over from a previous BLE
		// connection that wasn't fully torn down at the HCI level.
		if call := adapter.Call("org.freedesktop.DBus.Properties.Set", 0,
			adapterIface, "Powered", dbus.MakeVariant(true)); call.Err != nil {
			m.logger.Warn("Failed to power on Bluetooth adapter", zap.Error(call.Err))
			return
		}

		// Start discovery.
		if call := adapter.Call(adapterIface+".StartDiscovery", 0); call.Err != nil {
			m.logger.Warn("Failed to start Bluetooth discovery", zap.Error(call.Err))
			return
		}

		// Let discovery run, then stop it (best-effort).
		select {
		case <-time.After(scanDuration):
		case <-ctx.Done():
		}
		if call := adapter.Call(adapterIface+".StopDiscovery", 0); call.Err != nil {
			m.logger.Debug("Failed to stop Bluetooth discovery", zap.Error(call.Err))
		}

		peripherals := m.collectPeripherals(conn, adapterPath)
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
// and returns those belonging to the given adapter.
func (m *BlueZManager) collectPeripherals(conn *dbus.Conn, adapterPath string) []*agentpb.DiscoveredBluetoothPeripheral {
	var managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	root := conn.Object(bluezService, "/")
	if err := root.Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&managed); err != nil {
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

// deviceObjectPath returns the BlueZ D-Bus object path for a device given its
// adapter path and Bluetooth address. BlueZ encodes the address with colons
// replaced by underscores and upper-cased hex, e.g.
// /org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF.
func deviceObjectPath(adapterPath, address string) dbus.ObjectPath {
	encoded := strings.ToUpper(strings.ReplaceAll(address, ":", "_"))
	return dbus.ObjectPath(adapterPath + "/dev_" + encoded)
}

// isAlreadyExists reports whether a BlueZ D-Bus error indicates the operation
// was a no-op because the resource already exists (e.g. pairing a device that
// is already paired). Such errors are safe to treat as success.
func isAlreadyExists(err error) bool {
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		return dbusErr.Name == "org.bluez.Error.AlreadyExists"
	}
	return false
}

// Connect connects to a Bluetooth peripheral by address via BlueZ over D-Bus.
// When pair is set it registers a headless pairing agent and pairs first; when
// trust is set it marks the device trusted so BlueZ reconnects it automatically.
func (m *BlueZManager) Connect(ctx context.Context, address string, pair, trust bool) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connecting to system bus: %w", err)
	}
	defer conn.Close()

	devicePath := deviceObjectPath(bluezAdapterPath(), address)
	device := conn.Object(bluezService, devicePath)

	if trust {
		if call := device.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0,
			deviceIface, "Trusted", dbus.MakeVariant(true)); call.Err != nil {
			// Trust is best-effort: it improves reconnection but is not required
			// for the connection itself to succeed.
			m.logger.Warn("Failed to trust device", zap.String("address", address), zap.Error(call.Err))
		}
	}

	if pair {
		// BlueZ rejects pairing requests unless an authentication agent is
		// registered. Register a headless "just works" agent on this connection;
		// it is unregistered automatically when the connection closes.
		if err := registerPairingAgent(conn, m.logger, devicePath); err != nil {
			m.logger.Warn("Failed to register pairing agent", zap.Error(err))
		}
		if call := device.CallWithContext(ctx, deviceIface+".Pair", 0); call.Err != nil && !isAlreadyExists(call.Err) {
			return fmt.Errorf("pairing with %s: %w", address, call.Err)
		}
	}

	if call := device.CallWithContext(ctx, deviceIface+".Connect", 0); call.Err != nil {
		return fmt.Errorf("connecting to %s: %w", address, call.Err)
	}

	m.logger.Info("Connected to Bluetooth device", zap.String("address", address))
	return nil
}

// Disconnect disconnects from a Bluetooth peripheral via BlueZ over D-Bus.
func (m *BlueZManager) Disconnect(ctx context.Context, address string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connecting to system bus: %w", err)
	}
	defer conn.Close()

	device := conn.Object(bluezService, deviceObjectPath(bluezAdapterPath(), address))
	if call := device.CallWithContext(ctx, deviceIface+".Disconnect", 0); call.Err != nil {
		return fmt.Errorf("disconnecting from %s: %w", address, call.Err)
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

	adapterPath := bluezAdapterPath()
	devicePath := deviceObjectPath(adapterPath, address)
	adapter := conn.Object(bluezService, dbus.ObjectPath(adapterPath))
	if call := adapter.CallWithContext(ctx, adapterIface+".RemoveDevice", 0, devicePath); call.Err != nil {
		return fmt.Errorf("removing device %s: %w", address, call.Err)
	}

	m.logger.Info("Forgot Bluetooth device", zap.String("address", address))
	return nil
}
