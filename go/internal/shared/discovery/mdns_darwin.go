//go:build darwin

package discovery

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
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
