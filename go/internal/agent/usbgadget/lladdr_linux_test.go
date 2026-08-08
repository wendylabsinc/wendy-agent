//go:build linux

package usbgadget

import (
	"context"
	"net"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"go.uber.org/zap"
)

func TestEnsureOnceAddsAddressToGadgetInterfacesOnly(t *testing.T) {
	origIfaces, origLink, origAdd := netInterfacesFn, linkByNameFn, addrAddFn
	defer func() { netInterfacesFn, linkByNameFn, addrAddFn = origIfaces, origLink, origAdd }()

	netInterfacesFn = func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 2, Name: "eth0", Flags: net.FlagUp},
			{Index: 3, Name: "usb0", Flags: net.FlagUp},
			{Index: 4, Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
		}, nil
	}
	var lookedUp, added []string
	linkByNameFn = func(name string) (netlink.Link, error) {
		lookedUp = append(lookedUp, name)
		return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}, nil
	}
	addrAddFn = func(link netlink.Link, addr *netlink.Addr) error {
		added = append(added, link.Attrs().Name+" "+addr.IPNet.String())
		return nil
	}

	ensureOnce(context.Background(), zap.NewNop())

	if len(lookedUp) != 1 || lookedUp[0] != "usb0" {
		t.Fatalf("looked up %v, want only usb0", lookedUp)
	}
	if len(added) != 1 || added[0] != "usb0 fe80::5741:1/64" {
		t.Fatalf("added %v, want [\"usb0 fe80::5741:1/64\"]", added)
	}
}

func TestEnsureOnceTreatsEEXISTAsSuccess(t *testing.T) {
	origIfaces, origLink, origAdd := netInterfacesFn, linkByNameFn, addrAddFn
	defer func() { netInterfacesFn, linkByNameFn, addrAddFn = origIfaces, origLink, origAdd }()

	netInterfacesFn = func() ([]net.Interface, error) {
		return []net.Interface{{Index: 3, Name: "usb0", Flags: net.FlagUp}}, nil
	}
	linkByNameFn = func(name string) (netlink.Link, error) {
		return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}, nil
	}
	addrAddFn = func(netlink.Link, *netlink.Addr) error { return syscall.EEXIST }

	// Must not panic and must not log at error level; just verify it completes.
	ensureOnce(context.Background(), zap.NewNop())
}
