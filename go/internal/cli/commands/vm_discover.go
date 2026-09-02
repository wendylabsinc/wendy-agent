package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/vm"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// vmDeviceIDPrefix marks a discovery row as a local VM rather than a device on
// the network. Dotless, so resolveTargetInner still treats it as a provider-ish
// key rather than an address.
const vmDeviceIDPrefix = "vm:"

// vmProbeBudget bounds the whole sweep, matching usbDirectProbeBudget's role
// for the USB probe.
const vmProbeBudget = 8 * time.Second

// vmStatusesFn reports what the local VM store holds. A seam so tests can
// describe a set of VMs directly, rather than having to hold a real run lock
// to make one look running.
var vmStatusesFn = func() ([]vm.Status, error) {
	store, err := vm.NewStore()
	if err != nil {
		return nil, err
	}
	return store.Statuses()
}

// probeLocalVMDevices returns a row for every running user-mode VM whose agent
// answers on its forwarded loopback port.
//
// Shared-mode VMs are skipped: mDNS already finds them, so a row here would
// list them twice. User-mode VMs are the ones discovery can never see, because
// SLIRP carries no multicast.
func probeLocalVMDevices(ctx context.Context) []models.LANDevice {
	statuses, err := vmStatusesFn()
	if err != nil {
		return nil
	}

	type candidate struct {
		name string
		port int
	}
	var candidates []candidate
	for _, st := range statuses {
		if st.Running && st.State.NetMode == vm.NetUser && st.State.AgentPort != 0 {
			candidates = append(candidates, candidate{st.Name, st.State.AgentPort})
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// Bounded like the USB probe: a QEMU forward accepts on the host before it
	// checks the guest, so a booting VM passes the connect gate and reaches the
	// full auto-TLS ladder. Without a budget discovery would block on it.
	pctx, cancel := context.WithTimeout(ctx, vmProbeBudget)
	defer cancel()

	results := make([]*models.LANDevice, len(candidates))
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		go func(i int, c candidate) {
			defer wg.Done()
			// The forward maps host c.port onto the guest's plaintext port and
			// c.port+1 onto its mTLS port, so a guest port shifts by the same
			// delta to reach the host side.
			hostAddrFor := func(guestPort int) string {
				return fmt.Sprintf("127.0.0.1:%d", c.port+guestPort-defaultAgentPort)
			}
			if !anyAgentPortAnswers(pctx, hostAddrFor) {
				return
			}
			isMTLS, resp, err := getAgentVersionAtAddress(pctx, hostAddrFor(defaultAgentPort))
			if err != nil || resp == nil {
				return
			}
			advertisedPort := c.port
			if isMTLS {
				advertisedPort += agentMTLSPortOffset
			}
			results[i] = &models.LANDevice{
				ID:          vmDeviceIDPrefix + c.name,
				DisplayName: c.name,
				// Hostname stays empty on purpose. Every VM booted from the
				// same image reports the same guest hostname, and the dedup key
				// prefers the hostname -- two VMs would collapse into one row.
				IPAddress: "127.0.0.1",
				// A provisioned agent advertises its mTLS port; lanAgentAddresses
				// subtracts the offset back off to recover what to dial.
				Port:            advertisedPort,
				IsMTLS:          isMTLS,
				InterfaceType:   string(models.InterfaceLAN),
				IsWendyDevice:   true,
				AgentVersion:    resp.GetVersion(),
				OS:              resp.GetOs(),
				OSVersion:       resp.GetOsVersion(),
				CPUArchitecture: resp.GetCpuArchitecture(),
				DeviceType:      resp.GetDeviceType(),
			}
		}(i, c)
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

// discoverLocalTargets runs ordinary discovery plus the sources that only this
// machine can see: USB-direct links and local VMs.
// The launcher forwards a span of host ports starting at its own default, and
// that span has to cover the port provisioning moves the agent to -- otherwise
// a provisioned VM silently goes unreachable. The constants live in two
// packages, so this is what keeps them honest: a mismatch fails to compile.
var (
	_ = [1]struct{}{}[defaultAgentPort-vm.DefaultAgentPort]
	_ = [1]struct{}{}[vm.AgentPortSpan-(agentMTLSPortOffset+1)]
)

func discoverLocalTargets(ctx context.Context, opts discovery.DiscoveryOptions) (*models.DevicesCollection, error) {
	var vms []models.LANDevice
	var wg sync.WaitGroup
	// Same filter as the USB-direct probe: both synthesise LAN-type rows, so
	// both run exactly when LAN discovery would.
	if shouldProbeUSBDirect(opts) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vms = probeLocalVMDevices(ctx)
		}()
	}
	collection, err := discoverWithUSBDirect(ctx, opts)
	wg.Wait()
	if collection == nil {
		collection = &models.DevicesCollection{}
	}
	// Merged in even when LAN discovery failed, so the rows are there for a
	// caller that renders a partial result. Both of today's callers surface
	// the error instead, which is why the error is still returned.
	collection.LANDevices = append(collection.LANDevices, vms...)
	return collection, err
}
