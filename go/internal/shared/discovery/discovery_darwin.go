//go:build darwin

package discovery

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// discoverLAN uses the macOS dns-sd command to browse for WendyOS devices.
// This works across all network interfaces including USB host-mode connections,
// unlike raw multicast libraries which miss interfaces the system resolver covers.
func discoverLAN(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
	if timeout == 0 {
		timeout = defaultTimeout
	}

	browseCtx, browseCancel := context.WithTimeout(ctx, timeout)
	defer browseCancel()

	instances, err := dnssdBrowse(browseCtx, wendyServiceType)
	if err != nil {
		return nil, err
	}
	// darwinInterfaceDisplayNameMap shells out to networksetup once per scan and
	// returns a map reused across all dnssdResolve calls below — not per instance.
	interfaceDisplayNames := darwinInterfaceDisplayNameMap(ctx)
	linkSpeeds := make(map[string]string)

	var devices []models.LANDevice
	indexes := make(map[string]int)

	for _, inst := range instances {
		resolveCtx, resolveCancel := context.WithTimeout(ctx, 2*time.Second)
		dev, err := dnssdResolve(resolveCtx, inst, interfaceDisplayNames, linkSpeeds)
		resolveCancel()
		if err != nil {
			// Resolve failed (e.g. could not parse hostname) — fall back to
			// a device derived from the browse instance name.
			dev = deviceFromBrowse(inst, interfaceDisplayNames)
		}

		key := fmt.Sprintf("%s-%s-%d", dev.DisplayName, dev.Hostname, dev.Port)
		devices = appendPreferredLANDevice(devices, indexes, key, dev)
	}

	return devices, nil
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

// dnssdBrowse runs dns-sd -B and returns as soon as results stop arriving.
// It uses a short settle timer: once the first result arrives, it waits up to
// 500ms for more results before returning. This avoids waiting for the full timeout.
func dnssdBrowse(ctx context.Context, serviceType string) ([]browseResult, error) {
	cmd := exec.CommandContext(ctx, "dns-sd", "-B", serviceType, "local")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Parse lines on a channel so we can select with a timer.
	lineCh := make(chan browseResult, 16)

	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if result, ok := parseBrowseLine(scanner.Text()); ok {
				lineCh <- result
			}
		}
	}()

	var results []browseResult
	seen := make(map[string]bool)

	// Wait up to the context deadline for the first result.
	// Once we get one, use a short settle timer for additional results.
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return results, nil
		case result, open := <-lineCh:
			if !open {
				_ = cmd.Wait()
				return results, nil
			}
			key := result.instanceName + "%" + result.interfaceName
			if !seen[key] {
				seen[key] = true
				results = append(results, result)
				// Reset settle timer: wait 500ms for more results.
				settle = time.After(500 * time.Millisecond)
			}
		case <-settle:
			// No new results in 500ms, we're done.
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return results, nil
		}
	}
}

// dnssdResolve resolves an instance via dns-sd -L and maps the result to a
// wendy LANDevice, interpreting the wendy-specific TXT records.
func dnssdResolve(ctx context.Context, inst browseResult, interfaceDisplayNames map[string]string, linkSpeeds map[string]string) (models.LANDevice, error) {
	svc, err := dnssdResolveService(ctx, inst, wendyServiceType)
	if err != nil {
		return models.LANDevice{}, err
	}

	displayName := strings.TrimSuffix(svc.hostname, ".local")
	if dn, ok := svc.txtRecords["displayname"]; ok {
		displayName = dn
	}

	id := ""
	if v, ok := svc.txtRecords["wendyosdevice"]; ok {
		id = v
	} else if v, ok := svc.txtRecords["id"]; ok {
		id = v
	}
	if id == "" {
		id = displayName
	}

	dev := models.LANDevice{
		ID:            id,
		DisplayName:   displayName,
		Hostname:      svc.hostname,
		Port:          svc.port,
		IsMTLS:        svc.txtRecords["tls"] == "true",
		InterfaceType: string(models.InterfaceLAN),
		IsWendyDevice: true,
	}
	setAssetID(&dev, svc.txtRecords)
	setLANNetworkInterface(&dev, inst.interfaceName, interfaceDisplayNames[inst.interfaceName], darwinCachedInterfaceLinkSpeed(ctx, inst.interfaceName, linkSpeeds))
	return dev, nil
}

