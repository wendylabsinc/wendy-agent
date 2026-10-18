//go:build linux

package bluetooth

import (
	"bytes"
	"fmt"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
)

const (
	gattManagerIface        = "org.bluez.GattManager1"
	gattServiceIface        = "org.bluez.GattService1"
	gattCharacteristicIface = "org.bluez.GattCharacteristic1"
	objectManagerIface      = "org.freedesktop.DBus.ObjectManager"
	gattApplicationPath     = dbus.ObjectPath("/org/wendy/agent")
	gattServicePath         = dbus.ObjectPath("/org/wendy/agent/service0")
	gattInfoPath            = dbus.ObjectPath("/org/wendy/agent/service0/char0")
)

// agentInfoApplication publishes connection hints. TLS authenticates the
// advertised identity before the command server accepts any operation.
type agentInfoApplication struct{}

func (*agentInfoApplication) GetManagedObjects() (managedObjects, *dbus.Error) {
	return managedObjects{
		gattServicePath: {
			gattServiceIface: {
				"UUID":     dbus.MakeVariant(agentinfo.ServiceUUID),
				"Primary":  dbus.MakeVariant(true),
				"Includes": dbus.MakeVariant([]dbus.ObjectPath{}),
			},
		},
		gattInfoPath: {
			gattCharacteristicIface: {
				"UUID":    dbus.MakeVariant(agentinfo.CharacteristicUUID),
				"Service": dbus.MakeVariant(gattServicePath),
				"Flags":   dbus.MakeVariant([]string{"read"}),
			},
		},
	}, nil
}

type agentInfoCharacteristic struct {
	value []byte
}

func (c *agentInfoCharacteristic) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	var offset uint16
	if option, ok := options["offset"]; ok {
		var valid bool
		offset, valid = option.Value().(uint16)
		if !valid {
			return nil, dbus.NewError("org.bluez.Error.InvalidArguments", []any{"offset must be uint16"})
		}
	}
	if int(offset) > len(c.value) {
		return nil, dbus.NewError("org.bluez.Error.InvalidOffset", []any{"offset exceeds metadata length"})
	}
	return bytes.Clone(c.value[offset:]), nil
}

// readOnlyProperties creates the Properties interface from the same object
// tree BlueZ reads during application registration.
func readOnlyProperties(ifaces map[string]map[string]dbus.Variant) prop.Map {
	props := make(prop.Map, len(ifaces))
	for iface, values := range ifaces {
		props[iface] = make(map[string]*prop.Prop, len(values))
		for name, value := range values {
			props[iface][name] = &prop.Prop{Value: value.Value(), Emit: prop.EmitFalse}
		}
	}
	return props
}

func exportAgentInfo(conn *dbus.Conn, value []byte) error {
	app := &agentInfoApplication{}
	if err := conn.Export(app, gattApplicationPath, objectManagerIface); err != nil {
		return fmt.Errorf("export GATT object manager: %w", err)
	}
	if err := conn.Export(struct{}{}, gattServicePath, gattServiceIface); err != nil {
		return fmt.Errorf("export GATT service: %w", err)
	}
	if err := conn.Export(&agentInfoCharacteristic{value: value}, gattInfoPath, gattCharacteristicIface); err != nil {
		return fmt.Errorf("export agent info characteristic: %w", err)
	}
	objects, _ := app.GetManagedObjects()
	for path, ifaces := range objects {
		if _, err := prop.Export(conn, path, readOnlyProperties(ifaces)); err != nil {
			return fmt.Errorf("export GATT properties for %s: %w", path, err)
		}
	}
	return nil
}
