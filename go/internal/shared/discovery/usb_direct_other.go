//go:build !darwin && !windows

package discovery

// usbDisplayNameResolver has nothing to add on Linux and other unix-likes: the
// kernel interface name is itself the identifier the heuristics key off (enx…,
// usb0, ncm0) and looksLikeUSBConnection falls back to the sysfs device path
// for the rest. A nil resolver means "name-only classification".
func usbDisplayNameResolver() func(string) string { return nil }
