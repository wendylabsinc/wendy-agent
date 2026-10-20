// Package meshingress tracks the host TCP ports a running, isolated mesh app
// has explicitly published. Peer sessions consult this registry before
// connecting directly to its verified container endpoint.
package meshingress

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
)

var ErrPortDenied = errors.New("wendy net ingress port is not published by a running app")

// Registry is an in-memory authorization table. Each host port has at most
// one container owner, and an owner can publish several ports. The zero value
// is usable. It starts empty, so access fails closed across agent restarts.
type Registry struct {
	flowMu     sync.Mutex // acquired after mu; connection Close never acquires mu
	tcpByOwner map[string]map[*authorizedConn]struct{}
	mu         sync.RWMutex
	byPort     map[uint16]string
	targets    map[uint16]netip.AddrPort
	byOwner    map[string]map[uint16]struct{}
}

func NewRegistry() *Registry { return &Registry{} }

// Allowed reports whether a running container has claimed port. Port zero is
// never valid. The caller must still authenticate the remote mesh peer.
func (r *Registry) Allowed(port uint16) bool {
	if r == nil || port == 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byPort[port] != ""
}

// OwnedBy reports whether this exact live container claimed the host port.
// Recovery uses it to distinguish a successful forward from a sibling app
// container that may publish a different port under the same app ID.
func (r *Registry) OwnedBy(containerID string, port uint16) bool {
	if r == nil || containerID == "" || port == 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byPort[port] == containerID
}

// DialAuthorized holds the admission read lock across a bounded TCP dial.
// The callback must dial the supplied verified CNI endpoint, never the host
// port. Host firewall loss must not redirect an authorized app flow to a host
// listener. Release waits for the dial and then revokes the admitted flow.
func (r *Registry) DialAuthorized(port uint16, dial func(string) (net.Conn, error)) (net.Conn, error) {
	if r == nil || port == 0 || dial == nil {
		return nil, ErrPortDenied
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.byPort[port] == "" || !r.targets[port].IsValid() {
		return nil, ErrPortDenied
	}
	conn, err := dial(r.targets[port].String())
	if err != nil {
		return conn, err
	}
	if conn == nil {
		return nil, errors.New("wendy net ingress dial returned no connection")
	}
	owned := &authorizedConn{Conn: conn, registry: r, owner: r.byPort[port], done: make(chan struct{})}
	r.flowMu.Lock()
	if r.tcpByOwner == nil {
		r.tcpByOwner = make(map[string]map[*authorizedConn]struct{})
	}
	if r.tcpByOwner[owned.owner] == nil {
		r.tcpByOwner[owned.owner] = make(map[*authorizedConn]struct{})
	}
	r.tcpByOwner[owned.owner][owned] = struct{}{}
	r.flowMu.Unlock()
	return owned, nil
}

// authorizedConn retains the exact admission incarnation. Release revokes live
// flows as well as future dials; a late Close cannot revoke a replacement app.
type authorizedConn struct {
	net.Conn
	registry *Registry
	owner    string
	done     chan struct{}
	once     sync.Once
	err      error
}

func (c *authorizedConn) Done() <-chan struct{} { return c.done }
func (c *authorizedConn) Close() error {
	c.once.Do(func() {
		c.registry.flowMu.Lock()
		delete(c.registry.tcpByOwner[c.owner], c)
		if len(c.registry.tcpByOwner[c.owner]) == 0 {
			delete(c.registry.tcpByOwner, c.owner)
		}
		c.registry.flowMu.Unlock()
		close(c.done)
		c.err = c.Conn.Close()
	})
	return c.err
}
func (c *authorizedConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return errors.New("wendy net ingress connection does not support half-close")
}

// CheckAvailable rejects a port already owned by another container. Callers
// must serialize this check with forward installation and Claim; Claim repeats
// the check so a caller that forgets that rule still fails closed.
func (r *Registry) CheckAvailable(containerID string, port uint16) error {
	if r == nil {
		return fmt.Errorf("wendy net ingress registry is unavailable")
	}
	if containerID == "" || port == 0 {
		return fmt.Errorf("wendy net ingress needs a container ID and nonzero host port")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return checkOwner(r.byPort[port], containerID, port)
}

// Claim grants access only after the caller has successfully installed the
// matching host port forward. The caller supplies its verified CNI address
// and container port. Repeating the exact same admission is safe.
func (r *Registry) Claim(containerID string, port uint16, containerIP string, containerPort uint16) error {
	if r == nil {
		return fmt.Errorf("wendy net ingress registry is unavailable")
	}
	if containerID == "" || port == 0 {
		return fmt.Errorf("wendy net ingress needs a container ID and nonzero host port")
	}
	ip, err := netip.ParseAddr(containerIP)
	if err != nil || !ip.Is4() || !ip.IsPrivate() || containerPort == 0 {
		return fmt.Errorf("wendy net ingress needs a private IPv4 container endpoint")
	}
	target := netip.AddrPortFrom(ip, containerPort)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkOwner(r.byPort[port], containerID, port); err != nil {
		return err
	}
	if r.byPort[port] != "" && r.targets[port] != target {
		return fmt.Errorf("wendy net ingress host port %d cannot change its live container endpoint", port)
	}
	if r.byPort == nil {
		r.byPort = make(map[uint16]string)
		r.targets = make(map[uint16]netip.AddrPort)
		r.byOwner = make(map[string]map[uint16]struct{})
	}
	if r.byOwner[containerID] == nil {
		r.byOwner[containerID] = make(map[uint16]struct{})
	}
	r.byPort[port] = containerID
	r.targets[port] = target
	r.byOwner[containerID][port] = struct{}{}
	return nil
}

func checkOwner(owner, containerID string, port uint16) error {
	if owner != "" && owner != containerID {
		return fmt.Errorf("wendy net ingress host port %d is already published by container %q", port, owner)
	}
	return nil
}

// Release revokes all ports owned by containerID. It is safe to call after a
// partial start, stop, delete, or again during teardown.
func (r *Registry) Release(containerID string) {
	if r == nil || containerID == "" {
		return
	}
	r.mu.Lock()
	for port := range r.byOwner[containerID] {
		if r.byPort[port] == containerID {
			delete(r.byPort, port)
			delete(r.targets, port)
		}
	}
	delete(r.byOwner, containerID)
	r.flowMu.Lock()
	flows := r.tcpByOwner[containerID]
	delete(r.tcpByOwner, containerID)
	r.flowMu.Unlock()
	r.mu.Unlock()
	// Never hold the admission lock while closing sockets. Their callbacks
	// may complete teardown, and unrelated apps must remain available.
	for flow := range flows {
		_ = flow.Close()
	}
}
