//go:build linux

package bluetooth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"go.uber.org/zap"
)

// deviceProps builds an org.bluez.Device1 property map for tests.
func deviceProps(address string, extra map[string]dbus.Variant) map[string]dbus.Variant {
	props := map[string]dbus.Variant{"Address": dbus.MakeVariant(address)}
	for k, v := range extra {
		props[k] = v
	}
	return props
}

func TestFindDeviceByAddress(t *testing.T) {
	adapterEntry := map[string]map[string]dbus.Variant{
		adapterIface: {"Address": dbus.MakeVariant("00:00:00:00:00:00")},
	}

	tests := []struct {
		name            string
		managed         managedObjects
		address         string
		wantFound       bool
		wantDevicePath  dbus.ObjectPath
		wantAdapterPath string
	}{
		{
			name: "exact case match under hci0",
			managed: managedObjects{
				"/org/bluez/hci0": adapterEntry,
				"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF": {
					deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
				},
			},
			address:         "AA:BB:CC:DD:EE:FF",
			wantFound:       true,
			wantDevicePath:  "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF",
			wantAdapterPath: "/org/bluez/hci0",
		},
		{
			name: "lowercase query matches uppercase address",
			managed: managedObjects{
				"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF": {
					deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
				},
			},
			address:         "aa:bb:cc:dd:ee:ff",
			wantFound:       true,
			wantDevicePath:  "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF",
			wantAdapterPath: "/org/bluez/hci0",
		},
		{
			name: "absent address not found",
			managed: managedObjects{
				"/org/bluez/hci0": adapterEntry,
				"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF": {
					deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
				},
			},
			address:   "11:22:33:44:55:66",
			wantFound: false,
		},
		{
			name: "device on hci1, adapter path from Adapter property",
			managed: managedObjects{
				"/org/bluez/hci0": adapterEntry,
				"/org/bluez/hci1": adapterEntry,
				"/org/bluez/hci1/dev_AA_BB_CC_DD_EE_FF": {
					deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", map[string]dbus.Variant{
						"Adapter": dbus.MakeVariant(dbus.ObjectPath("/org/bluez/hci1")),
					}),
				},
			},
			address:         "AA:BB:CC:DD:EE:FF",
			wantFound:       true,
			wantDevicePath:  "/org/bluez/hci1/dev_AA_BB_CC_DD_EE_FF",
			wantAdapterPath: "/org/bluez/hci1",
		},
		{
			name: "adapter path falls back to object-path parent",
			managed: managedObjects{
				"/org/bluez/hci2/dev_AA_BB_CC_DD_EE_FF": {
					deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
				},
			},
			address:         "AA:BB:CC:DD:EE:FF",
			wantFound:       true,
			wantDevicePath:  "/org/bluez/hci2/dev_AA_BB_CC_DD_EE_FF",
			wantAdapterPath: "/org/bluez/hci2",
		},
		{
			name: "duplicate device on two adapters picks lowest path",
			managed: managedObjects{
				"/org/bluez/hci1/dev_AA_BB_CC_DD_EE_FF": {
					deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
				},
				"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF": {
					deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
				},
			},
			address:         "AA:BB:CC:DD:EE:FF",
			wantFound:       true,
			wantDevicePath:  "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF",
			wantAdapterPath: "/org/bluez/hci0",
		},
		{
			name: "non-device objects are ignored",
			managed: managedObjects{
				"/org/bluez/hci0": {
					adapterIface: {"Address": dbus.MakeVariant("AA:BB:CC:DD:EE:FF")},
				},
			},
			address:   "AA:BB:CC:DD:EE:FF",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			devicePath, adapterPath, props, found := findDeviceByAddress(tt.managed, tt.address)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if devicePath != tt.wantDevicePath {
				t.Errorf("devicePath = %q, want %q", devicePath, tt.wantDevicePath)
			}
			if adapterPath != tt.wantAdapterPath {
				t.Errorf("adapterPath = %q, want %q", adapterPath, tt.wantAdapterPath)
			}
			if props == nil {
				t.Error("props = nil, want the device's property map")
			}
		})
	}
}

