package resolution

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

const defaultAgentPort = 50051

// discoverLANFn is the mDNS discovery function; replaced in tests.
var discoverLANFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
	return discovery.DiscoverLAN(ctx, timeout)
}

// Resolve returns the full candidate set for target, trying up to four
// strategies and merging results. The second return value maps each Source to a
// human-readable description of the strategy's result. An error is returned
// only when every strategy produced zero candidates; partial failures are
// captured in sourceResults.
func Resolve(ctx context.Context, target string) ([]Candidate, map[Source]string, error) {
	host, port := hostAndPort(target)
	sourceResults := make(map[Source]string)

	// Strategy 1: literal IP — return immediately if target is already an IP.
	if ip, err := netip.ParseAddr(host); err == nil {
		c := Candidate{
			IP:     ip,
			Port:   port,
			Source: SourceLiteralIP,
		}
		sourceResults[SourceLiteralIP] = "literal IP address"
		return []Candidate{c}, sourceResults, nil
	}

	var all []Candidate

	// Strategy 2: mDNS — only for .local hostnames.
	if strings.HasSuffix(host, ".local") {
		candidates, result := resolveMDNS(ctx, host, port)
		sourceResults[SourceMDNS] = result
		all = append(all, candidates...)
	} else {
		sourceResults[SourceMDNS] = "skipped (not a .local hostname)"
	}

	// Strategy 3: system DNS — skip for .local hostnames.
	if strings.HasSuffix(host, ".local") {
		sourceResults[SourceDNS] = "skipped (.local hostname)"
	} else {
		candidates, result := resolveDNS(ctx, host, port)
		sourceResults[SourceDNS] = result
		all = append(all, candidates...)
	}

	// Strategy 4: config cache.
	candidates, result := resolveCache(host, port)
	sourceResults[SourceCache] = result
	all = append(all, candidates...)

	// De-duplicate by (IP, Port, Zone).
	all = dedup(all)

	if len(all) == 0 {
		return nil, sourceResults, &ResolutionError{Target: target, SourceResults: sourceResults}
	}

	return all, sourceResults, nil
}

// normalizeHost strips a trailing dot and lowercases for comparison.
func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// resolveMDNS runs mDNS discovery and filters by exact hostname match.
func resolveMDNS(ctx context.Context, host string, defaultPort uint16) ([]Candidate, string) {
	timeout := mdnsTimeout()
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	devices, err := discoverLANFn(tctx, timeout)
	if err != nil {
		return nil, fmt.Sprintf("discovery error: %v", err)
	}

	wantHost := normalizeHost(host)

	var candidates []Candidate
	for _, dev := range devices {
		devHost := normalizeHost(dev.Hostname)
		if devHost != wantHost {
			continue
		}
		if dev.IPAddress == "" {
			continue
		}
		ip, err := netip.ParseAddr(dev.IPAddress)
		if err != nil {
			continue
		}

		port := defaultPort
		if dev.Port > 0 {
			port = uint16(dev.Port)
			if dev.IsMTLS && dev.Port > 1 {
				port-- // advertised port is mTLS; subtract offset to get plaintext port
			}
		}

		zone := ""
		if ip.IsLinkLocalUnicast() && dev.NetworkInterface != "" {
			zone = dev.NetworkInterface
		}

		candidates = append(candidates, Candidate{
			IP:        ip,
			Port:      port,
			Zone:      zone,
			Source:    SourceMDNS,
			Interface: dev.NetworkInterface,
		})
	}

	if len(candidates) == 0 {
		return nil, "no response"
	}
	return candidates, fmt.Sprintf("%d candidate(s) from mDNS", len(candidates))
}

// resolveDNS resolves the host via the system DNS resolver.
func resolveDNS(ctx context.Context, host string, port uint16) ([]Candidate, string) {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, fmt.Sprintf("DNS error: %v", err)
	}

	var candidates []Candidate
	for _, addr := range addrs {
		ip, err := netip.ParseAddr(addr)
		if err != nil {
			continue
		}
		// Link-local addresses from DNS carry no interface/zone context and
		// cannot be dialed without one; skip them.
		if ip.IsLinkLocalUnicast() {
			continue
		}
		candidates = append(candidates, Candidate{
			IP:     ip,
			Port:   port,
			Source: SourceDNS,
		})
	}

	if len(candidates) == 0 {
		return nil, "no addresses returned"
	}
	return candidates, fmt.Sprintf("%d candidate(s) from DNS", len(candidates))
}

// resolveCache checks the CLI config for a cached endpoint matching the target host.
func resolveCache(targetHost string, port uint16) ([]Candidate, string) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "config error"
	}
	if cfg.DefaultDevice == "" || cfg.DefaultDeviceEndpoint == "" {
		return nil, "none"
	}
	// Only use cache when this target matches the saved default device.
	savedHost, _, _ := net.SplitHostPort(cfg.DefaultDevice)
	if savedHost == "" {
		savedHost = cfg.DefaultDevice
	}
	if !strings.EqualFold(strings.TrimSuffix(targetHost, "."), strings.TrimSuffix(savedHost, ".")) {
		return nil, "none (different host)"
	}
	cachedHost, cachedPortStr, err := net.SplitHostPort(cfg.DefaultDeviceEndpoint)
	if err != nil {
		return nil, "none (malformed endpoint)"
	}
	ip, err := netip.ParseAddr(cachedHost)
	if err != nil {
		return nil, "none (unparseable IP in cache)"
	}
	cachedPort, err := strconv.ParseUint(cachedPortStr, 10, 16)
	if err != nil {
		return nil, "none (unparseable port in cache)"
	}
	c := Candidate{
		IP:     ip,
		Port:   uint16(cachedPort),
		Source: SourceCache,
	}
	return []Candidate{c}, fmt.Sprintf("1 candidate (%s)", cfg.DefaultDeviceEndpoint)
}

// hostAndPort splits a target string into host and port. If no port is found,
// defaultAgentPort is returned.
func hostAndPort(target string) (host string, port uint16) {
	h, p, err := net.SplitHostPort(target)
	if err != nil {
		return target, defaultAgentPort
	}
	n, err := strconv.ParseUint(p, 10, 16)
	if err != nil || n == 0 {
		return h, defaultAgentPort
	}
	return h, uint16(n)
}

// dedup removes duplicate candidates by (IP, Port, Zone).
func dedup(candidates []Candidate) []Candidate {
	type key struct {
		ip   string
		port uint16
		zone string
	}
	seen := make(map[key]struct{}, len(candidates))
	out := candidates[:0:0]
	for _, c := range candidates {
		k := key{c.IP.String(), c.Port, c.Zone}
		if _, exists := seen[k]; exists {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	return out
}

const maxMDNSTimeout = 30 * time.Second

// mdnsTimeout returns the mDNS scan duration from WENDY_MDNS_TIMEOUT env var,
// falling back to 2 seconds. Values above 30 seconds are clamped to prevent
// a misconfigured or malicious env var from hanging all CLI invocations.
func mdnsTimeout() time.Duration {
	if v := os.Getenv("WENDY_MDNS_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			if d > maxMDNSTimeout {
				d = maxMDNSTimeout
			}
			return d
		}
	}
	return 2 * time.Second
}
