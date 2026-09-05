//go:build linux

package containerd

import (
	"context"
	"fmt"
	"net"

	"github.com/containernetworking/plugins/pkg/ns"
)

func dialReadinessNamespace(ctx context.Context, pid uint32, network, address string) (conn net.Conn, err error) {
	// Only numeric IPv4 loopback is used: no host DNS resolver, proxy, or
	// parallel IPv4/IPv6 dial can escape the target's network namespace.
	netns, err := ns.GetNS(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return nil, fmt.Errorf("opening application network namespace: %w", err)
	}
	defer netns.Close()
	err = netns.Do(func(ns.NetNS) error {
		var dialErr error
		conn, dialErr = (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		return dialErr
	})
	return conn, err
}
