//go:build linux

package usbgadget

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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

func TestEnsureOnceLogsAddrAddFailureOnceAcrossConsecutivePasses(t *testing.T) {
	origIfaces, origLink, origAdd := netInterfacesFn, linkByNameFn, addrAddFn
	origLinkFailing, origAddrFailing := linkLookupFailing, addrAddFailing
	defer func() {
		netInterfacesFn, linkByNameFn, addrAddFn = origIfaces, origLink, origAdd
		linkLookupFailing, addrAddFailing = origLinkFailing, origAddrFailing
	}()
	linkLookupFailing, addrAddFailing = false, false

	netInterfacesFn = func() ([]net.Interface, error) {
		return []net.Interface{{Index: 3, Name: "usb0", Flags: net.FlagUp}}, nil
	}
	linkByNameFn = func(name string) (netlink.Link, error) {
		return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}, nil
	}
	addrAddFn = func(netlink.Link, *netlink.Addr) error { return errors.New("permission denied") }

	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	// Two consecutive failing passes: the persistent failure must be reported
	// exactly once (on the transition into the failing state), not every pass.
	ensureOnce(context.Background(), logger)
	ensureOnce(context.Background(), logger)

	if got := logs.FilterLevelExact(zapcore.WarnLevel).Len(); got != 1 {
		t.Fatalf("got %d Warn logs across two consecutive failing passes, want exactly 1", got)
	}
}

func TestEnsureOnceWarnsAgainAfterAnInterveningSuccessfulPass(t *testing.T) {
	origIfaces, origLink, origAdd := netInterfacesFn, linkByNameFn, addrAddFn
	origLinkFailing, origAddrFailing := linkLookupFailing, addrAddFailing
	defer func() {
		netInterfacesFn, linkByNameFn, addrAddFn = origIfaces, origLink, origAdd
		linkLookupFailing, addrAddFailing = origLinkFailing, origAddrFailing
	}()
	linkLookupFailing, addrAddFailing = false, false

	netInterfacesFn = func() ([]net.Interface, error) {
		return []net.Interface{{Index: 3, Name: "usb0", Flags: net.FlagUp}}, nil
	}
	linkByNameFn = func(name string) (netlink.Link, error) {
		return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}, nil
	}
	failing := true
	addrAddFn = func(netlink.Link, *netlink.Addr) error {
		if failing {
			return errors.New("permission denied")
		}
		return nil
	}

	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	ensureOnce(context.Background(), logger) // fails: logs (transition into failure)
	failing = false
	ensureOnce(context.Background(), logger) // succeeds: resets suppression
	failing = true
	ensureOnce(context.Background(), logger) // fails again: must log again

	if got := logs.FilterLevelExact(zapcore.WarnLevel).Len(); got != 2 {
		t.Fatalf("got %d Warn logs, want exactly 2 (one per failure transition, none for the successful pass)", got)
	}
}

func TestEnsureOnceLogsLinkLookupFailureOnceAcrossConsecutivePasses(t *testing.T) {
	origIfaces, origLink, origAdd := netInterfacesFn, linkByNameFn, addrAddFn
	origLinkFailing, origAddrFailing := linkLookupFailing, addrAddFailing
	defer func() {
		netInterfacesFn, linkByNameFn, addrAddFn = origIfaces, origLink, origAdd
		linkLookupFailing, addrAddFailing = origLinkFailing, origAddrFailing
	}()
	linkLookupFailing, addrAddFailing = false, false

	netInterfacesFn = func() ([]net.Interface, error) {
		return []net.Interface{{Index: 3, Name: "usb0", Flags: net.FlagUp}}, nil
	}
	linkByNameFn = func(string) (netlink.Link, error) { return nil, errors.New("no such network interface") }
	addrAddFn = func(netlink.Link, *netlink.Addr) error {
		t.Fatal("addrAddFn must not be called when the link lookup failed")
		return nil
	}

	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	// Two consecutive failing passes: LinkByName errors (e.g. missing
	// CAP_NET_ADMIN, netlink unreachable) must not go completely silent, but
	// also must not spam a log line every interval forever.
	ensureOnce(context.Background(), logger)
	ensureOnce(context.Background(), logger)

	if got := logs.FilterLevelExact(zapcore.WarnLevel).Len(); got != 1 {
		t.Fatalf("got %d Warn logs across two consecutive failing passes, want exactly 1", got)
	}
}
