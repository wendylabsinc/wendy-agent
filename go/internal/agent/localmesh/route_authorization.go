package localmesh

import (
	"errors"
	"net/netip"
	"time"

	"github.com/wendylabsinc/WendyOS/babel"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// RouteOrigin is the exact Babel update a device authorizes in its signed
// manifest. The router ID and sequence number are origin fields; a relay's next
// hop and metric remain path-dependent and are not claims by the destination.
type RouteOrigin struct {
	Prefix   netip.Prefix   `json:"prefix"`
	RouterID babel.RouterID `json:"routerId"`
	Seqno    uint16         `json:"seqno"`
}

func (m Manifest) validateRoute(device meshid.Device) error {
	if m.Route == nil {
		return nil
	}
	if m.Route.Prefix != netip.PrefixFrom(device.MeshAddress(), 128) {
		return errors.New("route authorization must name the signer's exact mesh /128")
	}
	if m.Route.RouterID == 0 || m.Route.RouterID == ^babel.RouterID(0) {
		return errors.New("invalid authorized router ID")
	}
	return nil
}

// RouteAuthorizations returns a detached snapshot for route admission. It checks
// current trust and requires a live version-2 lease. Conflicting address claims
// by different principals authorize neither origin. A signed origin proves
// address ownership, not the honesty or reachability of its relays.
func (d *Directory) RouteAuthorizations(now time.Time) map[RouteOrigin]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked(now)
	routes := make(map[RouteOrigin]bool)
	owners := make(map[RouteOrigin]string)
	prefixOwners := make(map[netip.Prefix]string)
	conflicts := make(map[string]bool)
	for principal, e := range d.entries {
		m := e.manifest
		if e.expired || m.Withdraw || m.Version != ManifestVersionRoute || m.Route == nil {
			continue
		}
		if _, _, ok := d.cache.Get(principal, now); !ok {
			continue
		}
		route := *m.Route
		if owner, ok := prefixOwners[route.Prefix]; ok && owner != principal {
			conflicts[owner], conflicts[principal] = true, true
		}
		prefixOwners[route.Prefix] = principal
		owners[route] = principal
	}
	for route, principal := range owners {
		if !conflicts[principal] {
			routes[route] = true
		}
	}
	return routes
}
