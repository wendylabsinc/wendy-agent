//go:build darwin

package discovery

import "context"

// usbDisplayNameResolver maps a BSD interface name (en5, en7…) to its macOS
// "Hardware Port" display name ("Wendy USB NCM", "AX88179A"). BSD names encode
// nothing about the bus, so on macOS the display name is the only signal that
// separates a USB gadget link from built-in Ethernet.
//
// The networksetup invocation is deferred to the first lookup and its result
// cached for the life of the resolver, so enumeration shells out at most once
// per USBDirectCandidates call — and not at all when no interface needs it.
func usbDisplayNameResolver() func(string) string {
	var names map[string]string
	loaded := false
	return func(iface string) string {
		if !loaded {
			ctx, cancel := context.WithTimeout(context.Background(), usbDisplayNameTimeout)
			names = darwinInterfaceDisplayNameMap(ctx)
			cancel()
			loaded = true
		}
		return names[iface]
	}
}
