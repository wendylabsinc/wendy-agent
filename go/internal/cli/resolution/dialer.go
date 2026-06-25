package resolution

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const maxDialParallelism = 5

// DialFn is a function that dials a plaintext agent address and returns a
// connection. The address is in host:port form (the plaintext port).
type DialFn func(ctx context.Context, plaintextAddr string) (*grpcclient.AgentConnection, error)

// DefaultDialFn is the production dialer. It tries mTLS (port+1) first for
// every certificate stored in the CLI config, then falls back to plaintext.
//
// Design choice: the mTLS-probe logic is reproduced inline here using
// grpcclient.ConnectWithTLS and grpcclient.Connect rather than importing
// internal/cli/commands, which would create an import cycle (commands imports
// resolution). The commands package sets this variable at startup for
// production use; tests inject a stub directly into DialFirst.
var DefaultDialFn DialFn = defaultDial

// defaultDial implements the auto-TLS probe: try each stored cert on port+1;
// if all TLS attempts fail, fall back to plaintext on the given port.
func defaultDial(ctx context.Context, plaintextAddr string) (*grpcclient.AgentConnection, error) {
	host, portStr, err := net.SplitHostPort(plaintextAddr)
	if err != nil {
		return nil, fmt.Errorf("parsing address %q: %w", plaintextAddr, err)
	}

	// Parse port and compute mTLS port (plaintext + 1).
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return nil, fmt.Errorf("parsing port %q: %w", portStr, err)
	}
	mtlsAddr := fmt.Sprintf("%s:%d", host, port+1)

	cfg, err := config.Load()
	if err == nil {
		for i := range cfg.Auth {
			for j := range cfg.Auth[i].Certificates {
				cert := cfg.Auth[i].Certificates[j]
				conn, err := grpcclient.ConnectWithTLS(ctx, mtlsAddr, &cert)
				if err != nil {
					continue
				}
				// Probe to confirm the connection is live.
				_, err = conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
				if err != nil {
					_ = conn.Close()
					continue
				}
				return conn, nil
			}
		}
	}

	// Fallback: plaintext.
	return grpcclient.Connect(ctx, plaintextAddr)
}

// dialResult is the outcome of a single dial attempt.
type dialResult struct {
	conn      *grpcclient.AgentConnection
	err       error
	candidate Candidate
}

// AllFailedError is returned when every candidate in DialFirst fails.
type AllFailedError struct {
	Errors []CandidateError
}

// CandidateError pairs a candidate with the error produced while dialing it.
type CandidateError struct {
	Candidate Candidate
	Err       error
}

func (e *AllFailedError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, ce := range e.Errors {
		parts[i] = fmt.Sprintf("%s: %v", ce.Candidate.Addr(), ce.Err)
	}
	return "all candidates failed: " + strings.Join(parts, "; ")
}

// DialFirst races all candidates concurrently (capped at maxDialParallelism
// in-flight at any moment). The first candidate that completes a successful
// GetAgentVersion probe wins; all other in-flight connections are cancelled
// and closed. Returns the first successful AgentConnection, or an
// *AllFailedError if every candidate fails.
//
// The fn parameter is the dial function to use; pass nil to use DefaultDialFn.
// This parameter exists solely so tests can inject a stub without touching the
// package-level variable.
func DialFirst(ctx context.Context, candidates []Candidate, fn DialFn) (*grpcclient.AgentConnection, error) {
	if fn == nil {
		fn = DefaultDialFn
	}

	if len(candidates) == 0 {
		return nil, &AllFailedError{}
	}

	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan dialResult, len(candidates))
	sem := make(chan struct{}, maxDialParallelism)

	var wg sync.WaitGroup
	for _, c := range candidates {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Acquire semaphore slot.
			select {
			case sem <- struct{}{}:
			case <-dialCtx.Done():
				results <- dialResult{err: dialCtx.Err(), candidate: c}
				return
			}
			defer func() { <-sem }()

			conn, err := fn(dialCtx, c.Addr())
			results <- dialResult{conn: conn, err: err, candidate: c}
		}()
	}

	// Close results channel once all goroutines finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	var errs []CandidateError
	received := 0
	total := len(candidates)

	for r := range results {
		received++
		if r.err != nil {
			errs = append(errs, CandidateError{Candidate: r.candidate, Err: r.err})
		} else {
			// First success: cancel all remaining dials.
			cancel()
			winner := r.conn

			// Drain remaining results and close losing connections.
			for received < total {
				rem, ok := <-results
				if !ok {
					break
				}
				received++
				if rem.conn != nil {
					_ = rem.conn.Close()
				}
			}
			return winner, nil
		}
	}

	return nil, &AllFailedError{Errors: errs}
}
