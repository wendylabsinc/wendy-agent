//go:build !linux

package containerd

import (
	"context"
	"fmt"
	"net"
)

func dialReadinessNamespace(context.Context, uint32, string, string) (net.Conn, error) {
	return nil, fmt.Errorf("container network readiness requires a Linux runtime")
}
