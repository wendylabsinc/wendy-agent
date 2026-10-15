//go:build linux

package localmesh

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/WendyOS/go/babel"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"golang.org/x/sys/unix"
)

const interfaceOwner = "wendy:local-mesh:2"
const RouteTable = 18865
const routeProtocol = 186

type kernelPeer struct {
	link *netlink.Tuntap
	file *os.File
	rule *netlink.Rule
}

// Kernel owns only links it creates. It does not enable forwarding, change
// default routes, or touch the old NAN implementation. Session policy owns those
// operations and must quiesce transport around Apply.
//
// The mesh is IPv6-only: the host link carries the device's own ULA address, each
// point-to-point TUN carries the complementary pair derived from the two device
// UUIDs, and Babel routes are installed as ULA destinations with link-local next
// hops on the interface they arrived on.
type Kernel struct {
	link    *netlink.Dummy
	peers   map[babel.LinkID]kernelPeer
	routes  []netlink.Route
	rule    *netlink.Rule
	device  meshid.Device
	address net.IP
}

func NewKernel(device meshid.Device) (*Kernel, error) {
	address := device.MeshAddress()
	// Reserved route table must be empty; never adopt foreign or stale state.
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		existing, err := netlink.RouteListFiltered(family, &netlink.Route{Table: RouteTable}, netlink.RT_FILTER_TABLE)
		if err != nil {
			return nil, err
		}
		if len(existing) != 0 {
			return nil, errors.New("local-mesh route table already occupied")
		}
	}
	rules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if r.Table == RouteTable || r.Priority == RouteTable {
			return nil, errors.New("local-mesh rule ownership conflict")
		}
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = InterfaceName
	attrs.Alias = interfaceOwner
	attrs.MTU = TunnelMTU
	k := &Kernel{link: &netlink.Dummy{LinkAttrs: attrs}, peers: map[babel.LinkID]kernelPeer{}, device: device, address: net.IP(address.AsSlice())}
	if err = netlink.LinkAdd(k.link); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = k.Close()
		}
	}()
	if err = netlink.LinkSetAlias(k.link, interfaceOwner); err != nil {
		return nil, err
	}
	if err = netlink.AddrAdd(k.link, &netlink.Addr{IPNet: ipNet(netip.PrefixFrom(address, 128)), Flags: unix.IFA_F_NOPREFIXROUTE}); err != nil {
		return nil, err
	}
	if err = netlink.LinkSetUp(k.link); err != nil {
		return nil, err
	}
	// A permanent unreachable default prevents this table from falling through
	// to the host's ordinary uplink. A learnt route has a lower priority.
	guard := netlink.Route{Table: RouteTable, Protocol: routeProtocol, Family: netlink.FAMILY_V6, Type: unix.RTN_UNREACHABLE, Priority: 32767, Dst: ipNet(netip.MustParsePrefix("::/0"))}
	if err = netlink.RouteAdd(&guard); err != nil {
		return nil, err
	}
	k.routes = append(k.routes, guard)
	// Mesh traffic is looked up in this table only. Everything else keeps the
	// host's ordinary routing.
	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V6
	rule.Priority = RouteTable
	rule.Table = RouteTable
	rule.Dst = ipNet(meshid.MeshPrefix)
	if err = netlink.RuleAdd(rule); err != nil {
		return nil, err
	}
	k.rule = rule
	ok = true
	return k, nil
}

func ipNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: net.IP(p.Addr().AsSlice()), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}

func (k *Kernel) AddPeer(id babel.LinkID, peer meshid.Device) (*os.File, error) {
	if id == 0 || uint64(id) > 0xffffffff {
		return nil, errors.New("kernel link incarnation out of range")
	}
	if _, ok := k.peers[id]; ok {
		return nil, errors.New("duplicate kernel link")
	}
	if peer.Principal() == k.device.Principal() {
		return nil, errors.New("self link")
	}
	local, _, err := meshid.LinkLocalAddresses(k.device, peer)
	if err != nil {
		return nil, err
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = fmt.Sprintf("wlmp%x", uint64(id))
	attrs.MTU = TunnelMTU
	attrs.Alias = interfaceOwner
	tun := &netlink.Tuntap{LinkAttrs: attrs, Mode: netlink.TUNTAP_MODE_TUN, Flags: netlink.TUNTAP_NO_PI | netlink.TUNTAP_TUN_EXCL, Queues: 1, NonPersist: true}
	if err = netlink.LinkAdd(tun); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			for _, f := range tun.Fds {
				_ = f.Close()
			}
		}
	}()
	if len(tun.Fds) != 1 {
		return nil, errors.New("TUN did not return one queue")
	}
	if err = netlink.LinkSetAlias(tun, interfaceOwner); err != nil {
		return nil, err
	}
	if err = netlink.LinkSetMTU(tun, TunnelMTU); err != nil {
		return nil, err
	}
	// Babel owns addressing and routing on this point-to-point link. Never
	// accept peer router advertisements or manufacture SLAAC/default routes.
	// accept_ra_rt_info_max_plen only bounds routes carried inside router
	// information options, and some kernel builds do not expose it at all;
	// accept_ra=0 already blocks every advertisement. The knobs that actually
	// stop SLAAC, prefix assignment and solicitations stay mandatory.
	for _, setting := range []struct {
		name     string
		optional bool
	}{
		{"accept_ra", false},
		{"autoconf", false},
		{"accept_ra_rt_info_max_plen", true},
		{"router_solicitations", false},
	} {
		if err = os.WriteFile("/proc/sys/net/ipv6/conf/"+attrs.Name+"/"+setting.name, []byte("0\n"), 0600); err != nil {
			if setting.optional && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
	}
	if err = netlink.AddrAdd(tun, &netlink.Addr{IPNet: ipNet(netip.PrefixFrom(local, 64)), Flags: unix.IFA_F_NODAD}); err != nil {
		return nil, err
	}
	// The device's own mesh address is the source for payload leaving this link.
	if err = netlink.AddrAdd(tun, &netlink.Addr{IPNet: ipNet(netip.PrefixFrom(k.device.MeshAddress(), 128)), Flags: unix.IFA_F_NOPREFIXROUTE}); err != nil {
		return nil, err
	}
	if err = netlink.LinkSetUp(tun); err != nil {
		return nil, err
	}
	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V6
	rule.Priority = RouteTable + 1
	rule.Table = RouteTable
	rule.IifName = attrs.Name
	if err = netlink.RuleAdd(rule); err != nil {
		return nil, err
	}
	k.peers[id] = kernelPeer{tun, tun.Fds[0], rule}
	ok = true
	return tun.Fds[0], nil
}

