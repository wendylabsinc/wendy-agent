package mcp

import "time"

// proxyDiagEntry records a single container-MCP proxy failure so it can be
// surfaced to callers instead of vanishing to stderr.
type proxyDiagEntry struct {
	AppName string `json:"app_name"`
	Stage   string `json:"stage"`
	Error   string `json:"error"`
	Time    string `json:"time"`
}

// recordProxyDiag appends a diagnostic entry for a container-MCP proxy
// failure. No-op if err is nil.
func (s *mcpServer) recordProxyDiag(appName, stage string, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxyDiag = append(s.proxyDiag, proxyDiagEntry{
		AppName: appName,
		Stage:   stage,
		Error:   err.Error(),
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

// proxyDiagnostics returns a copy of all recorded container-MCP proxy
// diagnostics.
func (s *mcpServer) proxyDiagnostics() []proxyDiagEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]proxyDiagEntry, len(s.proxyDiag))
	copy(out, s.proxyDiag)
	return out
}
