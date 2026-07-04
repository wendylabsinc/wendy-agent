package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

func TestAutoSyncTimeAndRetry(t *testing.T) {
	skewErr := newTLSHandshakeRejectedError(errors.New("remote error: tls: bad certificate"))

	setup := func(broadcastErr error) (restore func(), broadcastCalls, retryCalls *int) {
		origBroadcast := broadcastTimeFn
		origSleep := clockSkewSyncSleep
		origJSON := jsonOutput
		origAttempted := clockSkewSyncAttempted
		broadcastCalls = new(int)
		retryCalls = new(int)
		broadcastTimeFn = func(context.Context) error {
			*broadcastCalls++
			return broadcastErr
		}
		clockSkewSyncSleep = func(time.Duration) {} // no real sleep in tests
		jsonOutput = false
		clockSkewSyncAttempted = false
		return func() {
			broadcastTimeFn = origBroadcast
			clockSkewSyncSleep = origSleep
			jsonOutput = origJSON
			clockSkewSyncAttempted = origAttempted
		}, broadcastCalls, retryCalls
	}

	t.Run("clock skew error syncs time and retries to success", func(t *testing.T) {
		restore, broadcastCalls, retryCalls := setup(nil)
		defer restore()

		wantConn := &grpcclient.AgentConnection{Host: "device.local"}
		conn, ok := autoSyncTimeAndRetry(context.Background(), skewErr, func() (*grpcclient.AgentConnection, error) {
			*retryCalls++
			return wantConn, nil
		})
		if !ok || conn != wantConn {
			t.Fatalf("autoSyncTimeAndRetry() = (%v, %v), want (%v, true)", conn, ok, wantConn)
		}
		if *broadcastCalls != 1 || *retryCalls != 1 {
			t.Fatalf("broadcastCalls=%d retryCalls=%d, want 1 and 1", *broadcastCalls, *retryCalls)
		}
	})

	t.Run("cert refreshable error also triggers sync and retry", func(t *testing.T) {
		restore, broadcastCalls, retryCalls := setup(nil)
		defer restore()

		certErr := errors.New("remote error: tls: bad certificate")
		wantConn := &grpcclient.AgentConnection{Host: "device.local"}
		conn, ok := autoSyncTimeAndRetry(context.Background(), certErr, func() (*grpcclient.AgentConnection, error) {
			*retryCalls++
			return wantConn, nil
		})
		if !ok || conn != wantConn {
			t.Fatalf("autoSyncTimeAndRetry() = (%v, %v), want (%v, true)", conn, ok, wantConn)
		}
		if *broadcastCalls != 1 || *retryCalls != 1 {
			t.Fatalf("broadcastCalls=%d retryCalls=%d, want 1 and 1", *broadcastCalls, *retryCalls)
		}
	})

	t.Run("syncs at most once per run", func(t *testing.T) {
		restore, broadcastCalls, retryCalls := setup(nil)
		defer restore()

		// First call attempts the sync (retry fails so the connection is not fixed).
		_, _ = autoSyncTimeAndRetry(context.Background(), skewErr, func() (*grpcclient.AgentConnection, error) {
			*retryCalls++
			return nil, errors.New("still rejected")
		})
		// Second call must not sync or retry again.
		_, ok := autoSyncTimeAndRetry(context.Background(), skewErr, func() (*grpcclient.AgentConnection, error) {
			*retryCalls++
			return nil, nil
		})
		if ok {
			t.Fatal("expected ok=false on second attempt")
		}
		if *broadcastCalls != 1 {
			t.Fatalf("broadcastCalls=%d, want 1 (synced only once)", *broadcastCalls)
		}
		if *retryCalls != 1 {
			t.Fatalf("retryCalls=%d, want 1 (retried only on first attempt)", *retryCalls)
		}
	})

	t.Run("broadcast failure does not retry", func(t *testing.T) {
		restore, broadcastCalls, retryCalls := setup(errors.New("roughtime servers unreachable"))
		defer restore()

		_, ok := autoSyncTimeAndRetry(context.Background(), skewErr, func() (*grpcclient.AgentConnection, error) {
			*retryCalls++
			return nil, nil
		})
		if ok || *broadcastCalls != 1 || *retryCalls != 0 {
			t.Fatalf("ok=%v broadcastCalls=%d retryCalls=%d, want false, 1, 0", ok, *broadcastCalls, *retryCalls)
		}
	})

	t.Run("non-skew error never syncs", func(t *testing.T) {
		restore, broadcastCalls, retryCalls := setup(nil)
		defer restore()

		_, ok := autoSyncTimeAndRetry(context.Background(), errors.New("connection refused"), func() (*grpcclient.AgentConnection, error) {
			*retryCalls++
			return nil, nil
		})
		if ok || *broadcastCalls != 0 || *retryCalls != 0 {
			t.Fatalf("ok=%v broadcastCalls=%d retryCalls=%d, want false, 0, 0", ok, *broadcastCalls, *retryCalls)
		}
	})

	t.Run("failed retry returns not ok", func(t *testing.T) {
		restore, _, _ := setup(nil)
		defer restore()

		_, ok := autoSyncTimeAndRetry(context.Background(), skewErr, func() (*grpcclient.AgentConnection, error) {
			return nil, errors.New("still rejected")
		})
		if ok {
			t.Fatal("expected ok=false when retry fails")
		}
	})

	t.Run("json mode still syncs and retries", func(t *testing.T) {
		restore, broadcastCalls, retryCalls := setup(nil)
		defer restore()
		jsonOutput = true

		wantConn := &grpcclient.AgentConnection{Host: "device.local"}
		conn, ok := autoSyncTimeAndRetry(context.Background(), skewErr, func() (*grpcclient.AgentConnection, error) {
			*retryCalls++
			return wantConn, nil
		})
		if !ok || conn != wantConn || *broadcastCalls != 1 || *retryCalls != 1 {
			t.Fatalf("ok=%v conn=%v broadcastCalls=%d retryCalls=%d, want true, conn, 1, 1", ok, conn, *broadcastCalls, *retryCalls)
		}
	})
}
