package bttable

import (
	"reflect"
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestFromProtoPopulatesFields(t *testing.T) {
	pb := &agentpb.DiscoveredBluetoothPeripheral{
		Name:       "AirPods Pro",
		Address:    "AC:12:34:56:78:9F",
		Rssi:       -42,
		DeviceType: "audio",
		Paired:     true,
		Connected:  true,
		Trusted:    true,
	}
	p := FromProto(pb)
	want := Peripheral{
		Name:       "AirPods Pro",
		Address:    "AC:12:34:56:78:9F",
		RSSI:       -42,
		DeviceType: "audio",
		Paired:     true,
		Connected:  true,
		Trusted:    true,
	}
	if p != want {
		t.Fatalf("FromProto = %+v; want %+v", p, want)
	}
}

func TestSortConnectedThenPairedThenRSSIThenName(t *testing.T) {
	ps := []Peripheral{
		{Name: "Zeta", Address: "00:00:00:00:00:05", RSSI: -90},
		{Name: "Paired Weak", Address: "00:00:00:00:00:02", Paired: true, RSSI: -80},
		{Name: "Paired Strong", Address: "00:00:00:00:00:03", Paired: true, RSSI: -40},
		{Name: "Connected", Address: "00:00:00:00:00:01", Connected: true, Paired: true, RSSI: -70},
		{Name: "Alpha", Address: "00:00:00:00:00:04", RSSI: -90},
	}
	Sort(ps)

	var got []string
	for _, p := range ps {
		got = append(got, p.Name)
	}
	// Connected first; then paired (strong RSSI before weak); then unpaired by
	// RSSI tie (-90 == -90) broken by name ascending (Alpha before Zeta).
	want := []string{"Connected", "Paired Strong", "Paired Weak", "Alpha", "Zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sort order = %v; want %v", got, want)
	}
}

func TestSortRSSITiebreakByAddress(t *testing.T) {
	ps := []Peripheral{
		{Name: "Dup", Address: "BB:BB:BB:BB:BB:BB", RSSI: -50},
		{Name: "Dup", Address: "AA:AA:AA:AA:AA:AA", RSSI: -50},
	}
	Sort(ps)
	if ps[0].Address != "AA:AA:AA:AA:AA:AA" {
		t.Fatalf("expected lower address first on full tie, got %v", ps[0].Address)
	}
}

func TestDeviceTypeLabel(t *testing.T) {
	cases := map[string]string{
		"audio":    "Audio",
		"wearable": "Wearable",
		"":         "",
	}
	for in, want := range cases {
		if got := DeviceTypeLabel(in); got != want {
			t.Errorf("DeviceTypeLabel(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestUpsertReplacesByAddress(t *testing.T) {
	list := []Peripheral{
		{Name: "Buds", Address: "D8:3A:11:22:33:21", Paired: false},
	}
	// Same address rediscovered, now paired — should update in place, not append.
	list = Upsert(list, Peripheral{Name: "Buds", Address: "D8:3A:11:22:33:21", Paired: true, Connected: true})
	if len(list) != 1 {
		t.Fatalf("Upsert should dedup by address; got %d entries", len(list))
	}
	if !list[0].Paired || !list[0].Connected {
		t.Fatalf("Upsert should update existing entry fields; got %+v", list[0])
	}
}

func TestUpsertAppendsNewAddress(t *testing.T) {
	list := []Peripheral{
		{Name: "Buds", Address: "D8:3A:11:22:33:21"},
	}
	list = Upsert(list, Peripheral{Name: "Watch", Address: "C0:98:AA:BB:CC:7B"})
	if len(list) != 2 {
		t.Fatalf("Upsert should append a new address; got %d entries", len(list))
	}
}

func TestFromProtoNormalizesAddressToUpper(t *testing.T) {
	p := FromProto(&agentpb.DiscoveredBluetoothPeripheral{Address: "ac:12:34:56:78:9f", Name: "x"})
	if p.Address != "AC:12:34:56:78:9F" {
		t.Fatalf("address not normalized to upper: %q", p.Address)
	}
}

func TestUpsertDedupsCaseDifferingAddressesAfterNormalization(t *testing.T) {
	// Two scans report the same MAC in different case; after FromProto
	// normalization they must collapse to a single entry, not shadow each other.
	upper := FromProto(&agentpb.DiscoveredBluetoothPeripheral{Address: "AC:12:34:56:78:9F", Name: "Upper"})
	lower := FromProto(&agentpb.DiscoveredBluetoothPeripheral{Address: "ac:12:34:56:78:9f", Name: "Lower", Paired: true})
	list := Upsert([]Peripheral{upper}, lower)
	if len(list) != 1 {
		t.Fatalf("expected a single deduped entry, got %d", len(list))
	}
	if !list[0].Paired {
		t.Errorf("expected the existing entry to be updated in place: %+v", list[0])
	}
}
