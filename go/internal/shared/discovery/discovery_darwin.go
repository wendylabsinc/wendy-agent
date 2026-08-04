//go:build darwin

package discovery

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// discoverLAN browses for WendyOS devices through mDNSResponder.
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

// dnssdBrowseSettle is how long a browse waits for further results after one
// arrives. Browsing is open-ended, so this is what bounds a scan on a network
// that has already answered.
const dnssdBrowseSettle = 500 * time.Millisecond

// dnssdResolve resolves a browse result into a LANDevice.
func dnssdResolve(ctx context.Context, inst browseResult, interfaceDisplayNames map[string]string, linkSpeeds map[string]string) (models.LANDevice, error) {
	hostname, port, txtRecords, err := dnssdResolveInstance(ctx, inst, wendyServiceType)
	if err != nil {
		return models.LANDevice{}, err
	}

	displayName := strings.TrimSuffix(hostname, ".local")
	if dn, ok := txtRecords["displayname"]; ok {
		displayName = dn
	}

	id := ""
	if v, ok := txtRecords["wendyosdevice"]; ok {
		id = v
	} else if v, ok := txtRecords["id"]; ok {
		id = v
	}
	if id == "" {
		id = displayName
	}

	dev := models.LANDevice{
		ID:            id,
		DisplayName:   displayName,
		Hostname:      hostname,
		Port:          port,
		IsMTLS:        txtRecords["tls"] == "true",
		InterfaceType: string(models.InterfaceLAN),
		IsWendyDevice: true,
	}
	setAssetID(&dev, txtRecords)
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

func deviceFromBrowse(inst browseResult, interfaceDisplayNames map[string]string) models.LANDevice {
	// Instance names arrive decoded from mDNSResponder, matching the Linux and
	// Windows backends — no display-format unescaping to undo.
	displayName := inst.instanceName

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

// discoverLANContinuous keeps a browse open and sends each newly discovered
// device to ch as it's resolved. Runs until ctx is cancelled.
// linkSpeeds is intentionally not shared across goroutines — each call to
// discoverLANContinuous owns its own map, and dnssdResolve is called
// synchronously from the browse callback, so no mutex is required.
func discoverLANContinuous(ctx context.Context, ch chan<- models.LANDevice) {
	defer close(ch)
	interfaceDisplayNames := darwinInterfaceDisplayNameMap(ctx)
	linkSpeeds := make(map[string]string)

	seen := make(map[string]bool)

	// Resolving inside the callback blocks the browse socket pump for up to the
	// resolve timeout. That matches the previous sequential behaviour, and
	// mDNSResponder queues further browse replies meanwhile.
	_ = dnssdBrowseStream(ctx, wendyServiceType, func(inst browseResult) {
		key := inst.instanceName + "%" + inst.interfaceName
		if seen[key] {
			return
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
		}
	})
}
