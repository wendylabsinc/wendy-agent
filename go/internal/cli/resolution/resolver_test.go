package resolution

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestResolve_LiteralIP(t *testing.T) {
	ctx := context.Background()
	candidates, results, err := Resolve(ctx, "192.168.1.42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].Source != SourceLiteralIP {
		t.Errorf("expected SourceLiteralIP, got %q", candidates[0].Source)
	}
	if candidates[0].Port != defaultAgentPort {
		t.Errorf("expected port %d, got %d", defaultAgentPort, candidates[0].Port)
	}
	if results[SourceLiteralIP] == "" {
		t.Error("expected sourceResults entry for SourceLiteralIP")
	}
}

func TestResolve_LiteralIP_WithPort(t *testing.T) {
	ctx := context.Background()
	candidates, _, err := Resolve(ctx, "10.0.0.1:9000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].Port != 9000 {
		t.Errorf("expected port 9000, got %d", candidates[0].Port)
	}
	if candidates[0].Source != SourceLiteralIP {
		t.Errorf("expected SourceLiteralIP, got %q", candidates[0].Source)
	}
}

func TestResolve_LocalHostname_DNSSkipped(t *testing.T) {
	// Replace mDNS discovery with a stub that returns nothing.
	orig := discoverLANFn
	discoverLANFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		return nil, nil
	}
	defer func() { discoverLANFn = orig }()

	ctx := context.Background()
	_, results, _ := Resolve(ctx, "mydevice.local")

	if results[SourceDNS] != "skipped (.local hostname)" {
		t.Errorf("expected DNS skipped message, got %q", results[SourceDNS])
	}
}

func TestResolve_NonLocalHostname_MDNSSkipped(t *testing.T) {
	orig := discoverLANFn
	discoverLANFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		t.Error("mDNS should not be called for non-.local hostname")
		return nil, nil
	}
	defer func() { discoverLANFn = orig }()

	ctx := context.Background()
	_, results, _ := Resolve(ctx, "somehost.example.com")

	if results[SourceMDNS] != "skipped (not a .local hostname)" {
		t.Errorf("expected mDNS skipped message, got %q", results[SourceMDNS])
	}
}

func TestResolve_MDNS_Match(t *testing.T) {
	orig := discoverLANFn
	discoverLANFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{
				Hostname:  "mydevice.local",
				IPAddress: "192.168.1.10",
				Port:      50052,
				IsMTLS:    true,
			},
		}, nil
	}
	defer func() { discoverLANFn = orig }()

	ctx := context.Background()
	candidates, results, err := Resolve(ctx, "mydevice.local")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	c := candidates[0]
	if c.Source != SourceMDNS {
		t.Errorf("expected SourceMDNS, got %q", c.Source)
	}
	// mTLS port 50052 should be decremented to 50051.
	if c.Port != 50051 {
		t.Errorf("expected plaintext port 50051, got %d", c.Port)
	}
	if c.IP.String() != "192.168.1.10" {
		t.Errorf("expected 192.168.1.10, got %q", c.IP.String())
	}
	if results[SourceMDNS] != "1 candidate(s) from mDNS" {
		t.Errorf("unexpected mDNS result: %q", results[SourceMDNS])
	}
}

func TestResolve_MDNS_NoMatch(t *testing.T) {
	orig := discoverLANFn
	discoverLANFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{Hostname: "other.local", IPAddress: "192.168.1.99", Port: 50051},
		}, nil
	}
	defer func() { discoverLANFn = orig }()

	ctx := context.Background()
	_, results, _ := Resolve(ctx, "mydevice.local")

	if results[SourceMDNS] != "no response" {
		t.Errorf("expected 'no response', got %q", results[SourceMDNS])
	}
}

func TestResolve_Deduplication(t *testing.T) {
	// Two mDNS results with the same IP/port/zone should deduplicate to one candidate.
	orig := discoverLANFn
	discoverLANFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{Hostname: "mydevice.local", IPAddress: "192.168.1.10", Port: 50051},
			{Hostname: "mydevice.local.", IPAddress: "192.168.1.10", Port: 50051},
		}, nil
	}
	defer func() { discoverLANFn = orig }()

	ctx := context.Background()
	candidates, _, err := Resolve(ctx, "mydevice.local")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Errorf("expected 1 candidate after dedup, got %d", len(candidates))
	}
}

func TestCandidate_Addr_IPv4(t *testing.T) {
	c := Candidate{
		IP:   netip.MustParseAddr("10.0.0.1"),
		Port: 50051,
	}
	if got := c.Addr(); got != "10.0.0.1:50051" {
		t.Errorf("expected '10.0.0.1:50051', got %q", got)
	}
}

func TestCandidate_Addr_IPv6(t *testing.T) {
	c := Candidate{
		IP:   netip.MustParseAddr("::1"),
		Port: 50051,
	}
	if got := c.Addr(); got != "[::1]:50051" {
		t.Errorf("expected '[::1]:50051', got %q", got)
	}
}

func TestCandidate_Addr_LinkLocalIPv6WithZone(t *testing.T) {
	c := Candidate{
		IP:   netip.MustParseAddr("fe80::1"),
		Port: 50051,
		Zone: "eth0",
	}
	if got := c.Addr(); got != "[fe80::1%eth0]:50051" {
		t.Errorf("expected '[fe80::1%%eth0]:50051', got %q", got)
	}
}

func TestHostAndPort_WithPort(t *testing.T) {
	h, p := hostAndPort("mydevice.local:9000")
	if h != "mydevice.local" {
		t.Errorf("expected 'mydevice.local', got %q", h)
	}
	if p != 9000 {
		t.Errorf("expected 9000, got %d", p)
	}
}

func TestHostAndPort_NoPort(t *testing.T) {
	h, p := hostAndPort("mydevice.local")
	if h != "mydevice.local" {
		t.Errorf("expected 'mydevice.local', got %q", h)
	}
	if p != defaultAgentPort {
		t.Errorf("expected %d, got %d", defaultAgentPort, p)
	}
}
