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
	// Defence in depth: the socket carries the agent's full gRPC with no auth,
	// so its file permissions ARE the access control. Assert the mode is not
	// world-accessible before serving — a stray umask, a filesystem that ignored
	// the chmod, or a future edit that widened it must fail loudly rather than
	// silently expose the control plane to every host UID.
	fi, err := os.Stat(path)
	if err != nil {
		lis.Close()
		return nil, fmt.Errorf("stat socket: %w", err)
	}
	if fi.Mode().Perm()&0o007 != 0 {
		lis.Close()
		return nil, fmt.Errorf("socket %s is world-accessible (mode %#o); refusing to serve", path, fi.Mode().Perm())
	}
	return lis, nil
}