func (k *Kernel) RemovePeer(id babel.LinkID) error {
	p, ok := k.peers[id]
	if !ok {
		return nil
	}
	delete(k.peers, id)
	return errors.Join(netlink.RuleDel(p.rule), p.file.Close())
}

// Apply installs ULA routes with IPv6 link-local next hops. The caller MUST hold
// the packet forwarding gate until all routes and durable engine state are
// committed. On any error it must call Stop, not resume a partial FIB.
func (k *Kernel) Apply(routes []babel.Route) error {
	var desired []netlink.Route
	for _, r := range routes {
		if r.Local {
			continue
		}
		if !r.Prefix.Addr().Is6() || r.Prefix.Addr().Is4In6() {
			return errors.New("local-mesh payload routing is IPv6-only")
		}
		n := netlink.Route{Table: RouteTable, Protocol: routeProtocol, Family: netlink.FAMILY_V6, Dst: ipNet(r.Prefix), Priority: 100, Type: unix.RTN_UNICAST}
		if r.Unreachable {
			n.Type = unix.RTN_UNREACHABLE
		} else {
			n.Src = k.address
			p, ok := k.peers[r.Link]
			if !ok {
				return errors.New("route references missing kernel link")
			}
			if !r.NextHop.Is6() || !r.NextHop.IsLinkLocalUnicast() {
				return errors.New("route has invalid scoped next hop")
			}
			n.LinkIndex = p.link.Index
			// A native IPv6 route names its next hop with RTA_GATEWAY; RTA_VIA
			// (netlink.Via) is the cross-family form and kernels reject it here.
			n.Gw = net.IP(r.NextHop.AsSlice())
		}
		desired = append(desired, n)
	}
	// Keep the permanent guard. Add/replace before deleting old prefixes, so
	// neither a withdrawal nor a route change creates a default fallback gap.
	for i := range desired {
		if err := netlink.RouteReplace(&desired[i]); err != nil {
			return err
		}
		// Track each successful mutation immediately for cleanup if a later
		// netlink operation fails. Replace the prior entry for this prefix.
		found := false
		for j := range k.routes {
			if k.routes[j].Priority != 32767 && k.routes[j].Dst.String() == desired[i].Dst.String() {
				k.routes[j] = desired[i]
				found = true
				break
			}
		}
		if !found {
			k.routes = append(k.routes, desired[i])
		}
	}
	for _, old := range k.routes {
		if old.Priority == 32767 {
			continue
		}
		keep := false
		for _, n := range desired {
			if n.Dst.String() == old.Dst.String() {
				keep = true
				break
			}
		}
		if !keep {
			if err := netlink.RouteDel(&old); err != nil && !errors.Is(err, unix.ESRCH) {
				return err
			}
		}
	}
	k.routes = append(k.routes[:1], desired...)
	return nil
}

func (k *Kernel) Stop() error {
	var errs []error
	for _, p := range k.peers {
		errs = append(errs, netlink.LinkSetDown(p.link))
	}
	return errors.Join(errs...)
}

func (k *Kernel) Close() error {
	var errs []error
	for id := range k.peers {
		errs = append(errs, k.RemovePeer(id))
	}
	if k.rule != nil {
		errs = append(errs, netlink.RuleDel(k.rule))
		k.rule = nil
	}
	for _, r := range k.routes {
		if err := netlink.RouteDel(&r); err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, err)
		}
	}
	k.routes = nil
	live, err := netlink.LinkByName(InterfaceName)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if live.Attrs().Index != k.link.Index || live.Attrs().Alias != interfaceOwner || live.Type() != "dummy" {
		return errors.Join(append(errs, errors.New("local-mesh interface ownership changed"))...)
	}
	errs = append(errs, netlink.LinkDel(live))
	return errors.Join(errs...)
}
