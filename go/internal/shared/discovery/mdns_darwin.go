//go:build darwin

package discovery

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// BrowseMDNSServices discovers mDNS services of the given type on macOS
// using dns-sd. Returns all services found within the timeout.
func BrowseMDNSServices(ctx context.Context, serviceType string, timeout time.Duration) ([]MDNSService, error) {
	if timeout == 0 {
		timeout = defaultTimeout
	}

	browseCtx, browseCancel := context.WithTimeout(ctx, timeout)
	defer browseCancel()

	instances, err := dnssdBrowse(browseCtx, serviceType)
	if err != nil {
		return nil, err
	}

	var services []MDNSService
	seen := make(map[string]bool)

	for _, inst := range instances {
		resolveCtx, resolveCancel := context.WithTimeout(ctx, 2*time.Second)
		svc, err := resolveMDNSService(resolveCtx, inst, serviceType)
		resolveCancel()
		if err != nil {
			continue
		}

		key := fmt.Sprintf("%s-%s-%d", svc.InstanceName, svc.Hostname, svc.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		services = append(services, svc)
	}

	return services, nil
}

type browseResult struct {
	instanceName  string
	domain        string
	interfaceName string
}

// parseBrowseLine parses one line of dns-sd -B output. It returns ok=false
// for lines that are not "Add" records or are too short to parse.
func parseBrowseLine(line string) (browseResult, bool) {
	if !strings.Contains(line, "Add") {
		return browseResult{}, false
	}
	fields := strings.Fields(line)
	if len(fields) < 7 {
		return browseResult{}, false
	}
	interfaceName := ""
	if interfaceIndex, err := strconv.Atoi(fields[3]); err == nil {
		// Silently falls through with empty interfaceName when the
		// interface disappears between browse and resolve; USB detection
		// will be skipped for that device rather than returning an error.
		if iface, ifaceErr := net.InterfaceByIndex(interfaceIndex); ifaceErr == nil {
			interfaceName = iface.Name
		}
	}
	return browseResult{
		instanceName:  strings.Join(fields[6:], " "),
		domain:        fields[4],
		interfaceName: interfaceName,
	}, true
}

// dnssdBrowseStream starts dns-sd -B for serviceType and streams each newly
// discovered instance to the returned channel. Duplicate (instance, interface)
// pairs are emitted only once. The channel is closed when ctx is cancelled or
// the dns-sd process exits; the process is killed and reaped on all paths.
// Consumers signal they are done by cancelling ctx.
//
// This function is intentionally generic mDNS browsing: keep it free of
// anything wendy-specific (service types, TXT record interpretation, device
// models) — callers layer that on top.
func dnssdBrowseStream(ctx context.Context, serviceType string) (<-chan browseResult, error) {
	cmd := exec.CommandContext(ctx, "dns-sd", "-B", serviceType, "local")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ch := make(chan browseResult, 16)
	go func() {
		defer close(ch)
		defer func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}()

		seen := make(map[string]bool)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			result, ok := parseBrowseLine(scanner.Text())
			if !ok {
				continue
			}
			key := result.instanceName + "%" + result.interfaceName
			if seen[key] {
				continue
			}
			seen[key] = true

			select {
			case ch <- result:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// dnssdBrowse runs dns-sd -B and returns as soon as results stop arriving.
// It uses a short settle timer: once the first result arrives, it waits up to
// 500ms for more results before returning. This avoids waiting for the full timeout.
func dnssdBrowse(ctx context.Context, serviceType string) ([]browseResult, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	browseCh, err := dnssdBrowseStream(streamCtx, serviceType)
	if err != nil {
		return nil, err
	}

	var results []browseResult

	// Wait up to the context deadline for the first result.
	// Once we get one, use a short settle timer for additional results.
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return results, nil
		case result, open := <-browseCh:
			if !open {
				return results, nil
			}
			results = append(results, result)
			// Reset settle timer: wait 500ms for more results.
			settle = time.After(500 * time.Millisecond)
		case <-settle:
			// No new results in 500ms, we're done.
			return results, nil
		}
	}
}

// resolvedService is the raw result of a dns-sd -L resolve: exactly what the
// SRV + TXT records provide. No IP address — resolving the hostname to an
// address is the caller's concern.
type resolvedService struct {
	instanceName string
	hostname     string
	port         int
	txtRecords   map[string]string
}

// parseDNSSDTXT extracts key=value pairs from a dns-sd TXT record line into txt.
// dns-sd escapes spaces inside values as "\ "; we split on unescaped
// whitespace so that values like "Dynamic\ Cosmos" round-trip correctly.
func parseDNSSDTXT(line string, txt map[string]string) {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && (line[i+1] == ' ' || line[i+1] == '\t') {
			cur.WriteByte(line[i+1])
			i++
		} else if line[i] == ' ' || line[i] == '\t' {
			if cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
		} else {
			cur.WriteByte(line[i])
		}
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	for _, field := range fields {
		if k, v, ok := strings.Cut(field, "="); ok {
			txt[k] = v
		}
	}
}

// dnssdResolveService runs dns-sd -L to resolve a browse result to hostname,
// port, and TXT records. Returns as soon as the "can be reached at" line is parsed.
//
// This function is intentionally generic mDNS resolution: keep it free of
// anything wendy-specific (service types, TXT record interpretation, device
// models) — callers layer that on top.
func dnssdResolveService(ctx context.Context, inst browseResult, serviceType string) (resolvedService, error) {
	cmd := exec.CommandContext(ctx, "dns-sd", "-L", inst.instanceName, serviceType, inst.domain)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return resolvedService{}, err
	}
	if err := cmd.Start(); err != nil {
		return resolvedService{}, err
	}

	var hostname string
	var port int
	txtRecords := make(map[string]string)

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(line, "can be reached at") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "at" && i+1 < len(parts) {
					hostPort := parts[i+1]
					h, portStr, splitErr := net.SplitHostPort(hostPort)
					if splitErr == nil {
						hostname = strings.TrimSuffix(h, ".")
						fmt.Sscanf(portStr, "%d", &port)
					}
					break
				}
			}

			// TXT records on the same line (some versions).
			parseDNSSDTXT(line, txtRecords)

			// TXT records are typically on the next line indented with a space.
			if scanner.Scan() {
				parseDNSSDTXT(scanner.Text(), txtRecords)
			}

			_ = cmd.Process.Kill()
			break
		}
	}

	_ = cmd.Wait()

	if hostname == "" {
		return resolvedService{}, fmt.Errorf("could not resolve instance %q", inst.instanceName)
	}

	return resolvedService{
		instanceName: inst.instanceName,
		hostname:     hostname,
		port:         port,
		txtRecords:   txtRecords,
	}, nil
}

// resolveMDNSService resolves a browse result into an MDNSService, including
// a best-effort IP address lookup for the resolved hostname.
func resolveMDNSService(ctx context.Context, inst browseResult, serviceType string) (MDNSService, error) {
	svc, err := dnssdResolveService(ctx, inst, serviceType)
	if err != nil {
		return MDNSService{}, err
	}

	ipAddr := ""
	if addrs, lookupErr := net.LookupHost(svc.hostname); lookupErr == nil {
		ipAddr = preferIPv4Addr(addrs)
	}

	return MDNSService{
		InstanceName: svc.instanceName,
		Hostname:     svc.hostname,
		IPAddress:    ipAddr,
		Port:         svc.port,
		TXTRecords:   svc.txtRecords,
	}, nil
}
