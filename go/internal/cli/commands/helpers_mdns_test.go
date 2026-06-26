package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestNormalizeMDNSHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"wendy-thor.local", "wendy-thor"},
		{"wendy-thor.local.", "wendy-thor"},
		{"Wendy-Thor.LOCAL", "wendy-thor"},
		{"  wendy-thor.local  ", "wendy-thor"},
		{"wendy-thor", "wendy-thor"},
	}
	for _, c := range cases {
		if got := normalizeMDNSHost(c.in); got != c.want {
			t.Errorf("normalizeMDNSHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveMDNSHost_MatchesByHostname(t *testing.T) {
	orig := lanBrowseFn
	defer func() { lanBrowseFn = orig }()
	lanBrowseFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{Hostname: "other.local", IPAddress: "192.168.1.10"},
			// Trailing dot and mixed case must still match the saved default.
			{Hostname: "Wendy-Thor.local.", IPAddress: "192.168.1.50"},
		}, nil
	}

	got := resolveMDNSHost(context.Background(), "wendy-thor.local")
	if got != "192.168.1.50" {
		t.Fatalf("resolveMDNSHost = %q, want %q", got, "192.168.1.50")
	}
}

func TestResolveMDNSHost_NonLocalSkipsBrowse(t *testing.T) {
	orig := lanBrowseFn
	defer func() { lanBrowseFn = orig }()
	called := false
	lanBrowseFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		called = true
		return nil, nil
	}

	if got := resolveMDNSHost(context.Background(), "example.com"); got != "" {
		t.Fatalf("resolveMDNSHost(non-.local) = %q, want empty", got)
	}
	if called {
		t.Fatal("expected no mDNS browse for a non-.local host")
	}
}

func TestResolveMDNSHost_NoMatchReturnsEmpty(t *testing.T) {
	orig := lanBrowseFn
	defer func() { lanBrowseFn = orig }()
	lanBrowseFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		return []models.LANDevice{{Hostname: "other.local", IPAddress: "192.168.1.10"}}, nil
	}

	if got := resolveMDNSHost(context.Background(), "wendy-thor.local"); got != "" {
		t.Fatalf("resolveMDNSHost(no match) = %q, want empty", got)
	}
}

func TestResolveAddrOnce_LiteralIPUnchanged(t *testing.T) {
	if got := resolveAddrOnce(context.Background(), "192.168.1.50:50051"); got != "192.168.1.50:50051" {
		t.Fatalf("resolveAddrOnce(IP) = %q, want unchanged", got)
	}
}

func TestResolveAddrOnce_PrefersIPv4FromOSResolver(t *testing.T) {
	orig := osLookupHostFn
	defer func() { osLookupHostFn = orig }()
	osLookupHostFn = func(ctx context.Context, host string) ([]string, error) {
		return []string{"fe80::1", "192.168.1.50"}, nil
	}

	if got := resolveAddrOnce(context.Background(), "wendy-thor.local:50051"); got != "192.168.1.50:50051" {
		t.Fatalf("resolveAddrOnce = %q, want %q", got, "192.168.1.50:50051")
	}
}

// When the OS resolver can't see the mDNS ".local" name (the Windows/Linux
// failure mode behind issue #1155), resolveAddrOnce must fall back to an mDNS
// browse rather than returning the unresolvable name.
func TestResolveAddrOnce_FallsBackToMDNSWhenOSResolverFails(t *testing.T) {
	origLookup := osLookupHostFn
	origBrowse := lanBrowseFn
	defer func() {
		osLookupHostFn = origLookup
		lanBrowseFn = origBrowse
	}()

	osLookupHostFn = func(ctx context.Context, host string) ([]string, error) {
		return nil, errors.New("no such host")
	}
	lanBrowseFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		return []models.LANDevice{{Hostname: "wendy-thor.local", IPAddress: "192.168.1.50"}}, nil
	}

	if got := resolveAddrOnce(context.Background(), "wendy-thor.local:50051"); got != "192.168.1.50:50051" {
		t.Fatalf("resolveAddrOnce = %q, want %q", got, "192.168.1.50:50051")
	}
}

func TestDefaultDeviceUnreachableError(t *testing.T) {
	cause := errors.New("connection refused")
	err := defaultDeviceUnreachableError("wendyos-workshop-16.local", cause)
	if !errors.Is(err, cause) {
		t.Fatal("expected wrapped error to unwrap to the underlying cause")
	}
	msg := err.Error()
	for _, want := range []string{"wendyos-workshop-16.local", "set but could not be reached", "get-default"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}
