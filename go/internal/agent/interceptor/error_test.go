package interceptor

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stubServerStream is a no-op grpc.ServerStream for driving the interceptor.
type stubServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *stubServerStream) Context() context.Context { return s.ctx }

// runStreamInterceptor invokes StreamErrorInterceptor with a handler that
// returns handlerErr, returning the captured log entries.
func runStreamInterceptor(t *testing.T, handlerErr error) []observer.LoggedEntry {
	t.Helper()

	// Capture every level so we can assert on both Debug and Error.
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	interceptor := StreamErrorInterceptor(logger)
	info := &grpc.StreamServerInfo{FullMethod: "/wendy.agent.services.v1.WendyContainerService/RunContainer"}
	handler := func(srv any, stream grpc.ServerStream) error { return handlerErr }

	err := interceptor(nil, &stubServerStream{ctx: context.Background()}, info, handler)
	if !errors.Is(err, handlerErr) {
		t.Fatalf("interceptor should pass the handler error through unchanged, got %v", err)
	}

	return logs.All()
}

// TestStreamErrorInterceptor_CancellationLoggedAtDebug verifies that the normal
// cancellation produced when a client closes a stream (e.g. `wendy run
// --detach` returning) is logged at Debug, not Error — otherwise it surfaces in
// `wendy device logs` and reads like the app crashed (issue #1169).
func TestStreamErrorInterceptor_CancellationLoggedAtDebug(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"context.Canceled", context.Canceled},
		{"wrapped context.Canceled", errors.Join(errors.New("run container"), context.Canceled)},
		{"grpc codes.Canceled", status.Error(codes.Canceled, "context canceled")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := runStreamInterceptor(t, tc.err)
			if len(entries) != 1 {
				t.Fatalf("expected exactly one log entry, got %d: %+v", len(entries), entries)
			}
			entry := entries[0]
			if entry.Level != zapcore.DebugLevel {
				t.Errorf("cancellation should be logged at Debug, got %v (%q)", entry.Level, entry.Message)
			}
			if entry.Message == "gRPC stream handler error" {
				t.Errorf("cancellation must not use the error message that reads as a crash")
			}
		})
	}
}

// TestStreamErrorInterceptor_RealErrorLoggedAtError verifies genuine handler
// failures are still logged at Error level.
func TestStreamErrorInterceptor_RealErrorLoggedAtError(t *testing.T) {
	entries := runStreamInterceptor(t, errors.New("boom"))
	if len(entries) != 1 {
		t.Fatalf("expected exactly one log entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Level != zapcore.ErrorLevel {
		t.Errorf("real errors should be logged at Error, got %v", entry.Level)
	}
	if entry.Message != "gRPC stream handler error" {
		t.Errorf("unexpected message %q", entry.Message)
	}
}

// TestStreamErrorInterceptor_NoErrorNoLog verifies a successful handler logs
// nothing.
func TestStreamErrorInterceptor_NoErrorNoLog(t *testing.T) {
	entries := runStreamInterceptor(t, nil)
	if len(entries) != 0 {
		t.Fatalf("expected no log entries for a successful handler, got %d: %+v", len(entries), entries)
	}
}
