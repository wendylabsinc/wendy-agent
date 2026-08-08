//go:build linux

// Package usbgadget keeps the device's USB gadget network interface reachable
// at the well-known IPv6 link-local address that the CLI dials directly
// (specs/2026-08-07-usb-deterministic-ncm-design.md).
package usbgadget

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// Seams for tests.
var (
	netInterfacesFn = net.Interfaces
	linkByNameFn    = netlink.LinkByName
	addrAddFn       = netlink.AddrAdd
)

// EnsureWellKnownAddress applies the well-known link-local address to every
// USB gadget interface, once immediately and then every interval until ctx is
// cancelled. The periodic re-apply survives NetworkManager re-activating the
// usb-gadget profile (which flushes addresses it did not configure) and the
// gadget interface appearing late (Jetson USB role-switch races).
func EnsureWellKnownAddress(ctx context.Context, interval time.Duration, logger *zap.Logger) {
	ensureOnce(ctx, logger)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ensureOnce(ctx, logger)
		}
	}
}

func ensureOnce(_ context.Context, logger *zap.Logger) {
	ifaces, err := netInterfacesFn()
	if err != nil {
		return
	}
	for i := range ifaces {
		name := ifaces[i].Name
		// Device-side gadget interfaces are usbN on every supported board
		// (see the usb-gadget.nmconnection profile, pinned to usb0).
		if ifaces[i].Flags&net.FlagLoopback != 0 || !strings.HasPrefix(name, "usb") {
			continue
		}
		link, err := linkByNameFn(name)
		if err != nil {
			continue
		}
		addr, err := netlink.ParseAddr(discovery.WellKnownUSBAddr + "/64")
		if err != nil {
			logger.Error("parsing well-known USB address", zap.Error(err))
			return
		}
		// nodad: the only peer on a gadget link is the USB host, which never
		// claims this address; skipping DAD makes the address usable instantly.
		addr.Flags = unix.IFA_F_NODAD
		addr.Scope = int(netlink.SCOPE_LINK)
		if err := addrAddFn(link, addr); err != nil && !errors.Is(err, syscall.EEXIST) {
			logger.Debug("adding well-known USB address", zap.String("iface", name), zap.Error(err))
		}
	}
}