func TestRestrictToAdapter(t *testing.T) {
	managed := managedObjects{
		"/org/bluez/hci0": {adapterIface: {}},
		"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF": {
			deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
		},
		"/org/bluez/hci1": {adapterIface: {}},
		"/org/bluez/hci1/dev_AA_BB_CC_DD_EE_FF": {
			deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
		},
	}

	t.Run("empty restriction returns input unchanged", func(t *testing.T) {
		if got := restrictToAdapter(managed, ""); len(got) != len(managed) {
			t.Errorf("got %d objects, want %d", len(got), len(managed))
		}
	})

	t.Run("restriction pins device lookup to the given adapter", func(t *testing.T) {
		restricted := restrictToAdapter(managed, "/org/bluez/hci1")
		devicePath, adapterPath, _, found := findDeviceByAddress(restricted, "AA:BB:CC:DD:EE:FF")
		if !found {
			t.Fatal("device should be found on the restricted adapter")
		}
		if devicePath != "/org/bluez/hci1/dev_AA_BB_CC_DD_EE_FF" || adapterPath != "/org/bluez/hci1" {
			t.Errorf("got (%q, %q), want the hci1 device", devicePath, adapterPath)
		}
	})
}

func TestConnectFailureErrorPrefersNotFound(t *testing.T) {
	m := &BlueZManager{logger: zap.NewNop()}
	pairErr := dbus.Error{Name: "org.bluez.Error.Failed", Body: []any{"br-connection-unknown"}}
	goneErr := dbus.Error{
		Name: "org.freedesktop.DBus.Error.UnknownMethod",
		Body: []any{`Method "Connect" with signature "" on interface "org.bluez.Device1" doesn't exist`},
	}

	t.Run("device gone during connect reports NotFound despite pair failure", func(t *testing.T) {
		err := m.connectFailureError("AA:BB:CC:DD:EE:FF", pairErr, goneErr)
		if !errors.Is(err, ErrDeviceNotFound) {
			t.Fatalf("err = %v, want ErrDeviceNotFound wrap", err)
		}
	})

	t.Run("pair failure is primary for ordinary connect failures", func(t *testing.T) {
		connErr := dbus.Error{Name: "org.bluez.Error.Failed", Body: []any{"br-connection-refused"}}
		err := m.connectFailureError("AA:BB:CC:DD:EE:FF", pairErr, connErr)
		if !strings.Contains(err.Error(), "pairing") {
			t.Errorf("err = %q, want the pairing error reported as primary", err.Error())
		}
	})

	t.Run("no pair error reports the connect failure", func(t *testing.T) {
		connErr := dbus.Error{Name: "org.bluez.Error.Failed", Body: []any{"br-connection-refused"}}
		err := m.connectFailureError("AA:BB:CC:DD:EE:FF", nil, connErr)
		if !strings.Contains(err.Error(), "refused") {
			t.Errorf("err = %q, want the connect failure text", err.Error())
		}
	})
}

