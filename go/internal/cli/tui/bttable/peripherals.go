// Package bttable implements the interactive `wendy device bluetooth` table.
package bttable

import (
	"sort"
	"strings"
	"unicode"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// Peripheral is a CLI-side view of a discovered Bluetooth peripheral. Fields are
// plain values so the TUI and sorting logic can be unit-tested without a gRPC
// transport.
type Peripheral struct {
	Name       string
	Address    string
	DeviceType string
	Paired     bool
	Connected  bool
	Trusted    bool
	// RSSI is the signal strength in dBm (higher/closer-to-zero is stronger), or
	// 0 when the agent does not report it. Used only as a sort tiebreak today;
	// it has no dedicated column because the current agent never populates it.
	RSSI int32
}

// FromProto converts a DiscoveredBluetoothPeripheral into the local Peripheral.
// The address is canonicalized to upper-case so identity comparisons (Upsert,
// optimistic updates) are exact and two scans reporting the same MAC in
// different case can never shadow one another.
func FromProto(p *agentpb.DiscoveredBluetoothPeripheral) Peripheral {
	return Peripheral{
		Name:       p.GetName(),
		Address:    NormalizeAddress(p.GetAddress()),
		DeviceType: p.GetDeviceType(),
		Paired:     p.GetPaired(),
		Connected:  p.GetConnected(),
		Trusted:    p.GetTrusted(),
		RSSI:       p.GetRssi(),
	}
}

// NormalizeAddress canonicalizes a Bluetooth MAC to upper-case (BlueZ already
// reports upper-case, so this is normally a no-op) and trims surrounding space.
func NormalizeAddress(addr string) string {
	return strings.ToUpper(strings.TrimSpace(addr))
}

// Sort sorts peripherals in-place: connected first, then paired, then by
// descending RSSI (stronger signal first), then by name and address ascending
// so the ordering is stable and obvious at a glance.
func Sort(ps []Peripheral) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.Connected != b.Connected {
			return a.Connected
		}
		if a.Paired != b.Paired {
			return a.Paired
		}
		if a.RSSI != b.RSSI {
			return a.RSSI > b.RSSI
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Address < b.Address
	})
}

// DeviceTypeLabel renders a peripheral's device type for display, capitalizing
// the first letter (e.g. "audio" → "Audio"). An empty type renders as empty.
func DeviceTypeLabel(t string) string {
	if t == "" {
		return ""
	}
	r := []rune(t)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// Upsert merges a discovered peripheral into list, keyed by Address: it replaces
// an existing entry's fields when the address is already present, or appends a
// new entry otherwise. This lets the model accept either today's single batched
// scan response or a future incremental stream without changing behavior.
func Upsert(list []Peripheral, p Peripheral) []Peripheral {
	for i := range list {
		if list[i].Address == p.Address {
			list[i] = p
			return list
		}
	}
	return append(list, p)
}
