//go:build linux

package bluetooth

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
)

func TestAgentInfoReadValue(t *testing.T) {
	info := agentinfo.Info{
		ProtocolVersion: agentinfo.ProtocolVersion, PSM: agentinfo.DefaultPSM,
		Identity: "urn:wendy:org:7:asset:42", Hostname: "wendy-pi", Enrolled: true,
		Capabilities: []string{"wifi"},
	}
	value, err := agentinfo.Encode(info)
	if err != nil {
		t.Fatal(err)
	}
	characteristic := &agentInfoCharacteristic{value: value}
	for _, offset := range []uint16{0, 22, uint16(len(value))} {
		got, err := characteristic.ReadValue(map[string]dbus.Variant{"offset": dbus.MakeVariant(offset)})
		if err != nil || !bytes.Equal(got, value[offset:]) {
			t.Fatalf("offset %d: got %q, %v; want %q", offset, got, err, value[offset:])
		}
	}
	got, readErr := characteristic.ReadValue(nil)
	if readErr != nil {
		t.Fatal(readErr)
	}
	decoded, err := agentinfo.Decode(got)
	if err != nil || !reflect.DeepEqual(decoded, &info) {
		t.Fatalf("full read = %v, %v; want %v", decoded, err, info)
	}
	got[0] = '!'
	if value[0] == '!' {
		t.Fatal("ReadValue exposed mutable metadata storage")
	}
}

func TestAgentInfoReadValueRejectsInvalidOffsets(t *testing.T) {
	characteristic := &agentInfoCharacteristic{value: []byte("metadata")}
	for _, tt := range []struct {
		offset any
		name   string
	}{
		{uint16(9), "org.bluez.Error.InvalidOffset"},
		{uint16(65535), "org.bluez.Error.InvalidOffset"},
		{int32(-1), "org.bluez.Error.InvalidArguments"},
		{"1", "org.bluez.Error.InvalidArguments"},
	} {
		value, err := characteristic.ReadValue(map[string]dbus.Variant{"offset": dbus.MakeVariant(tt.offset)})
		if value != nil || err == nil || err.Name != tt.name {
			t.Errorf("offset %v: value=%q error=%v, want %s", tt.offset, value, err, tt.name)
		}
	}
}

func TestAgentInfoObjectTreeIsReadOnly(t *testing.T) {
	objects, err := (&agentInfoApplication{}).GetManagedObjects()
	if err != nil || len(objects) != 2 {
		t.Fatalf("object tree = %v, %v", objects, err)
	}
	service := objects[gattServicePath][gattServiceIface]
	if service["UUID"].Value() != agentinfo.ServiceUUID || service["Primary"].Value() != true || !reflect.DeepEqual(service["Includes"].Value(), []dbus.ObjectPath{}) {
		t.Errorf("service properties = %v", service)
	}
	characteristic := objects[gattInfoPath][gattCharacteristicIface]
	if characteristic["UUID"].Value() != agentinfo.CharacteristicUUID || characteristic["Service"].Value() != gattServicePath || !reflect.DeepEqual(characteristic["Flags"].Value(), []string{"read"}) {
		t.Errorf("characteristic properties = %v", characteristic)
	}
	if got := dbus.SignatureOf(objects).String(); got != "a{oa{sa{sv}}}" {
		t.Errorf("ObjectManager signature = %q", got)
	}
	for path, ifaces := range objects {
		for iface, props := range readOnlyProperties(ifaces) {
			for name, property := range props {
				if property.Writable || property.Emit != prop.EmitFalse || !reflect.DeepEqual(property.Value, ifaces[iface][name].Value()) {
					t.Errorf("%s %s.%s is not a matching read-only property: %+v", path, iface, name, property)
				}
			}
		}
	}
}