func TestIncludePeripheral(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]dbus.Variant
		want  bool
	}{
		{"paired only", map[string]dbus.Variant{"Paired": dbus.MakeVariant(true)}, true},
		{"connected only", map[string]dbus.Variant{"Connected": dbus.MakeVariant(true)}, true},
		{"trusted only", map[string]dbus.Variant{"Trusted": dbus.MakeVariant(true)}, true},
		{"rssi present only", map[string]dbus.Variant{"RSSI": dbus.MakeVariant(int16(-60))}, true},
		{"stale cache entry", map[string]dbus.Variant{
			"Name":   dbus.MakeVariant("Old Speaker"),
			"Paired": dbus.MakeVariant(false),
		}, false},
		{"empty props", map[string]dbus.Variant{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := includePeripheral(tt.props); got != tt.want {
				t.Errorf("includePeripheral = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldListPeripheral(t *testing.T) {
	bare := map[string]dbus.Variant{"Name": dbus.MakeVariant("Mystery")}
	withRSSI := map[string]dbus.Variant{"RSSI": dbus.MakeVariant(int16(-50))}
	paired := map[string]dbus.Variant{"Paired": dbus.MakeVariant(true)}

	tests := []struct {
		name        string
		props       map[string]dbus.Variant
		preexisting bool
		want        bool
	}{
		// A device object that appeared during this discovery is always listed —
		// RSSI is an optional BlueZ property and must not gate fresh devices.
		{"new device without RSSI", bare, false, true},
		{"new device with RSSI", withRSSI, false, true},
		// Pre-existing cache entries need a presence marker.
		{"stale cache entry", bare, true, false},
		{"cached but re-seen (RSSI)", withRSSI, true, true},
		{"cached and paired", paired, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldListPeripheral(tt.props, tt.preexisting); got != tt.want {
				t.Errorf("shouldListPeripheral(preexisting=%v) = %v, want %v", tt.preexisting, got, tt.want)
			}
		})
	}
}

func TestDevicePathsUnder(t *testing.T) {
	managed := managedObjects{
		"/org/bluez/hci0": {adapterIface: {}},
		"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF": {
			deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
		},
		"/org/bluez/hci1/dev_11_22_33_44_55_66": {
			deviceIface: deviceProps("11:22:33:44:55:66", nil),
		},
	}
	got := devicePathsUnder(managed, "/org/bluez/hci0")
	if len(got) != 1 || !got["/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF"] {
		t.Errorf("devicePathsUnder = %v, want only the hci0 device", got)
	}
}

func TestResolveAdapterPath(t *testing.T) {
	adapterEntry := map[string]map[string]dbus.Variant{adapterIface: {}}

	t.Run("env override wins verbatim", func(t *testing.T) {
		t.Setenv("WENDY_BT_ADAPTER", "/org/bluez/hci7")
		got, err := resolveAdapterPath(managedObjects{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/org/bluez/hci7" {
			t.Errorf("path = %q, want /org/bluez/hci7", got)
		}
	})

	t.Run("lowest Adapter1 path wins", func(t *testing.T) {
		t.Setenv("WENDY_BT_ADAPTER", "")
		managed := managedObjects{
			"/org/bluez/hci1": adapterEntry,
			"/org/bluez/hci0": adapterEntry,
			"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF": {
				deviceIface: deviceProps("AA:BB:CC:DD:EE:FF", nil),
			},
		}
		got, err := resolveAdapterPath(managed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/org/bluez/hci0" {
			t.Errorf("path = %q, want /org/bluez/hci0", got)
		}
	})

	t.Run("no adapter is an error", func(t *testing.T) {
		t.Setenv("WENDY_BT_ADAPTER", "")
		if _, err := resolveAdapterPath(managedObjects{}); err == nil {
			t.Fatal("expected an error when no adapter implements org.bluez.Adapter1")
		}
	})
}

func TestDbusErrorInfo(t *testing.T) {
	dbErr := dbus.Error{
		Name: "org.freedesktop.DBus.Error.UnknownMethod",
		Body: []any{`Method "Pair" with signature "" on interface "org.bluez.Device1" doesn't exist`},
	}

	t.Run("plain dbus.Error", func(t *testing.T) {
		name, message, ok := dbusErrorInfo(dbErr)
		if !ok || name != dbErr.Name || message != dbErr.Body[0].(string) {
			t.Fatalf("got (%q, %q, %v)", name, message, ok)
		}
	})

	t.Run("wrapped dbus.Error", func(t *testing.T) {
		name, _, ok := dbusErrorInfo(fmt.Errorf("pairing with X: %w", dbErr))
		if !ok || name != dbErr.Name {
			t.Fatalf("got (%q, %v)", name, ok)
		}
	})

	t.Run("pointer dbus.Error", func(t *testing.T) {
		name, _, ok := dbusErrorInfo(dbus.NewError("org.bluez.Error.Failed", []any{"br-connection-unknown"}))
		if !ok || name != "org.bluez.Error.Failed" {
			t.Fatalf("got (%q, %v)", name, ok)
		}
	})

	t.Run("non-dbus error", func(t *testing.T) {
		if _, _, ok := dbusErrorInfo(errors.New("plain")); ok {
			t.Fatal("expected ok=false for a non-dbus error")
		}
	})
}

func TestWrapBluetoothError(t *testing.T) {
	m := &BlueZManager{logger: zap.NewNop()}

	t.Run("missing object wraps ErrDeviceNotFound", func(t *testing.T) {
		err := m.wrapBluetoothError("pairing with", "AA:BB:CC:DD:EE:FF", dbus.Error{
			Name: "org.freedesktop.DBus.Error.UnknownMethod",
			Body: []any{`Method "Pair" with signature "" on interface "org.bluez.Device1" doesn't exist`},
		})
		if !errors.Is(err, ErrDeviceNotFound) {
			t.Fatalf("err = %v, want ErrDeviceNotFound wrap", err)
		}
	})

	t.Run("bearer failure becomes friendly text", func(t *testing.T) {
		err := m.wrapBluetoothError("connecting to", "AA:BB:CC:DD:EE:FF", dbus.Error{
			Name: "org.bluez.Error.Failed",
			Body: []any{"br-connection-unknown"},
		})
		if errors.Is(err, ErrDeviceNotFound) {
			t.Fatal("bearer failure must not be ErrDeviceNotFound")
		}
		if msg := err.Error(); !strings.Contains(msg, "pairing mode") {
			t.Errorf("err = %q, want pairing-mode hint", msg)
		}
	})

	t.Run("unclassified error keeps raw text", func(t *testing.T) {
		raw := errors.New("connection reset by peer")
		err := m.wrapBluetoothError("connecting to", "AA:BB:CC:DD:EE:FF", raw)
		if !strings.Contains(err.Error(), "connection reset by peer") {
			t.Errorf("err = %q, want raw text preserved", err.Error())
		}
	})
}