// setAssetID parses the assetid TXT record into dev.AssetID. Only positive
// values are accepted; 0 (or an absent/unparseable record) leaves AssetID at
// its zero value, meaning unknown or unprovisioned.
func setAssetID(dev *models.LANDevice, txtRecords map[string]string) {
	if v, ok := txtRecords["assetid"]; ok {
		if id, err := strconv.ParseInt(v, 10, 32); err == nil && id > 0 {
			dev.AssetID = int32(id)
		}
	}
}

func darwinCachedInterfaceLinkSpeed(ctx context.Context, interfaceName string, linkSpeeds map[string]string) string {
	if interfaceName == "" {
		return ""
	}
	if linkSpeed, ok := linkSpeeds[interfaceName]; ok {
		return linkSpeed
	}
	linkSpeed := getInterfaceLinkSpeed(ctx, interfaceName)
	linkSpeeds[interfaceName] = linkSpeed
	return linkSpeed
}

// deviceFromBrowse builds a LANDevice from browse results alone, without
// resolving via dns-sd -L. Used as a fallback when resolve fails (e.g.
// the service has no TXT records).

var hostnameLabelRegexp = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// isValidHostnameLabel reports whether s is a valid RFC1123 hostname label.
func isValidHostnameLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	return hostnameLabelRegexp.MatchString(s)
}

// unescapeDNSSDName converts dns-sd's backslash-escaped spaces ("\ ") back to
// regular spaces, matching the display form of an mDNS service instance name.
func unescapeDNSSDName(s string) string {
	return strings.ReplaceAll(s, `\ `, " ")
}

func deviceFromBrowse(inst browseResult, interfaceDisplayNames map[string]string) models.LANDevice {
	displayName := unescapeDNSSDName(inst.instanceName)

	var (
		id       string
		hostname string
		port     int
	)

	// Only synthesize a hostname/ID when the instance name is already a valid
	// hostname label. Otherwise, leave Hostname empty and Port zero to avoid
	// exposing a misleading dialable pair.
	if isValidHostnameLabel(inst.instanceName) {
		id = inst.instanceName
		hostname = inst.instanceName + ".local"
		port = 50051
	}

	dev := models.LANDevice{
		ID:            id,
		DisplayName:   displayName,
		Hostname:      hostname,
		Port:          port,
		InterfaceType: string(models.InterfaceLAN),
		IsWendyDevice: true,
	}
	setLANNetworkInterface(&dev, inst.interfaceName, interfaceDisplayNames[inst.interfaceName], "")
	return dev
}

// discoverLANContinuous keeps dns-sd -B running and sends each newly
// discovered device to ch as it's resolved. Runs until ctx is cancelled.
// linkSpeeds is intentionally not shared across goroutines — each call to
// discoverLANContinuous owns its own map, and dnssdResolve is called
// synchronously within the same goroutine, so no mutex is required.
func discoverLANContinuous(ctx context.Context, ch chan<- models.LANDevice) {
	defer close(ch)
	interfaceDisplayNames := darwinInterfaceDisplayNameMap(ctx)
	linkSpeeds := make(map[string]string)

	cmd := exec.CommandContext(ctx, "dns-sd", "-B", wendyServiceType, "local")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}

	seen := make(map[string]bool)
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		inst, ok := parseBrowseLine(scanner.Text())
		if !ok {
			continue
		}

		key := inst.instanceName + "%" + inst.interfaceName
		if seen[key] {
			continue
		}
		seen[key] = true

		resolveCtx, resolveCancel := context.WithTimeout(ctx, 2*time.Second)
		dev, err := dnssdResolve(resolveCtx, inst, interfaceDisplayNames, linkSpeeds)
		resolveCancel()
		if err != nil {
			dev = deviceFromBrowse(inst, interfaceDisplayNames)
		}

		select {
		case ch <- dev:
		case <-ctx.Done():
			return
		}
	}

	_ = cmd.Wait()
}
