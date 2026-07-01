//go:build linux

package commands

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
)

// usbGadgetIfaceNames and usbIfaceAddrs are indirections so tests can inject
// fake interface state without touching real network devices.
var (
	usbGadgetIfaceNames = discovery.USBNetworkInterfaceNames
	usbIfaceAddrs       = func(name string) ([]net.Addr, error) {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return nil, err
		}
		return iface.Addrs()
	}
)

// ipv4Configured reports whether any address is an IPv4 address. A USB gadget
// link that still needs setup carries only an IPv6 link-local address (fe80::)
// and no IPv4; once configured it has either a routable 10.42.0.x lease
// (shared/DHCP) or a 169.254.x.x link-local address, so any IPv4 means the host
// link is already up.
func ipv4Configured(addrs []net.Addr) bool {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() != nil {
			return true
		}
	}
	return false
}

// detectUnconfiguredUSBGadget returns the name of a single USB-C gadget
// interface that is present but has no IPv4 address (so the host link still
// needs configuring), or "" when there is nothing to do. It is read-only and
// needs no privileges.
func detectUnconfiguredUSBGadget() string {
	names, err := usbGadgetIfaceNames()
	if err != nil || len(names) != 1 {
		// 0 = no gadget present; >1 = ambiguous, never guess (the autonomous
		// flow has no --iface override for the user to disambiguate).
		return ""
	}
	name := names[0]
	addrs, err := usbIfaceAddrs(name)
	if err != nil || ipv4Configured(addrs) {
		return ""
	}
	// Don't re-prompt if we already created the profile and it's mid-bring-up.
	if usbSetupProfileExists() {
		return ""
	}
	return name
}

// usbSetupProfileExists reports whether the NetworkManager profile this flow
// manages already exists, so discovery doesn't re-offer setup while the link is
// still coming up. Absence (or no NetworkManager) is treated as "not set up".
// It's a var so tests can stub the nmcli probe.
var usbSetupProfileExists = func() bool {
	nmcliPath, err := exec.LookPath("nmcli")
	if err != nil {
		return false
	}
	out, err := exec.Command(nmcliPath, "-t", "-f", "NAME", "connection", "show").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == usbSetupNMConnName {
			return true
		}
	}
	return false
}

// maybeOfferUSBSetup detects an unconfigured USB-C Wendy gadget link and, with
// the user's consent, configures it by re-executing the hidden "__usb-setup"
// subcommand under sudo. It is best-effort: any failure is reported but never
// aborts discovery.
func maybeOfferUSBSetup(ctx context.Context) error {
	if jsonOutput || !isInteractiveTerminal() {
		return nil
	}
	iface := detectUnconfiguredUSBGadget()
	if iface == "" {
		return nil
	}

	fmt.Printf("Found a USB-C Wendy device whose host link isn't configured (%s).\n", iface)
	confirmed, err := tui.Confirm("Configure the USB-C link to this Wendy device now?")
	if err != nil {
		if !errors.Is(err, tui.ErrCancelled) {
			fmt.Printf("Skipping USB setup: %v\n", err)
		}
		return nil
	}
	if !confirmed {
		fmt.Println("Skipping. Connect by IP with --device, or re-run 'wendy discover' later.")
		return nil
	}

	// tui.Confirm has fully exited here, so the terminal is back in cooked mode
	// and sudo can prompt for a password safely.
	if err := preAuthElevation(); err != nil {
		fmt.Printf("USB setup needs sudo: %v\n", err)
		return nil
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Printf("USB setup failed: %v\n", err)
		return nil
	}
	cmd := exec.CommandContext(ctx, "sudo", self, "__usb-setup", "--iface", iface)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("USB setup didn't complete; see the message above.")
	}
	return nil
}
