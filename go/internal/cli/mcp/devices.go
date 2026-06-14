package mcp

import (
	"context"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

// cachedConn wraps a live agent connection; host is the cache key and UI label.
type cachedConn struct {
	host string
	conn *grpcclient.AgentConnection
}

func (s *mcpServer) cacheConn(key string, c *cachedConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connCache == nil {
		s.connCache = map[string]*cachedConn{}
	}
	s.connCache[key] = c
}

// resolveConn returns the connection for `device`. Empty device falls back to
// the active connection. A cached device reuses its connection; an uncached
// device is dialed on demand and cached.
func (s *mcpServer) resolveConn(ctx context.Context, device string) (*grpcclient.AgentConnection, error) {
	if device == "" {
		if c := s.GetConn(); c != nil {
			return c, nil
		}
		return nil, fmt.Errorf("no device connected — use device_connect first or pass a device argument")
	}
	s.mu.RLock()
	cached := s.connCache[device]
	s.mu.RUnlock()
	if cached != nil && cached.conn != nil {
		return cached.conn, nil
	}
	if s.connectFn == nil {
		return nil, fmt.Errorf("no connect function configured")
	}
	conn, err := s.connectFn(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", device, err)
	}
	s.cacheConn(device, &cachedConn{host: conn.Host, conn: conn})
	return conn, nil
}
