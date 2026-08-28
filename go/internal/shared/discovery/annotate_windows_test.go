//go:build windows

package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// TestWindowsAdapterDetailsByNameIndexesCaseInsensitively pins the by-name
// lookup windowsAdapterDetailsByName builds from Get-NetAdapter output: keys
// are lowercased so a later, differently-cased interface name still matches.
func TestWindowsAdapterDetailsByNameIndexesCaseInsensitively(t *testing.T) {
	orig := readNetAdapterEntriesFn
	t.Cleanup(func() { readNetAdapterEntriesFn = orig })
	readNetAdapterEntriesFn = func(context.Context) ([]netAdapterEntry, error) {
		return []netAdapterEntry{{
			Name:                 "Ethernet 3",
			InterfaceDescription: "Remote NDIS Compatible Device",
			LinkSpeed:            "425 Mbps",
		}}, nil
	}

	got := windowsAdapterDetailsByName(context.Background())
	entry, ok := got["ethernet 3"]
	if !ok {
		t.Fatalf("got %#v, want an entry keyed by lowercased name", got)
	}
	if entry.InterfaceDescription != "Remote NDIS Compatible Device" || entry.LinkSpeed != "425 Mbps" {
		t.Errorf("entry = %+v, want the fake's fields", entry)
	}
}

// TestWindowsAdapterDetailsByNameQueryFailureYieldsEmptyMap matches the old
// windowsNetworkAdapterLookup's failure behavior (formerly
// discovery_windows.go's newWindowsNetworkAdapterLookup): a failed
// Get-NetAdapter query degrades annotation, it does not fail the scan.
func TestWindowsAdapterDetailsByNameQueryFailureYieldsEmptyMap(t *testing.T) {
	orig := readNetAdapterEntriesFn
	t.Cleanup(func() { readNetAdapterEntriesFn = orig })
	readNetAdapterEntriesFn = func(context.Context) ([]netAdapterEntry, error) {
		return nil, errors.New("powershell unavailable")
	}

	if got := windowsAdapterDetailsByName(context.Background()); len(got) != 0 {
		t.Fatalf("got %#v, want empty map on query failure", got)
	}
}

// TestWindowsLANAnnotatorAppliesAdapterDetails is the property the missing
// windows newLANAnnotator override broke: it must actually reach
// setLANNetworkInterface, so a live-sighted device picks up the adapter
// display name and link speed the way the old (now-deleted) discoverLAN
// batch path did via lanDeviceFromMDNSEntry + windowsNetworkAdapterLookup,
// instead of being left with only the bare interface name
// lanDeviceFromService set.
func TestWindowsLANAnnotatorAppliesAdapterDetails(t *testing.T) {
	orig := readNetAdapterEntriesFn
	t.Cleanup(func() { readNetAdapterEntriesFn = orig })
	readNetAdapterEntriesFn = func(context.Context) ([]netAdapterEntry, error) {
		return []netAdapterEntry{{
			Name:                 "Ethernet 3",
			InterfaceDescription: "Remote NDIS Compatible Device",
			LinkSpeed:            "425 Mbps",
		}}, nil
	}

	annotate := newLANAnnotator(context.Background())

	dev := &models.LANDevice{NetworkInterface: "Ethernet 3"}
	annotate(dev)

	if dev.NetworkInterface != "Ethernet 3" {
		t.Errorf("NetworkInterface = %q, want %q", dev.NetworkInterface, "Ethernet 3")
	}
	wantUSB := "Remote NDIS Compatible Device (Ethernet 3) 425 Mbps"
	if dev.USB != wantUSB {
		t.Errorf("USB = %q, want %q", dev.USB, wantUSB)
	}
}

// TestWindowsLANAnnotatorSkipsEmptyInterface mirrors the old code's
// `if iface != nil` guard: a sighting from Windows' nil/default-interface
// sweep target (InterfaceName == "") must not be annotated.
func TestWindowsLANAnnotatorSkipsEmptyInterface(t *testing.T) {
	orig := readNetAdapterEntriesFn
	t.Cleanup(func() { readNetAdapterEntriesFn = orig })
	readNetAdapterEntriesFn = func(context.Context) ([]netAdapterEntry, error) {
		return nil, nil
	}

	annotate := newLANAnnotator(context.Background())
	dev := &models.LANDevice{}
	annotate(dev)

	if dev.NetworkInterface != "" || dev.USB != "" {
		t.Errorf("dev = %+v, want untouched for an empty interface name", dev)
	}
}
