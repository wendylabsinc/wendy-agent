package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

// TestRetryOnHandshakeTimeout covers the classify-and-retry logic added to
// tolerate slow post-quantum ML-DSA mTLS handshakes on constrained hardware
// (Jetson, Raspberry Pi): retry on timeout-class errors, never on genuine
// certificate rejections, and stop once maxHandshakeTimeoutRetries is
// exhausted.
func TestRetryOnHandshakeTimeout(t *testing.T) {
	timeoutErr := errors.New("192.168.1.50:50052: connection timed out")
	deadlineErr := errors.New("rpc error: code = DeadlineExceeded desc = context deadline exceeded")
	certRejectedErr := errors.New("remote error: tls: bad certificate")
	otherErr := errors.New("connection refused")

	setup := func() (restoreJSON func()) {
		origJSON := jsonOutput
		jsonOutput = false
		return func() { jsonOutput = origJSON }
	}

	t.Run("retries on timeout-class error until success", func(t *testing.T) {
		restore := setup()
		defer restore()

		wantConn := &grpcclient.AgentConnection{Host: "device.local"}
		var calls int
		conn, err, ok := retryOnHandshakeTimeout(context.Background(), timeoutErr, func() (*grpcclient.AgentConnection, error) {
			calls++
			if calls < 2 {
				return nil, timeoutErr
			}
			return wantConn, nil
		})
		if !ok || conn != wantConn || err != nil {
			t.Fatalf("retryOnHandshakeTimeout() = (%v, %v, %v), want (%v, nil, true)", conn, err, ok, wantConn)
		}
		if calls != 2 {
			t.Fatalf("retry calls = %d, want 2", calls)
		}
	})

	t.Run("recognizes context deadline exceeded as timeout-class", func(t *testing.T) {
		restore := setup()
		defer restore()

		wantConn := &grpcclient.AgentConnection{Host: "device.local"}
		conn, err, ok := retryOnHandshakeTimeout(context.Background(), deadlineErr, func() (*grpcclient.AgentConnection, error) {
			return wantConn, nil
		})
		if !ok || conn != wantConn || err != nil {
			t.Fatalf("retryOnHandshakeTimeout() = (%v, %v, %v), want (%v, nil, true)", conn, err, ok, wantConn)
		}
	})

	t.Run("never retries a genuine certificate rejection", func(t *testing.T) {
		restore := setup()
		defer restore()

		var calls int
		_, err, ok := retryOnHandshakeTimeout(context.Background(), certRejectedErr, func() (*grpcclient.AgentConnection, error) {
			calls++
			return nil, nil
		})
		if ok {
			t.Fatal("expected ok=false for a certificate rejection")
		}
		if err != certRejectedErr {
			t.Fatalf("returned error = %v, want the original cause %v passed through unchanged", err, certRejectedErr)
		}
		if calls != 0 {
			t.Fatalf("retry calls = %d, want 0 (must not retry cert rejections)", calls)
		}
	})

	t.Run("never retries an unrelated error", func(t *testing.T) {
		restore := setup()
		defer restore()

		var calls int
		_, err, ok := retryOnHandshakeTimeout(context.Background(), otherErr, func() (*grpcclient.AgentConnection, error) {
			calls++
			return nil, nil
		})
		if ok {
			t.Fatal("expected ok=false for an unrelated error")
		}
		if err != otherErr {
			t.Fatalf("returned error = %v, want the original cause %v passed through unchanged", err, otherErr)
		}
		if calls != 0 {
			t.Fatalf("retry calls = %d, want 0", calls)
		}
	})

	t.Run("gives up after maxHandshakeTimeoutRetries", func(t *testing.T) {
		restore := setup()
		defer restore()

		var calls int
		_, err, ok := retryOnHandshakeTimeout(context.Background(), timeoutErr, func() (*grpcclient.AgentConnection, error) {
			calls++
			return nil, timeoutErr
		})
		if ok {
			t.Fatal("expected ok=false when every retry also times out")
		}
		if err != timeoutErr {
			t.Fatalf("returned error = %v, want the last timeout %v", err, timeoutErr)
		}
		if calls != maxHandshakeTimeoutRetries {
			t.Fatalf("retry calls = %d, want %d", calls, maxHandshakeTimeoutRetries)
		}
	})

	t.Run("stops retrying once a retry comes back as a genuine rejection", func(t *testing.T) {
		restore := setup()
		defer restore()

		var calls int
		_, err, ok := retryOnHandshakeTimeout(context.Background(), timeoutErr, func() (*grpcclient.AgentConnection, error) {
			calls++
			return nil, certRejectedErr
		})
		if ok {
			t.Fatal("expected ok=false once a retry reveals a genuine rejection")
		}
		// The fresher, more specific error must be surfaced so the caller's
		// downstream handling (cert-refresh offer) diagnoses the real failure
		// instead of the original timeout.
		if err != certRejectedErr {
			t.Fatalf("returned error = %v, want the fresher rejection %v surfaced to the caller", err, certRejectedErr)
		}
		if calls != 1 {
			t.Fatalf("retry calls = %d, want 1 (must stop immediately on a non-timeout failure)", calls)
		}
	})

	t.Run("stops retrying once the context is cancelled", func(t *testing.T) {
		restore := setup()
		defer restore()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var calls int
		_, _, ok := retryOnHandshakeTimeout(ctx, timeoutErr, func() (*grpcclient.AgentConnection, error) {
			calls++
			return nil, nil
		})
		if ok {
			t.Fatal("expected ok=false for a cancelled context")
		}
		if calls != 0 {
			t.Fatalf("retry calls = %d, want 0 (context already done)", calls)
		}
	})

	t.Run("json mode still retries silently", func(t *testing.T) {
		restore := setup()
		defer restore()
		jsonOutput = true

		wantConn := &grpcclient.AgentConnection{Host: "device.local"}
		conn, err, ok := retryOnHandshakeTimeout(context.Background(), timeoutErr, func() (*grpcclient.AgentConnection, error) {
			return wantConn, nil
		})
		if !ok || conn != wantConn || err != nil {
			t.Fatalf("retryOnHandshakeTimeout() = (%v, %v, %v), want (%v, nil, true)", conn, err, ok, wantConn)
		}
	})
}
