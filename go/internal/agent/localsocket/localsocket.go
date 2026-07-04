// Package localsocket provides the wendy-agent's local unix-domain-socket
// listener. The socket carries the agent's full gRPC with no mTLS; access is
// gated entirely by the admin entitlement, which bind-mounts the socket into
// entitled containers (see oci.applyAdmin).
package localsocket

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Listen creates the socket's parent directory, removes any stale socket at
// path, listens on it, and restricts it to mode 0660.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on unix socket: %w", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		lis.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return lis, nil
}
