package commands

import (
	"context"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// usbDirectProbeBudget bounds one well-known-address probe. A dead candidate
// fails within one NDP neighbor-resolution timeout (~3s); a live agent answers
// the TCP SYN immediately but its ML-DSA mTLS handshake can take several
// seconds on Jetson-class hardware (see mtlsProbeTimeout), hence the headroom.
const usbDirectProbeBudget = 8 * time.Second

// usbDirectCandidatesFn is a seam for tests.
var usbDirectCandidatesFn = discovery.USBDirectCandidates

// probeUSBDirectDevices dials the well-known USB link-local address on every
// USB-backed host interface in parallel and returns a LANDevice for each Wendy
// agent that answers. Devices on WendyOS images without the well-known address
// (or with no agent listening) simply never answer; the caller's mDNS path
// still covers those. Identity comes from GetAgentVersion instead of mDNS TXT
// records, so no multicast needs to work on the link.
func probeUSBDirectDevices(ctx context.Context) []models.LANDevice {
	candidates := usbDirectCandidatesFn()
	if len(candidates) == 0 {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, usbDirectProbeBudget)
	defer cancel()

	results := make([]*models.LANDevice, len(candidates))
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		go func(i int, cand discovery.USBDirectCandidate) {
			defer wg.Done()
			isMTLS, resp, err := getAgentVersionAtAddress(pctx, cand.HostPort(defaultAgentPort))
			if err != nil || resp == nil {
				return
			}
			hostname := discovery.SanitiseDisplayName(resp.GetHostname())
			dev := models.LANDevice{
				DisplayName:      hostname,
				IPAddress:        discovery.WellKnownUSBAddr + "%" + cand.Zone,
				Port:             defaultAgentPort,
				IsMTLS:           isMTLS,
				InterfaceType:    string(models.InterfaceLAN),
				NetworkInterface: cand.Interface,
				USB:              cand.Interface,
				IsWendyDevice:    true,
				AgentVersion:     resp.GetVersion(),
				OS:               resp.GetOs(),
				OSVersion:        resp.GetOsVersion(),
				CPUArchitecture:  resp.GetCpuArchitecture(),
				DeviceType:       resp.GetDeviceType(),
			}
			if hostname != "" {
				dev.Hostname = hostname + ".local"
			} else {
				// Pre-hostname-field agent: still show it, identified by port.
				dev.DisplayName = "USB device (" + cand.Interface + ")"
			}
			results[i] = &dev
		}(i, candidates[i])
	}
	wg.Wait()

	var out []models.LANDevice
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// shouldProbeUSBDirect mirrors Discover's type filter: the probe produces
// LAN-type devices, so it runs whenever LAN discovery would.
func shouldProbeUSBDirect(opts discovery.DiscoveryOptions) bool {
	if len(opts.Types) == 0 {
		return true
	}
	for _, t := range opts.Types {
		if t == models.InterfaceLAN {
			return true
		}
	}
	return false
}

// discoverWithUSBDirect runs standard discovery and the USB well-known-address
// probe concurrently, then merges the probe results into the LAN device list.
// Drop-in replacement for discovery.Discover at command call sites.
func discoverWithUSBDirect(ctx context.Context, opts discovery.DiscoveryOptions) (*models.DevicesCollection, error) {
	var probed []models.LANDevice
	var wg sync.WaitGroup
	if shouldProbeUSBDirect(opts) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probed = probeUSBDirectDevices(ctx)
		}()
	}
	collection, err := discovery.Discover(ctx, opts)
	wg.Wait()
	if err == nil && len(probed) > 0 {
		collection.LANDevices = mergeUSBDirectDevices(collection.LANDevices, probed)
	}
	return collection, err
}

// mergeUSBDirectDevices folds direct-probe results into an mDNS-discovered LAN
// device list. A probed device whose hostname matches an existing entry
// enriches it in place (USB annotation, interface); identity fields and the
// mDNS-advertised address are preserved, since preferDiscoveredLANDevice
// scoring and lanAgentAddresses ordering already know how to dial USB-annotated
// devices. A probed device with no counterpart is appended — that is the case
// where mDNS is broken and the direct probe is the only path.
func mergeUSBDirectDevices(devices, probed []models.LANDevice) []models.LANDevice {
	for _, p := range probed {
		matched := false
		for i := range devices {
			sameHost := p.Hostname != "" && normalizeMDNSHost(devices[i].Hostname) == normalizeMDNSHost(p.Hostname)
			sameIface := devices[i].NetworkInterface != "" && devices[i].NetworkInterface == p.NetworkInterface
			if !sameHost && !sameIface {
				continue
			}
			matched = true
			if devices[i].USB == "" {
				devices[i].USB = p.USB
			}
			if devices[i].NetworkInterface == "" {
				devices[i].NetworkInterface = p.NetworkInterface
			}
			if devices[i].IPAddress == "" {
				devices[i].IPAddress = p.IPAddress
			}
			if devices[i].AgentVersion == "" {
				devices[i].AgentVersion = p.AgentVersion
			}
			break
		}
		if !matched {
			devices = append(devices, p)
		}
	}
	return devices
}

// usbDirectConnectFn is a seam for tests.
var usbDirectConnectFn = connectWithAutoTLS

// usbDirectFallback attempts to reach the requested device over the USB
// well-known address after normal resolution/connection failed (e.g. mDNS
// broken on this host, stale stored address). It returns a live connection
// ONLY when the device's reported hostname matches the requested one; an
// empty hostname (agent predating the field) never matches — connecting to
// whichever device happens to be plugged in would silently target the wrong
// machine.
func usbDirectFallback(ctx context.Context, wantHost string) (*grpcclient.AgentConnection, bool) {
	want := normalizeMDNSHost(wantHost)
	if want == "" {
		return nil, false
	}
	for _, cand := range usbDirectCandidatesFn() {
		pctx, cancel := context.WithTimeout(ctx, usbDirectProbeBudget)
		conn, err := usbDirectConnectFn(pctx, cand.HostPort(defaultAgentPort))
		if err != nil {
			cancel()
			continue
		}
		resp, verr := conn.AgentService.GetAgentVersion(pctx, &agentpb.GetAgentVersionRequest{})
		cancel()
		if verr != nil || resp.GetHostname() == "" || normalizeMDNSHost(resp.GetHostname()) != want {
			conn.Close()
			continue
		}
		return conn, true
	}
	return nil, false
}
