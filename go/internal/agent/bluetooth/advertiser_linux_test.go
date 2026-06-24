//go:build linux

package bluetooth

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestFindAdvertisingAdapter(t *testing.T) {
	advMgr := map[string]map[string]dbus.Variant{advManagerIface: {}}
	adapterOnly := map[string]map[string]dbus.Variant{adapterIface: {}}

	tests := []struct {
		name    string
		managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
		want    string
	}{
		{
			name: "advertising-capable adapter at hci0",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci0": advMgr,
			},
			want: "/org/bluez/hci0",
		},
		{
			name: "advertising-capable adapter at hci1 while hci0 lacks the interface",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci0": adapterOnly,
				"/org/bluez/hci1": advMgr,
			},
			want: "/org/bluez/hci1",
		},
		{
			name: "no adapter implements LEAdvertisingManager1",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci0": adapterOnly,
			},
			want: "",
		},
		{
			name: "multiple capable adapters returns the lowest path deterministically",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci2": advMgr,
				"/org/bluez/hci0": advMgr,
				"/org/bluez/hci1": advMgr,
			},
			want: "/org/bluez/hci0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findAdvertisingAdapter(tt.managed); got != tt.want {
				t.Errorf("findAdvertisingAdapter() = %q, want %q", got, tt.want)
			}
		})
	}
}
