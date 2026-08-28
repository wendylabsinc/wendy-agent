package discovery

import (
	"bytes"
	"strings"
	"testing"
)

// TestLogMDNSBackend covers the WENDY_MDNS_DEBUG line that names the backend
// a stream session actually ran on (dns_sd / avahi-dbus / hashicorp) — until
// now the env var only ever reported failures, so a successful run said
// nothing about which path the platform took.
func TestLogMDNSBackend(t *testing.T) {
	orig := mdnsDebugOut
	t.Cleanup(func() { mdnsDebugOut = orig })

	var buf bytes.Buffer
	mdnsDebugOut = &buf

	t.Setenv("WENDY_MDNS_DEBUG", "")
	logMDNSBackend("hashicorp")
	if buf.Len() != 0 {
		t.Fatalf("backend logged without WENDY_MDNS_DEBUG: %q", buf.String())
	}

	t.Setenv("WENDY_MDNS_DEBUG", "1")
	logMDNSBackend("avahi-dbus")
	if got := buf.String(); !strings.Contains(got, "avahi-dbus") {
		t.Fatalf("debug output %q does not name the backend", got)
	}
}
